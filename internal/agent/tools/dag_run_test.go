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
			{ID: "seed", Kind: "run_short_command", Command: "printf alpha"},
			{ID: "combine", Kind: "run_short_command", DependsOn: []string{"seed"}, Command: "printf '${seed.output}-beta'"},
		},
	})

	require.False(t, resp.IsError)
	parsed := responseMetadata(t, resp)
	require.Equal(t, 2, parsed.Summary.Completed)
	require.Equal(t, "completed", findDagRunResult(t, parsed, "combine").Status)
	require.Equal(t, "alpha-beta", findDagRunResult(t, parsed, "combine").Output)
	require.Contains(t, resp.Content, "[evidence]")
	require.Contains(t, resp.Content, "[summary]")
}

func TestDagRunToolRunsReadSearchNodes(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "alpha.txt"), []byte("needle\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(workingDir, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "pkg", "beta.txt"), []byte("beta\n"), 0o644))
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		Nodes: []DagRunNode{
			{ID: "files", Kind: "search_files", Query: "alpha"},
			{ID: "hits", Kind: "search_text", Query: "needle", LiteralText: true},
			{ID: "read", Kind: "read_file", Path: "alpha.txt", Limit: 10},
			{ID: "tree", Kind: "list_tree", Path: ".", Depth: 2},
		},
	})

	require.False(t, resp.IsError)
	parsed := responseMetadata(t, resp)
	require.Equal(t, 4, parsed.Summary.Completed)
	require.Contains(t, findDagRunResult(t, parsed, "files").Output, "alpha.txt")
	require.Contains(t, findDagRunResult(t, parsed, "hits").Output, "needle")
	require.Contains(t, findDagRunResult(t, parsed, "read").Output, "needle")
	require.Contains(t, findDagRunResult(t, parsed, "tree").Output, "beta.txt")
}

func TestDagRunToolBlocksForegroundSleepPolling(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		Nodes: []DagRunNode{
			{ID: "bad", Kind: "run_short_command", Command: "sleep 2 && echo done"},
		},
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "foreground sleep polling is blocked")
}

func TestDagRunToolMapsLegacyNodeTools(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "alpha.txt"), []byte("needle\n"), 0o644))
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		Nodes: []DagRunNode{{ID: "hits", Tool: "rg", Pattern: "needle"}},
	})

	require.False(t, resp.IsError)
	require.Equal(t, "search_text", responseMetadata(t, resp).Nodes[0].Kind)
}

func TestEvidenceBatchIgnoresDependencies(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewEvidenceBatchTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  EvidenceBatchToolName,
		Input: `{"nodes":[{"id":"one","kind":"run_short_command","command":"printf one"},{"id":"two","kind":"run_short_command","command":"printf two","depends_on":["missing"]}]}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, 2, responseMetadata(t, resp).Summary.Completed)
}

func TestDagRunOnFailureContinueAllowsDependentNode(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		Nodes: []DagRunNode{
			{ID: "expected_failure", Kind: "run_short_command", Command: "exit 7", OnFailure: "continue"},
			{ID: "after", Kind: "run_short_command", DependsOn: []string{"expected_failure"}, Command: "printf after"},
		},
	})

	require.False(t, resp.IsError)
	parsed := responseMetadata(t, resp)
	require.Equal(t, "failed", findDagRunResult(t, parsed, "expected_failure").Status)
	require.Equal(t, "completed", findDagRunResult(t, parsed, "after").Status)
}

func TestDagRunOnFailureSkipDependentsSkipsDependentNode(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewDagRunTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runDagRunTool(t, tool, ctx, DagRunParams{
		Nodes: []DagRunNode{
			{ID: "expected_failure", Kind: "run_short_command", Command: "exit 7", OnFailure: "skip_dependents"},
			{ID: "after", Kind: "run_short_command", DependsOn: []string{"expected_failure"}, Command: "printf after"},
		},
	})

	require.False(t, resp.IsError)
	parsed := responseMetadata(t, resp)
	require.Equal(t, "failed", findDagRunResult(t, parsed, "expected_failure").Status)
	require.Equal(t, "skipped", findDagRunResult(t, parsed, "after").Status)
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

func responseMetadata(t *testing.T, resp fantasy.ToolResponse) DagRunResponse {
	t.Helper()
	var parsed DagRunResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &parsed))
	return parsed
}
