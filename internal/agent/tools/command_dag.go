package tools

import (
	"context"
	_ "embed"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/shell"
	"golang.org/x/sync/errgroup"
)

const (
	CommandDAGToolName              = "command_dag"
	DefaultCommandDAGMaxParallelism = 4
)

//go:embed command_dag.md
var commandDAGDescription string

type CommandDAGParams struct {
	Description string           `json:"description" description:"A brief description of the command DAG"`
	Commands    []CommandDAGNode `json:"commands" description:"Command nodes with unique IDs and dependency edges"`
	MaxParallel int              `json:"max_parallel,omitempty" description:"Maximum number of commands to run concurrently, defaults to 4"`
}

type CommandDAGNode struct {
	ID             string            `json:"id" description:"Unique command node ID"`
	Description    string            `json:"description,omitempty" description:"Short command node description"`
	Command        string            `json:"command" description:"The shell command to execute"`
	WorkingDir     string            `json:"working_dir,omitempty" description:"Working directory, defaults to the workspace root"`
	Deps           []string          `json:"deps,omitempty" description:"Command IDs that must semantically succeed before this node runs"`
	Env            map[string]string `json:"env,omitempty" description:"Additional environment variables for this command"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" description:"Optional per-command timeout in seconds"`
}

type CommandDAGResponseMetadata struct {
	Description string             `json:"description"`
	StartedAt   int64              `json:"started_at"`
	FinishedAt  int64              `json:"finished_at"`
	DurationMs  int64              `json:"duration_ms"`
	Success     bool               `json:"success"`
	Results     []CommandDAGResult `json:"results"`
}

type CommandDAGResult struct {
	ID               string   `json:"id"`
	Description      string   `json:"description,omitempty"`
	Command          string   `json:"command"`
	WorkingDirectory string   `json:"working_directory"`
	Deps             []string `json:"deps,omitempty"`
	StartedAt        int64    `json:"started_at,omitempty"`
	FinishedAt       int64    `json:"finished_at,omitempty"`
	DurationMs       int64    `json:"duration_ms,omitempty"`
	ExitCode         *int     `json:"exit_code,omitempty"`
	Outcome          string   `json:"outcome"`
	Success          bool     `json:"success"`
	Skipped          bool     `json:"skipped,omitempty"`
	Stdout           string   `json:"stdout,omitempty"`
	Stderr           string   `json:"stderr,omitempty"`
	StdoutBytes      int      `json:"stdout_bytes"`
	StderrBytes      int      `json:"stderr_bytes"`
	Error            string   `json:"error,omitempty"`
}

func NewCommandDAGTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		CommandDAGToolName,
		commandDAGDescription,
		func(ctx context.Context, params CommandDAGParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if strings.TrimSpace(params.Description) == "" {
				return fantasy.NewTextErrorResponse("missing description"), nil
			}
			if len(params.Commands) == 0 {
				return fantasy.NewTextErrorResponse("commands must contain at least one node"), nil
			}
			if err := validateCommandDAG(params.Commands); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			startedAt := time.Now()
			appendCommandDAGToolTrace(ctx, agentruntime.TraceKindToolStarted, call.ID, params.Description, startedAt, time.Time{}, true, "")
			results, runErr := executeCommandDAG(ctx, workingDir, params, call.ID)
			finishedAt := time.Now()
			success := runErr == nil && commandDAGSucceeded(results)
			output := formatCommandDAGOutput(params.Description, results, runErr)
			metadata := CommandDAGResponseMetadata{
				Description: params.Description,
				StartedAt:   startedAt.UnixMilli(),
				FinishedAt:  finishedAt.UnixMilli(),
				DurationMs:  finishedAt.Sub(startedAt).Milliseconds(),
				Success:     success,
				Results:     results,
			}
			kind := agentruntime.TraceKindToolFinished
			if !success {
				kind = agentruntime.TraceKindToolFailed
			}
			appendCommandDAGToolTrace(ctx, kind, call.ID, params.Description, startedAt, finishedAt, success, output)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(output), metadata), nil
		},
	)
}

func validateCommandDAG(nodes []CommandDAGNode) error {
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if strings.TrimSpace(node.ID) == "" {
			return fmt.Errorf("command_dag: node id is required")
		}
		if _, ok := seen[node.ID]; ok {
			return fmt.Errorf("command_dag: duplicate node id %q", node.ID)
		}
		if strings.TrimSpace(node.Command) == "" {
			return fmt.Errorf("command_dag: command is required for node %q", node.ID)
		}
		seen[node.ID] = struct{}{}
	}
	for _, node := range nodes {
		for _, dep := range node.Deps {
			if _, ok := seen[dep]; !ok {
				return fmt.Errorf("command_dag: node %q depends on unknown node %q", node.ID, dep)
			}
		}
	}
	return nil
}

