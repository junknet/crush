package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/shell"
	"golang.org/x/sync/errgroup"
)

type DagRunParams struct {
	Nodes          []DagRunNode `json:"nodes" description:"DAG nodes to execute"`
	MaxParallel    int          `json:"max_parallel,omitempty" description:"Maximum concurrent ready nodes, default 4, max 16"`
	TimeoutSeconds int          `json:"timeout_seconds,omitempty" description:"Whole-DAG timeout in seconds, default 120, max 600"`
}

type DagRunNode struct {
	ID          string   `json:"id" description:"Unique node ID used by dependencies and output interpolation"`
	Tool        string   `json:"tool" description:"Node tool: fd, rg, view, run, or shell"`
	DependsOn   []string `json:"depends_on,omitempty" description:"Node IDs that must complete before this node runs"`
	Pattern     string   `json:"pattern,omitempty" description:"fd/rg pattern"`
	Path        string   `json:"path,omitempty" description:"fd/rg search path"`
	Include     string   `json:"include,omitempty" description:"rg include glob"`
	LiteralText bool     `json:"literal_text,omitempty" description:"rg literal search"`
	FilePath    string   `json:"file_path,omitempty" description:"view file path"`
	Offset      int      `json:"offset,omitempty" description:"view line offset"`
	Limit       int      `json:"limit,omitempty" description:"view line limit"`
	Fold        bool     `json:"fold,omitempty" description:"view folded semantic read"`
	Language    string   `json:"language,omitempty" description:"run language: shell, python, node"`
	Script      string   `json:"script,omitempty" description:"run script"`
	Command     string   `json:"command,omitempty" description:"shell command"`
}

type DagRunResponse struct {
	DurationMs  int64               `json:"duration_ms"`
	MaxParallel int                 `json:"max_parallel"`
	Nodes       []DagRunNodeResult  `json:"nodes"`
	Summary     DagRunResultSummary `json:"summary"`
}

type DagRunNodeResult struct {
	ID         string `json:"id"`
	Tool       string `json:"tool"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

type DagRunResultSummary struct {
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

const (
	DagRunToolName            = "dag_run"
	dagRunDefaultMaxParallel  = 4
	dagRunMaxParallel         = 16
	dagRunDefaultTimeout      = 120 * time.Second
	dagRunMaxTimeout          = 600 * time.Second
	dagRunNodeOutputMaxBytes  = 4000
	dagRunTotalOutputMaxBytes = 24000
)

//go:embed dag_run.md
var dagRunDescription string

var dagRunInterpolationPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_.-]+)\.output\}`)

func NewDagRunTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		DagRunToolName,
		dagRunDescription,
		func(ctx context.Context, params DagRunParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if len(params.Nodes) == 0 {
				return fantasy.NewTextErrorResponse("nodes is required"), nil
			}
			if err := validateDagRunNodes(params.Nodes); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing dag_run")
			}
			granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        workingDir,
				ToolCallID:  call.ID,
				ToolName:    DagRunToolName,
				Action:      "execute",
				Description: fmt.Sprintf("Execute DAG with %d nodes", len(params.Nodes)),
				Params:      params,
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !granted {
				return NewPermissionDeniedResponse(), nil
			}

			timeout := dagRunTimeout(params.TimeoutSeconds)
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			startedAt := time.Now()
			results := executeDagRun(runCtx, workingDir, params.Nodes, dagRunMaxParallelValue(params.MaxParallel))
			response := buildDagRunResponse(startedAt, dagRunMaxParallelValue(params.MaxParallel), results)
			body, err := json.MarshalIndent(response, "", "  ")
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("marshal dag_run response: %w", err)
			}
			return fantasy.NewTextResponse(string(body)), nil
		},
	)
}

func validateDagRunNodes(nodes []DagRunNode) error {
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			return fmt.Errorf("dag_run node id is required")
		}
		if _, ok := seen[node.ID]; ok {
			return fmt.Errorf("duplicate dag_run node id: %s", node.ID)
		}
		seen[node.ID] = struct{}{}
		switch node.Tool {
		case "fd":
			if node.Pattern == "" {
				return fmt.Errorf("node %s: pattern is required for fd", node.ID)
			}
		case "rg":
			if node.Pattern == "" {
				return fmt.Errorf("node %s: pattern is required for rg", node.ID)
			}
		case "view":
			if node.FilePath == "" {
				return fmt.Errorf("node %s: file_path is required for view", node.ID)
			}
		case "run":
			if strings.TrimSpace(node.Script) == "" {
				return fmt.Errorf("node %s: script is required for run", node.ID)
			}
			if message, blocked := blockForegroundSleep(node.Script); blocked {
				return fmt.Errorf("node %s: %s", node.ID, message)
			}
		case "shell":
			if strings.TrimSpace(node.Command) == "" {
				return fmt.Errorf("node %s: command is required for shell", node.ID)
			}
			if message, blocked := blockForegroundSleep(node.Command); blocked {
				return fmt.Errorf("node %s: %s", node.ID, message)
			}
		default:
			return fmt.Errorf("node %s: unsupported tool %q", node.ID, node.Tool)
		}
	}
	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			if _, ok := seen[dep]; !ok {
				return fmt.Errorf("node %s depends on unknown node %s", node.ID, dep)
			}
			if dep == node.ID {
				return fmt.Errorf("node %s cannot depend on itself", node.ID)
			}
		}
	}
	if hasDagCycle(nodes) {
		return fmt.Errorf("dag_run contains a dependency cycle")
	}
	return nil
}

