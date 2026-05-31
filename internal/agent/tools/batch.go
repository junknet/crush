package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"golang.org/x/sync/errgroup"
)

const (
	EvidenceBatchToolName = "Batch"
)

// RegistrySetter allows the coordinator to inject the complete map of unwrapped or wrapped tools.
type RegistrySetter interface {
	SetRegistry(registry map[string]fantasy.AgentTool)
}

// BatchProgressBroker is the broker for publishing progress updates.
var BatchProgressBroker = pubsub.NewBroker[BatchProgress]()

type BatchSubcallState string

const (
	BatchSubcallRunning   BatchSubcallState = "running"
	BatchSubcallSucceeded BatchSubcallState = "succeeded"
	BatchSubcallFailed    BatchSubcallState = "failed"
)

type BatchSubcallProgress struct {
	ID    string            `json:"id"`
	Name  string            `json:"name"`
	State BatchSubcallState `json:"state"`
}

type BatchProgress struct {
	SessionID  string                 `json:"session_id"`
	ToolCallID string                 `json:"tool_call_id"`
	Total      int                    `json:"total"`
	Completed  int                    `json:"completed"`
	Running    int                    `json:"running"`
	Subcalls   []BatchSubcallProgress `json:"subcalls"`
}

func SubscribeBatchProgress(ctx context.Context) <-chan pubsub.Event[BatchProgress] {
	return BatchProgressBroker.Subscribe(ctx)
}

type EvidenceBatchTool struct {
	lspManager   *lsp.Manager
	permissions  permission.Service
	workingDir   string
	client       *http.Client
	registry     map[string]fantasy.AgentTool
	providerOpts fantasy.ProviderOptions
	mu           sync.Mutex
}

func NewEvidenceBatchTool(lspManager *lsp.Manager, permissions permission.Service, workingDir string, client *http.Client) *EvidenceBatchTool {
	return &EvidenceBatchTool{
		lspManager:  lspManager,
		permissions: permissions,
		workingDir:  workingDir,
		client:      client,
	}
}

func (b *EvidenceBatchTool) SetRegistry(registry map[string]fantasy.AgentTool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.registry = registry
}

// Info returns the metadata of the Batch tool.
func (b *EvidenceBatchTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        EvidenceBatchToolName,
		Description: "Run multiple tools in parallel (up to 16 concurrently) to batch content search, file finding, reading, or running terminal commands.",
	}
}

func (b *EvidenceBatchTool) ProviderOptions() fantasy.ProviderOptions {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.providerOpts
}

func (b *EvidenceBatchTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.providerOpts = opts
}

type BatchNode struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Tool      string   `json:"tool,omitempty"`       // Legacy fallback for tool name
	DependsOn []string `json:"depends_on,omitempty"` // Legacy ignored
	OnFailure string   `json:"on_failure,omitempty"` // Legacy ignored

	// Parameters for run_short_command / bash
	Command string `json:"command,omitempty"`

	// Parameters for search_files / search_text / Search / Find / Grep
	Query       string `json:"query,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
	LiteralText bool   `json:"literal_text,omitempty"`
	FilesOnly   bool   `json:"files_only,omitempty"`
	Include     string `json:"include,omitempty"`

	// Parameters for read_file / View / Read
	Path  string `json:"path,omitempty"`
	Limit int    `json:"limit,omitempty"`

	// Parameters for list_tree / ReadDir
	Depth int `json:"depth,omitempty"`

	// Arbitrary custom inputs (for arguments/parameters)
	Args       map[string]any `json:"args,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

type BatchParams struct {
	MaxParallel int         `json:"max_parallel"`
	Nodes       []BatchNode `json:"nodes"`
}