func executeCommandDAG(ctx context.Context, workingDir string, params CommandDAGParams, toolCallID string) ([]CommandDAGResult, error) {
	nodesByID := make(map[string]CommandDAGNode, len(params.Commands))
	pending := make(map[string]struct{}, len(params.Commands))
	resultsByID := make(map[string]CommandDAGResult, len(params.Commands))
	for _, node := range params.Commands {
		nodesByID[node.ID] = node
		pending[node.ID] = struct{}{}
	}

	maxParallel := params.MaxParallel
	if maxParallel <= 0 {
		maxParallel = DefaultCommandDAGMaxParallelism
	}

	for len(pending) > 0 {
		skipped := skipBlockedCommandNodes(ctx, workingDir, nodesByID, pending, resultsByID, toolCallID)
		ready := readyCommandNodes(nodesByID, pending, resultsByID)
		if len(ready) == 0 {
			if skipped > 0 {
				continue
			}
			return sortedCommandDAGResults(params.Commands, resultsByID), fmt.Errorf("command_dag: dependency cycle or unresolved dependency")
		}

		group, groupCtx := errgroup.WithContext(ctx)
		sem := make(chan struct{}, maxParallel)
		var mu sync.Mutex
		for _, node := range ready {
			node := node
			delete(pending, node.ID)
			group.Go(func() error {
				sem <- struct{}{}
				defer func() { <-sem }()
				result := executeCommandNode(groupCtx, workingDir, node, toolCallID)
				mu.Lock()
				resultsByID[node.ID] = result
				mu.Unlock()
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return sortedCommandDAGResults(params.Commands, resultsByID), err
		}
	}

	return sortedCommandDAGResults(params.Commands, resultsByID), nil
}

func skipBlockedCommandNodes(ctx context.Context, workingDir string, nodesByID map[string]CommandDAGNode, pending map[string]struct{}, resultsByID map[string]CommandDAGResult, toolCallID string) int {
	skipped := 0
	for id := range maps.Keys(pending) {
		node := nodesByID[id]
		failedDep := failedDependency(node, resultsByID)
		if failedDep == "" {
			continue
		}
		delete(pending, id)
		result := CommandDAGResult{
			ID:               node.ID,
			Description:      node.Description,
			Command:          node.Command,
			WorkingDirectory: resolveCommandWorkingDir(workingDir, node.WorkingDir),
			Deps:             append([]string(nil), node.Deps...),
			Outcome:          "skipped_dependency_failed",
			Success:          false,
			Skipped:          true,
			Error:            fmt.Sprintf("dependency %q did not succeed", failedDep),
		}
		resultsByID[id] = result
		appendCommandNodeTrace(ctx, toolCallID, result, agentruntime.TraceKindCommandSkip)
		skipped++
	}
	return skipped
}

func readyCommandNodes(nodesByID map[string]CommandDAGNode, pending map[string]struct{}, resultsByID map[string]CommandDAGResult) []CommandDAGNode {
	var ready []CommandDAGNode
	for id := range pending {
		node := nodesByID[id]
		if allDependenciesSucceeded(node, resultsByID) {
			ready = append(ready, node)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		return ready[i].ID < ready[j].ID
	})
	return ready
}

func allDependenciesSucceeded(node CommandDAGNode, resultsByID map[string]CommandDAGResult) bool {
	for _, dep := range node.Deps {
		result, ok := resultsByID[dep]
		if !ok || !result.Success {
			return false
		}
	}
	return true
}

func failedDependency(node CommandDAGNode, resultsByID map[string]CommandDAGResult) string {
	for _, dep := range node.Deps {
		result, ok := resultsByID[dep]
		if ok && !result.Success {
			return dep
		}
	}
	return ""
}

func executeCommandNode(ctx context.Context, workingDir string, node CommandDAGNode, toolCallID string) CommandDAGResult {
	execWorkingDir := resolveCommandWorkingDir(workingDir, node.WorkingDir)
	result := CommandDAGResult{
		ID:               node.ID,
		Description:      node.Description,
		Command:          node.Command,
		WorkingDirectory: execWorkingDir,
		Deps:             append([]string(nil), node.Deps...),
	}

	startedAt := time.Now()
	result.StartedAt = startedAt.UnixMilli()
	result.Outcome = "running"
	appendCommandNodeTrace(ctx, toolCallID, result, agentruntime.TraceKindCommandStart)

	commandCtx := ctx
	cancel := func() {}
	if node.TimeoutSeconds > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, time.Duration(node.TimeoutSeconds)*time.Second)
	}
	defer cancel()

	stdout, stderr, execErr := shell.NewShell(&shell.Options{
		WorkingDir: execWorkingDir,
		Env:        commandEnvironment(node.Env),
		BlockFuncs: blockFuncs(),
	}).Exec(commandCtx, node.Command)

	finishedAt := time.Now()
	result.FinishedAt = finishedAt.UnixMilli()
	result.DurationMs = finishedAt.Sub(startedAt).Milliseconds()
	result.StdoutBytes = len(stdout)
	result.StderrBytes = len(stderr)
	result.Stdout = truncateOutput(stdout)
	result.Stderr = truncateOutput(stderr)
	semantics := deriveCommandSemantics(node.Command, stdout, execErr)
	result.ExitCode = &semantics.ExitCode
	result.Outcome = string(semantics.Outcome)
	result.Success = semantics.Success
	if execErr != nil && !semantics.Success {
		result.Error = execErr.Error()
	}
	kind := agentruntime.TraceKindCommandDone
	if !result.Success {
		kind = agentruntime.TraceKindCommandFail
	}
	appendCommandNodeTrace(ctx, toolCallID, result, kind)
	return result
}

