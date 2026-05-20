package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestCommandDAGTool_GrepNoMatchIsSemanticSuccess(t *testing.T) {
	workingDir := t.TempDir()
	targetFile := filepath.Join(workingDir, "sample.txt")
	require.NoError(t, os.WriteFile(targetFile, []byte("alpha\nbeta\n"), 0o644))

	runtime := agentruntime.NewSession(workingDir, nil)
	ctx := WithTraceContext(context.Background(), runtime, "node-1", "root", "tools_agent", "waitai", "anthropic", "claude-haiku")
	ctx = context.WithValue(ctx, SessionIDContextKey, "session-1")

	resp := runCommandDAGTool(t, NewCommandDAGTool(workingDir), ctx, CommandDAGParams{
		Description: "grep no match",
		Commands: []CommandDAGNode{
			{ID: "grep-missing", Command: "grep -n zzz " + targetFile},
		},
	})

	require.False(t, resp.IsError)
	var metadata CommandDAGResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &metadata))
	require.True(t, metadata.Success)
	require.Len(t, metadata.Results, 1)
	require.Equal(t, "no_match", metadata.Results[0].Outcome)
	require.True(t, metadata.Results[0].Success)
	require.NotNil(t, metadata.Results[0].ExitCode)
	require.Equal(t, 1, *metadata.Results[0].ExitCode)

	traces := runtime.TraceEntries()
	require.NotEmpty(t, traces)
	require.Contains(t, traceOutcomes(traces), "no_match")
}

func TestCommandDAGTool_SkipsDependentNodeAfterFailure(t *testing.T) {
	workingDir := t.TempDir()

	resp := runCommandDAGTool(t, NewCommandDAGTool(workingDir), context.Background(), CommandDAGParams{
		Description: "failure skip",
		Commands: []CommandDAGNode{
			{ID: "fail", Command: "exit 2"},
			{ID: "after-fail", Command: "echo should-not-run", Deps: []string{"fail"}},
			{ID: "independent", Command: "echo independent"},
		},
		MaxParallel: 2,
	})

	require.False(t, resp.IsError)
	var metadata CommandDAGResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &metadata))
	require.False(t, metadata.Success)
	require.Len(t, metadata.Results, 3)
	require.Equal(t, "failed", metadata.Results[0].Outcome)
	require.Equal(t, "skipped_dependency_failed", metadata.Results[1].Outcome)
	require.Equal(t, "succeeded", metadata.Results[2].Outcome)
}

func runCommandDAGTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params CommandDAGParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "command-dag-call",
		Name:  CommandDAGToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}

func traceOutcomes(traces []agentruntime.TaskTrace) []string {
	outcomes := make([]string, 0, len(traces))
	for _, trace := range traces {
		if trace.Outcome != "" {
			outcomes = append(outcomes, trace.Outcome)
		}
	}
	return outcomes
}