type BatchNodeResult struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "completed", "failed", "skipped"
	Output string `json:"output,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Error  string `json:"error,omitempty"`
}

type BatchSummary struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

type BatchResponse struct {
	Nodes   []BatchNodeResult `json:"nodes"`
	Summary BatchSummary      `json:"summary"`
}

func (b *EvidenceBatchTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params BatchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("batch: failed to parse input: %v", err)), nil
	}

	if len(params.Nodes) == 0 {
		respData := BatchResponse{
			Nodes: []BatchNodeResult{},
			Summary: BatchSummary{
				Total:     0,
				Completed: 0,
				Failed:    0,
			},
		}
		metadataBytes, _ := json.Marshal(respData)
		return fantasy.ToolResponse{
			Content:  "No nodes executed.",
			Metadata: string(metadataBytes),
		}, nil
	}

	// 限制并发度
	maxParallel := params.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 4
	}
	if maxParallel > 16 {
		maxParallel = 16
	}

	// 分配字节预算，限额为 50000 字节公平分配
	byteLimitPerNode := 50000 / len(params.Nodes)
	if byteLimitPerNode < 500 {
		byteLimitPerNode = 500
	}

	// 初始化进度数据
	sessionID := GetSessionFromContext(ctx)
	progress := BatchProgress{
		SessionID:  sessionID,
		ToolCallID: call.ID,
		Total:      len(params.Nodes),
		Completed:  0,
		Running:    0,
		Subcalls:   make([]BatchSubcallProgress, len(params.Nodes)),
	}

	for i, node := range params.Nodes {
		kind := node.Kind
		if kind == "" {
			kind = node.Tool
		}
		progress.Subcalls[i] = BatchSubcallProgress{
			ID:    node.ID,
			Name:  kind,
			State: BatchSubcallRunning,
		}
	}

	b.mu.Lock()
	registry := b.registry
	b.mu.Unlock()

	var progressMu sync.Mutex
	publishProgress := func() {
		progressMu.Lock()
		pCopy := progress
		pCopy.Subcalls = append([]BatchSubcallProgress(nil), progress.Subcalls...)
		progressMu.Unlock()
		BatchProgressBroker.Publish(pubsub.UpdatedEvent, pCopy)
	}

	// 首次发布进度
	publishProgress()

	g, gCtx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, maxParallel)
	results := make([]BatchNodeResult, len(params.Nodes))

	for i := range params.Nodes {
		i := i
		node := params.Nodes[i]
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			// 检查取消
			if gCtx.Err() != nil {
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "skipped",
					Error:  "cancelled",
				}
				progressMu.Lock()
				progress.Subcalls[i].State = BatchSubcallFailed
				progress.Completed++
				progressMu.Unlock()
				publishProgress()
				return nil
			}

			// 自愈及参数转换
			targetTool, inputJSON, err := b.normalizeBatchNode(node)
			if err != nil {
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "failed",
					Error:  fmt.Sprintf("failed to normalize node: %v", err),
				}
				progressMu.Lock()
				progress.Subcalls[i].State = BatchSubcallFailed
				progress.Completed++
				progressMu.Unlock()
				publishProgress()
				return nil
			}

			// 防止递归死循环调用
			if targetTool == EvidenceBatchToolName || targetTool == "Agent" {
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "failed",
					Error:  fmt.Sprintf("nested tool %s is blocked in Batch", targetTool),
				}
				progressMu.Lock()
				progress.Subcalls[i].State = BatchSubcallFailed
				progress.Completed++
				progressMu.Unlock()
				publishProgress()
				return nil
			}

			// 在注册表中寻找目标工具
			var actualTool fantasy.AgentTool
			if registry != nil {
				actualTool = registry[targetTool]
				// 兜底不区分大小写匹配
				if actualTool == nil {
					for name, t := range registry {
						if strings.EqualFold(name, targetTool) {
							actualTool = t
							break
						}
					}
				}
			}

			if actualTool == nil {
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "failed",
					Error:  fmt.Sprintf("tool %s not registered", targetTool),
				}
				progressMu.Lock()
				progress.Subcalls[i].State = BatchSubcallFailed
				progress.Completed++
				progressMu.Unlock()
				publishProgress()
				return nil
			}

			// 执行子工具
			slog.Debug("Batch executing node", "id", node.ID, "tool", targetTool)
			subCall := fantasy.ToolCall{
				ID:    fmt.Sprintf("%s-%s", call.ID, node.ID),
				Name:  targetTool,
				Input: inputJSON,
			}

			resp, runErr := actualTool.Run(gCtx, subCall)

			progressMu.Lock()
			if runErr != nil {
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "failed",
					Error:  runErr.Error(),
					Kind:   targetTool,
				}
				progress.Subcalls[i].State = BatchSubcallFailed
			} else if resp.IsError {
				content := resp.Content
				if len(content) > byteLimitPerNode {
					content = content[:byteLimitPerNode] + "\n[Content truncated by Batch output budget]"
				}
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "failed",
					Error:  content,
					Kind:   targetTool,
				}
				progress.Subcalls[i].State = BatchSubcallFailed
			} else {
				content := resp.Content
				if len(content) > byteLimitPerNode {
					content = content[:byteLimitPerNode] + "\n[Content truncated by Batch output budget]"
				}
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "completed",
					Output: content,
					Kind:   targetTool,
				}
				progress.Subcalls[i].State = BatchSubcallSucceeded
			}

			progress.Completed++
			progressMu.Unlock()

			publishProgress()
			return nil
		})
	}

	_ = g.Wait()

	// 最终汇总
	summary := BatchSummary{
		Total: len(params.Nodes),
	}
	var evidenceParts []string
	for _, res := range results {
		if res.Status == "completed" {
			summary.Completed++
			evidenceParts = append(evidenceParts, fmt.Sprintf("[evidence] node: %s status: %s\n%s", res.ID, res.Status, res.Output))
		} else {
			summary.Failed++
			evidenceParts = append(evidenceParts, fmt.Sprintf("[evidence] node: %s status: %s error: %s", res.ID, res.Status, res.Error))
		}
	}

	respData := BatchResponse{
		Nodes:   results,
		Summary: summary,
	}
	metadataBytes, _ := json.Marshal(respData)

	content := fmt.Sprintf("[summary] total: %d completed: %d failed: %d\n\n%s",
		summary.Total, summary.Completed, summary.Failed, strings.Join(evidenceParts, "\n\n"))

	return fantasy.ToolResponse{
		Content:  content,
		Metadata: string(metadataBytes),
	}, nil
}

func (b *EvidenceBatchTool) normalizeBatchNode(node BatchNode) (string, string, error) {
	inputs := healLLMInput(node)

	kind := strings.ToLower(strings.TrimSpace(node.Kind))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(node.Tool))
	}

	var targetTool string
	var params map[string]any

	switch kind {
	case "bash", "run_short_command", "run_command":
		targetTool = "bash"
		cmd := node.Command
		if cmd == "" {
			if v, ok := inputs["command"]; ok {
				cmd, _ = v.(string)
			} else if v, ok := inputs["script"]; ok {
				cmd, _ = v.(string)
			}
		}
		params = map[string]any{
			"command": cmd,
		}

	case "rg", "grep", "search", "search_text", "search_files", "find", "files", "glob", "file_search":
		targetTool = "Search"
		mode := "content"
		if kind == "search_files" || kind == "find" || kind == "files" || kind == "glob" || kind == "file_search" || node.FilesOnly {
			mode = "files"
		}
		if v, ok := inputs["mode"]; ok {
			if m, ok := v.(string); ok {
				mode = m
			}
		}

		pattern := node.Query
		if pattern == "" {
			pattern = node.Pattern
		}
		if pattern == "" {
			pattern = node.Include
		}
		if pattern == "" {
			if v, ok := inputs["pattern"]; ok {
				pattern, _ = v.(string)
			} else if v, ok := inputs["query"]; ok {
				pattern, _ = v.(string)
			} else if v, ok := inputs["include"]; ok {
				pattern, _ = v.(string)
			}
		}

		path := node.Path
		if path == "" {
			if v, ok := inputs["path"]; ok {
				path, _ = v.(string)
			}
		}

		ignoreCase := false
		if v, ok := inputs["ignore_case"]; ok {
			if bVal, ok := v.(bool); ok {
				ignoreCase = bVal
			}
		}
		literal := node.LiteralText
		if v, ok := inputs["literal"]; ok {
			if bVal, ok := v.(bool); ok {
				literal = bVal
			}
		}

		params = map[string]any{
			"mode":        mode,
			"pattern":     pattern,
			"path":        path,
			"ignore_case": ignoreCase,
			"literal":     literal,
		}
		if mode == "content" {
			if node.Include != "" {
				params["include"] = node.Include
			} else if v, ok := inputs["include"]; ok {
				params["include"] = v
			}
			if node.FilesOnly {
				params["files_only"] = true
			} else if v, ok := inputs["files_only"]; ok {
				params["files_only"] = v
			}
		}

	case "read_file", "view", "read":
		targetTool = "Read"
		filePath := node.Path
		if filePath == "" {
			if v, ok := inputs["file_path"]; ok {
				filePath, _ = v.(string)
			} else if v, ok := inputs["path"]; ok {
				filePath, _ = v.(string)
			}
		}
		limit := node.Limit
		if limit == 0 {
			if v, ok := inputs["limit"]; ok {
				if f, ok := v.(float64); ok {
					limit = int(f)
				} else if i, ok := v.(int); ok {
					limit = i
				}
			}
		}
		offset := 0
		if v, ok := inputs["offset"]; ok {
			if f, ok := v.(float64); ok {
				offset = int(f)
			} else if i, ok := v.(int); ok {
				offset = i
			}
		}
		fold := false
		if v, ok := inputs["fold"]; ok {
			fold, _ = v.(bool)
		}

		params = map[string]any{
			"file_path": filePath,
			"limit":     limit,
			"offset":    offset,
			"fold":      fold,
		}

	case "list_tree", "readdir", "ls", "tree":
		targetTool = "ReadDir"
		path := node.Path
		if path == "" {
			if v, ok := inputs["path"]; ok {
				path, _ = v.(string)
			}
		}
		depth := node.Depth
		if depth == 0 {
			if v, ok := inputs["depth"]; ok {
				if f, ok := v.(float64); ok {
					depth = int(f)
				} else if i, ok := v.(int); ok {
					depth = i
				}
			}
		}
		var ignore []string
		if v, ok := inputs["ignore"]; ok {
			if arr, ok := v.([]any); ok {
				for _, a := range arr {
					if s, ok := a.(string); ok {
						ignore = append(ignore, s)
					}
				}
			}
		}

		params = map[string]any{
			"path":  path,
			"depth": depth,
		}
		if len(ignore) > 0 {
			params["ignore"] = ignore
		}

	default:
		targetTool = node.Kind
		if targetTool == "" {
			targetTool = node.Tool
		}
		params = inputs
	}

	inputBytes, err := json.Marshal(params)
	if err != nil {
		return "", "", err
	}
	return targetTool, string(inputBytes), nil
}

func healLLMInput(node BatchNode) map[string]any {
	result := make(map[string]any)

	if node.Command != "" {
		result["command"] = node.Command
	}
	if node.Query != "" {
		result["query"] = node.Query
	}
	if node.Pattern != "" {
		result["pattern"] = node.Pattern
	}
	if node.Path != "" {
		result["path"] = node.Path
	}
	if node.Limit != 0 {
		result["limit"] = node.Limit
	}
	if node.Depth != 0 {
		result["depth"] = node.Depth
	}

	mergeMap := func(m map[string]any) {
		for k, v := range m {
			result[k] = v
		}
	}
	mergeMap(node.Args)
	mergeMap(node.Arguments)
	mergeMap(node.Parameters)

	return result
}
