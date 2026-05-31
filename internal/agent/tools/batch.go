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

	// batchOutputBudget is the total byte budget shared across all node outputs,
	// divided evenly so one chatty node can't crowd out the rest of the context.
	batchOutputBudget = 50000
	batchMinNodeBytes = 500
	batchMaxParallel  = 16
	batchDefaultPar   = 4
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
//
// A node is one ordinary tool call run in parallel: name the real tool and give
// its native input verbatim. Batch transports the input untouched to the target
// tool, so there is exactly one schema per tool — Batch never re-declares or
// translates a tool's parameters. This is what makes Batch generalize to every
// registered tool (including MCP) with zero per-tool wiring.
func (b *EvidenceBatchTool) Info() fantasy.ToolInfo {
	// Parameters is intentionally left empty (no formal JSON Schema): the
	// Gemini/antigravity function-declaration validator rejects hand-written
	// nested schemas (repeated `required`, free-form object inputs), and an
	// empty schema is accepted by every provider. The node contract is carried
	// in the Description and the role templates instead — and parseBatchNode
	// tolerates both the nested {tool,input} and the flat shape, so the model
	// does not need a formal schema to produce valid nodes.
	return fantasy.ToolInfo{
		Name: EvidenceBatchToolName,
		Description: "Run multiple tools in parallel (up to 16 concurrently). " +
			"Input is {\"nodes\":[ ... ], optional \"max_parallel\":N}. " +
			"Each node is one ordinary tool call: {\"tool\":\"<exact tool name>\",\"input\":{<that tool's normal input object>}} plus an optional \"id\" label. " +
			"The input is identical to a standalone call. " +
			"Examples: {\"tool\":\"Read\",\"input\":{\"file_path\":\"main.go\"}}; " +
			"{\"tool\":\"Search\",\"input\":{\"mode\":\"content\",\"pattern\":\"foo\"}}; " +
			"{\"tool\":\"ReadDir\",\"input\":{\"path\":\"internal\"}}; " +
			"{\"tool\":\"Bash\",\"input\":{\"command\":\"go build ./...\"}}. " +
			"Use it to fan out independent reads, searches, directory listings, or short commands in a single turn.",
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

// BatchNode is one parallel tool call. Tool names the target; Input is that
// tool's native input, passed through verbatim. There are intentionally no
// per-tool typed fields here — adding them would re-create a second, divergent
// copy of every tool's schema (the historical source of dropped arguments).
type BatchNode struct {
	ID    string          `json:"id,omitempty"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`
}

type BatchParams struct {
	MaxParallel int         `json:"max_parallel,omitempty"`
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

// parseBatchNode decodes one node, tolerating the flat shape (tool params at the
// node top level) in addition to the canonical nested {tool, input} shape. This
// is structural tolerance only — no field is renamed or mapped per tool.
func parseBatchNode(raw json.RawMessage) BatchNode {
	var node BatchNode
	_ = json.Unmarshal(raw, &node)

	if !hasInput(node.Input) {
		var top map[string]json.RawMessage
		if json.Unmarshal(raw, &top) == nil {
			delete(top, "id")
			delete(top, "tool")
			if len(top) > 0 {
				if encoded, err := json.Marshal(top); err == nil {
					node.Input = encoded
				}
			}
		}
	}
	if !hasInput(node.Input) {
		node.Input = json.RawMessage("{}")
	}
	return node
}

func hasInput(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// resolveTool finds the target tool by exact name, then case-insensitively, so
// "bash" resolves to the registered "Bash" without a hand-maintained alias list.
func resolveTool(registry map[string]fantasy.AgentTool, want string) (string, fantasy.AgentTool) {
	if registry == nil {
		return "", nil
	}
	if t, ok := registry[want]; ok {
		return want, t
	}
	for name, t := range registry {
		if strings.EqualFold(name, want) {
			return name, t
		}
	}
	return "", nil
}

func (b *EvidenceBatchTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var rawParams struct {
		MaxParallel int               `json:"max_parallel"`
		Nodes       []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(call.Input), &rawParams); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("batch: failed to parse input: %v", err)), nil
	}

	if len(rawParams.Nodes) == 0 {
		respData := BatchResponse{
			Nodes:   []BatchNodeResult{},
			Summary: BatchSummary{},
		}
		metadataBytes, _ := json.Marshal(respData)
		return fantasy.ToolResponse{
			Content:  "No nodes executed.",
			Metadata: string(metadataBytes),
		}, nil
	}

	nodes := make([]BatchNode, len(rawParams.Nodes))
	for i, raw := range rawParams.Nodes {
		nodes[i] = parseBatchNode(raw)
	}

	maxParallel := rawParams.MaxParallel
	if maxParallel <= 0 {
		maxParallel = batchDefaultPar
	}
	if maxParallel > batchMaxParallel {
		maxParallel = batchMaxParallel
	}

	// Even byte budget per node so one chatty node can't crowd out the rest.
	byteLimitPerNode := batchOutputBudget / len(nodes)
	if byteLimitPerNode < batchMinNodeBytes {
		byteLimitPerNode = batchMinNodeBytes
	}

	sessionID := GetSessionFromContext(ctx)
	progress := BatchProgress{
		SessionID:  sessionID,
		ToolCallID: call.ID,
		Total:      len(nodes),
		Subcalls:   make([]BatchSubcallProgress, len(nodes)),
	}
	for i, node := range nodes {
		progress.Subcalls[i] = BatchSubcallProgress{
			ID:    node.ID,
			Name:  node.Tool,
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
	publishProgress()

	g, gCtx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, maxParallel)
	results := make([]BatchNodeResult, len(nodes))

	for i := range nodes {
		i := i
		node := nodes[i]
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			fail := func(errMsg string) {
				results[i] = BatchNodeResult{
					ID:     node.ID,
					Status: "failed",
					Error:  errMsg,
					Kind:   node.Tool,
				}
				progressMu.Lock()
				progress.Subcalls[i].State = BatchSubcallFailed
				progress.Completed++
				progressMu.Unlock()
				publishProgress()
			}

			if gCtx.Err() != nil {
				results[i] = BatchNodeResult{ID: node.ID, Status: "skipped", Error: "cancelled", Kind: node.Tool}
				progressMu.Lock()
				progress.Subcalls[i].State = BatchSubcallFailed
				progress.Completed++
				progressMu.Unlock()
				publishProgress()
				return nil
			}

			if strings.TrimSpace(node.Tool) == "" {
				fail("node is missing a \"tool\" name")
				return nil
			}

			// Block recursion: Batch cannot nest itself or spawn sub-agents.
			if strings.EqualFold(node.Tool, EvidenceBatchToolName) || strings.EqualFold(node.Tool, "Agent") {
				fail(fmt.Sprintf("nested tool %s is blocked in Batch", node.Tool))
				return nil
			}

			name, actualTool := resolveTool(registry, node.Tool)
			if actualTool == nil {
				fail(fmt.Sprintf("tool %s not registered", node.Tool))
				return nil
			}

			slog.Debug("Batch executing node", "id", node.ID, "tool", name)
			subCall := fantasy.ToolCall{
				ID:    fmt.Sprintf("%s-%s", call.ID, node.ID),
				Name:  name,
				Input: string(node.Input),
			}

			resp, runErr := actualTool.Run(gCtx, subCall)

			progressMu.Lock()
			switch {
			case runErr != nil:
				results[i] = BatchNodeResult{ID: node.ID, Status: "failed", Error: runErr.Error(), Kind: name}
				progress.Subcalls[i].State = BatchSubcallFailed
			case resp.IsError:
				results[i] = BatchNodeResult{ID: node.ID, Status: "failed", Error: clampContent(resp.Content, byteLimitPerNode), Kind: name}
				progress.Subcalls[i].State = BatchSubcallFailed
			default:
				results[i] = BatchNodeResult{ID: node.ID, Status: "completed", Output: clampContent(resp.Content, byteLimitPerNode), Kind: name}
				progress.Subcalls[i].State = BatchSubcallSucceeded
			}
			progress.Completed++
			progressMu.Unlock()

			publishProgress()
			return nil
		})
	}

	_ = g.Wait()

	summary := BatchSummary{Total: len(nodes)}
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

	respData := BatchResponse{Nodes: results, Summary: summary}
	metadataBytes, _ := json.Marshal(respData)

	content := fmt.Sprintf("[summary] total: %d completed: %d failed: %d\n\n%s",
		summary.Total, summary.Completed, summary.Failed, strings.Join(evidenceParts, "\n\n"))

	return fantasy.ToolResponse{
		Content:  content,
		Metadata: string(metadataBytes),
	}, nil
}

func clampContent(content string, limit int) string {
	if len(content) > limit {
		return content[:limit] + "\n[Content truncated by Batch output budget]"
	}
	return content
}
