package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestDagRunToolExecutesDependencyGraph(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		MaxParallel: 2,
		Nodes: []DagRunNode{
			{ID: "seed", Tool: "shell", Command: "printf alpha"},
			{ID: "combine", Tool: "shell", DependsOn: []string{"seed"}, Command: "printf '${seed.output}-beta'"},
		},
	})

	require.False(t, resp.IsError)
	var parsed DagRunResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &parsed))
	require.Equal(t, 2, parsed.Summary.Completed)
	require.Equal(t, "completed", findDagRunResult(t, parsed, "combine").Status)
	require.Equal(t, "alpha-beta", findDagRunResult(t, parsed, "combine").Output)
}

func TestDagRunToolRunsReadSearchNodes(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "alpha.txt"), []byte("needle\n"), 0o644))
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		Nodes: []DagRunNode{
			{ID: "files", Tool: "rg", Pattern: "alpha", FilesOnly: true},
			{ID: "hits", Tool: "rg", Pattern: "needle", LiteralText: true},
			{ID: "read", Tool: "view", FilePath: "alpha.txt", Limit: 10},
		},
	})

	require.False(t, resp.IsError)
	var parsed DagRunResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &parsed))
	require.Equal(t, 3, parsed.Summary.Completed)
	require.Contains(t, findDagRunResult(t, parsed, "files").Output, "alpha.txt")
	require.Contains(t, findDagRunResult(t, parsed, "hits").Output, "needle")
	require.Contains(t, findDagRunResult(t, parsed, "read").Output, "needle")
}

func TestDagRunToolBlocksForegroundSleepPolling(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		Nodes: []DagRunNode{
			{ID: "bad", Tool: "shell", Command: "sleep 2 && echo done"},
		},
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "foreground sleep polling is blocked")
}

func runDagRunTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params DagRunParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  DagRunToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}

func findDagRunResult(t *testing.T, response DagRunResponse, id string) DagRunNodeResult {
	t.Helper()
	for _, result := range response.Nodes {
		if result.ID == id {
			return result
		}
	}
	t.Fatalf("dag_run result %s not found", id)
	return DagRunNodeResult{}
}