func hasDagCycle(nodes []DagRunNode) bool {
	byID := make(map[string]DagRunNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	visiting := make(map[string]bool, len(nodes))
	visited := make(map[string]bool, len(nodes))
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dep := range byID[id].DependsOn {
			if visit(dep) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	for _, node := range nodes {
		if visit(node.ID) {
			return true
		}
	}
	return false
}

func dagRunMaxParallelValue(value int) int {
	if value <= 0 {
		return dagRunDefaultMaxParallel
	}
	if value > dagRunMaxParallel {
		return dagRunMaxParallel
	}
	return value
}

func dagRunTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return dagRunDefaultTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > dagRunMaxTimeout {
		return dagRunMaxTimeout
	}
	return timeout
}

func executeDagRun(ctx context.Context, workingDir string, nodes []DagRunNode, maxParallel int) map[string]DagRunNodeResult {
	remaining := make(map[string]DagRunNode, len(nodes))
	for _, node := range nodes {
		remaining[node.ID] = node
	}
	results := make(map[string]DagRunNodeResult, len(nodes))
	var resultsMu sync.Mutex
	sem := make(chan struct{}, maxParallel)

	for len(remaining) > 0 {
		ready := readyDagRunNodes(remaining, results)
		if len(ready) == 0 {
			for id, node := range remaining {
				results[id] = DagRunNodeResult{ID: id, Tool: node.Tool, Status: "skipped", Error: "dependency failed or no node can run"}
				delete(remaining, id)
			}
			break
		}

		group, groupCtx := errgroup.WithContext(ctx)
		for _, node := range ready {
			node := node
			delete(remaining, node.ID)
			group.Go(func() error {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-groupCtx.Done():
					resultsMu.Lock()
					results[node.ID] = DagRunNodeResult{ID: node.ID, Tool: node.Tool, Status: "skipped", Error: groupCtx.Err().Error()}
					resultsMu.Unlock()
					return nil
				}
				resultsMu.Lock()
				dependencyOutputs := dagRunDependencyOutputs(node, results)
				resultsMu.Unlock()
				result := executeDagRunNode(groupCtx, workingDir, node, dependencyOutputs)
				resultsMu.Lock()
				results[node.ID] = result
				resultsMu.Unlock()
				return nil
			})
		}
		_ = group.Wait()
	}
	return results
}

func readyDagRunNodes(remaining map[string]DagRunNode, results map[string]DagRunNodeResult) []DagRunNode {
	ready := make([]DagRunNode, 0)
	for _, node := range remaining {
		blocked := false
		for _, dep := range node.DependsOn {
			result, ok := results[dep]
			if !ok {
				blocked = true
				break
			}
			if result.Status != "completed" {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, node)
		}
	}
	slices.SortFunc(ready, func(left, right DagRunNode) int {
		return strings.Compare(left.ID, right.ID)
	})
	return ready
}

func dagRunDependencyOutputs(node DagRunNode, results map[string]DagRunNodeResult) map[string]string {
	outputs := make(map[string]string, len(node.DependsOn))
	for _, dep := range node.DependsOn {
		if result, ok := results[dep]; ok {
			outputs[dep] = result.Output
		}
	}
	return outputs
}

func executeDagRunNode(ctx context.Context, workingDir string, node DagRunNode, dependencyOutputs map[string]string) DagRunNodeResult {
	startedAt := time.Now()
	result := DagRunNodeResult{ID: node.ID, Tool: node.Tool, Status: "completed"}
	output, err := executeDagRunNodeOutput(ctx, workingDir, interpolateDagRunNode(node, dependencyOutputs))
	result.DurationMs = time.Since(startedAt).Milliseconds()
	result.Output, result.Truncated = truncateDagRunOutput(output, dagRunNodeOutputMaxBytes)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	return result
}