func resolveCommandWorkingDir(defaultWorkingDir, nodeWorkingDir string) string {
	if strings.TrimSpace(nodeWorkingDir) != "" {
		return nodeWorkingDir
	}
	return defaultWorkingDir
}

func commandEnvironment(extra map[string]string) []string {
	env := os.Environ()
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func sortedCommandDAGResults(nodes []CommandDAGNode, resultsByID map[string]CommandDAGResult) []CommandDAGResult {
	results := make([]CommandDAGResult, 0, len(nodes))
	for _, node := range nodes {
		if result, ok := resultsByID[node.ID]; ok {
			results = append(results, result)
		}
	}
	return results
}

func commandDAGSucceeded(results []CommandDAGResult) bool {
	for _, result := range results {
		if !result.Success {
			return false
		}
	}
	return true
}

func formatCommandDAGOutput(description string, results []CommandDAGResult, runErr error) string {
	var b strings.Builder
	successCount := 0
	for _, result := range results {
		if result.Success {
			successCount++
		}
	}
	fmt.Fprintf(&b, "Command DAG: %s\n", description)
	fmt.Fprintf(&b, "Result: %d/%d succeeded\n", successCount, len(results))
	if runErr != nil {
		fmt.Fprintf(&b, "Error: %s\n", runErr.Error())
	}
	for _, result := range results {
		exitText := "none"
		if result.ExitCode != nil {
			exitText = fmt.Sprintf("%d", *result.ExitCode)
		}
		fmt.Fprintf(&b, "\n[%s] outcome=%s success=%t exit=%s duration_ms=%d\n", result.ID, result.Outcome, result.Success, exitText, result.DurationMs)
		if result.Stdout != "" {
			fmt.Fprintf(&b, "stdout:\n%s\n", result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprintf(&b, "stderr:\n%s\n", result.Stderr)
		}
		if result.Error != "" {
			fmt.Fprintf(&b, "error: %s\n", result.Error)
		}
	}
	return strings.TrimSpace(b.String())
}

func appendCommandDAGToolTrace(ctx context.Context, kind agentruntime.TraceKind, toolCallID, description string, startedAt, finishedAt time.Time, success bool, output string) {
	durationMs := int64(0)
	if !startedAt.IsZero() && !finishedAt.IsZero() {
		durationMs = finishedAt.Sub(startedAt).Milliseconds()
	}
	AppendTraceFromContext(ctx, agentruntime.TaskTrace{
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		DurationMs:  durationMs,
		Kind:        kind,
		Status:      statusForToolTrace(kind),
		Success:     success,
		ToolName:    CommandDAGToolName,
		ToolCallID:  toolCallID,
		ToolInput:   description,
		ToolOutput:  output,
		Output:      output,
		OutputBytes: len(output),
	})
}

func appendCommandNodeTrace(ctx context.Context, toolCallID string, result CommandDAGResult, kind agentruntime.TraceKind) {
	var startedAt time.Time
	if result.StartedAt > 0 {
		startedAt = time.UnixMilli(result.StartedAt)
	}
	var finishedAt time.Time
	if result.FinishedAt > 0 {
		finishedAt = time.UnixMilli(result.FinishedAt)
	}
	output := result.Stdout
	if output == "" {
		output = result.Stderr
	}
	AppendTraceFromContext(ctx, agentruntime.TaskTrace{
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		DurationMs:  result.DurationMs,
		Kind:        kind,
		Status:      result.Outcome,
		Success:     result.Success,
		ToolName:    CommandDAGToolName,
		ToolCallID:  toolCallID,
		CommandID:   result.ID,
		Command:     result.Command,
		WorkingDir:  result.WorkingDirectory,
		ExitCode:    result.ExitCode,
		Outcome:     result.Outcome,
		StdoutBytes: result.StdoutBytes,
		StderrBytes: result.StderrBytes,
		ToolInput:   result.Command,
		ToolOutput:  output,
		Output:      output,
		OutputBytes: len(output),
		Error:       result.Error,
	})
}

func statusForToolTrace(kind agentruntime.TraceKind) string {
	switch kind {
	case agentruntime.TraceKindToolStarted:
		return "running"
	case agentruntime.TraceKindToolFailed:
		return "failed"
	default:
		return "done"
	}
}