func executeDagRunNodeOutput(ctx context.Context, workingDir string, node DagRunNode) (string, error) {
	switch node.Tool {
	case "fd":
		searchPath := filepathext.SmartJoin(workingDir, node.Path)
		files, truncated, err := runFdSearch(ctx, node.Pattern, searchPath, 100)
		if err != nil {
			return "", err
		}
		output := strings.Join(files, "\n")
		if output == "" {
			output = "No files found"
		}
		if truncated {
			output += "\n(Results are truncated.)"
		}
		return output, nil
	case "rg":
		searchPattern := node.Pattern
		if node.LiteralText {
			searchPattern = escapeRegexPattern(searchPattern)
		}
		searchPath := filepathext.SmartJoin(workingDir, node.Path)
		matches, truncated, err := RgSearch(ctx, searchPattern, searchPath, node.Include, 100)
		if err != nil {
			return "", err
		}
		return formatDagRunRgMatches(matches, truncated), nil
	case "view":
		filePath, err := resolveDagRunWorkspacePath(workingDir, node.FilePath)
		if err != nil {
			return "", err
		}
		limit := node.Limit
		if limit <= 0 {
			limit = 200
		}
		content, _, hasMore, err := readTextFile(ctx, filePath, node.Offset, limit, MaxViewSize, node.Fold)
		if err != nil {
			return "", err
		}
		if hasMore {
			content += "\n(File has more lines.)"
		}
		return content, nil
	case "run":
		language := strings.ToLower(strings.TrimSpace(node.Language))
		if language == "" {
			language = "shell"
		}
		stdout, stderr, err := executeRunScript(ctx, workingDir, language, node.Script)
		output := formatOutput(stdout, stderr, err)
		return output, err
	case "shell":
		var stdout, stderr strings.Builder
		err := shell.Run(ctx, shell.RunOptions{
			Command:    node.Command,
			Cwd:        workingDir,
			Env:        os.Environ(),
			Stdout:     &stdout,
			Stderr:     &stderr,
			BlockFuncs: blockFuncs(),
		})
		output := formatOutput(stdout.String(), stderr.String(), err)
		return output, err
	default:
		return "", fmt.Errorf("unsupported tool %q", node.Tool)
	}
}

func interpolateDagRunNode(node DagRunNode, outputs map[string]string) DagRunNode {
	interpolate := func(value string) string {
		if value == "" {
			return value
		}
		return dagRunInterpolationPattern.ReplaceAllStringFunc(value, func(match string) string {
			parts := dagRunInterpolationPattern.FindStringSubmatch(match)
			if len(parts) != 2 {
				return match
			}
			if output, ok := outputs[parts[1]]; ok {
				return output
			}
			return ""
		})
	}
	node.Pattern = interpolate(node.Pattern)
	node.Path = interpolate(node.Path)
	node.Include = interpolate(node.Include)
	node.FilePath = interpolate(node.FilePath)
	node.Language = interpolate(node.Language)
	node.Script = interpolate(node.Script)
	node.Command = interpolate(node.Command)
	return node
}

func resolveDagRunWorkspacePath(workingDir, target string) (string, error) {
	filePath := filepathext.SmartJoin(workingDir, target)
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return "", err
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absWorkingDir, absFilePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("dag_run view cannot read outside working directory: %s", target)
	}
	return absFilePath, nil
}

func formatDagRunRgMatches(matches []RgMatch, truncated bool) string {
	if len(matches) == 0 {
		return "No files found"
	}
	var out strings.Builder
	for _, match := range matches {
		fmt.Fprintf(&out, "%s:%d:%d:%s\n", filepath.ToSlash(match.Path), match.LineNum, match.CharNum, strings.TrimSpace(match.LineText))
	}
	if truncated {
		out.WriteString("(Results are truncated.)\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func buildDagRunResponse(startedAt time.Time, maxParallel int, results map[string]DagRunNodeResult) DagRunResponse {
	ids := make([]string, 0, len(results))
	for id := range results {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	response := DagRunResponse{
		DurationMs:  time.Since(startedAt).Milliseconds(),
		MaxParallel: maxParallel,
		Nodes:       make([]DagRunNodeResult, 0, len(ids)),
	}
	totalBytes := 0
	for _, id := range ids {
		result := results[id]
		switch result.Status {
		case "completed":
			response.Summary.Completed++
		case "failed":
			response.Summary.Failed++
		case "skipped":
			response.Summary.Skipped++
		}
		if result.Output != "" {
			remaining := dagRunTotalOutputMaxBytes - totalBytes
			if remaining <= 0 {
				result.Output = ""
				result.Truncated = true
			} else {
				output, truncated := truncateDagRunOutput(result.Output, remaining)
				result.Output = output
				result.Truncated = result.Truncated || truncated
				totalBytes += len(output)
			}
		}
		response.Nodes = append(response.Nodes, result)
	}
	return response
}

func truncateDagRunOutput(output string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", output != ""
	}
	if len(output) <= maxBytes {
		return output, false
	}
	return output[:maxBytes] + "\n<truncated>", true
}
