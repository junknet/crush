package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestBatchToolRunsParallelNodesPassthrough(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "alpha.txt"), []byte("needle-alpha\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "beta.txt"), []byte("needle-beta\n"), 0o644))

	// Mock registry with Search and Read tools
	searchTool := NewSearchTool(workingDir)
	readTool := NewViewTool(nil, &mockBashPermissionService{}, mockFileTracker{}, nil, workingDir)

	registry := map[string]fantasy.AgentTool{
		"Search": searchTool,
		"Read":   readTool,
	}

	tool := NewEvidenceBatchTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir, nil)
	tool.SetRegistry(registry)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// Subscribe to progress
	progressCh := SubscribeBatchProgress(ctx)

	inputParams := BatchParams{
		Nodes: []BatchNode{
			{
				ID:    "hits-alpha",
				Tool:  "Search",
				Input: json.RawMessage(`{"mode":"content","pattern":"needle-alpha"}`),
			},
			{
				ID:    "read-beta",
				Tool:  "Read",
				Input: json.RawMessage(`{"file_path":"beta.txt"}`),
			},
		},
	}

	inputBytes, err := json.Marshal(inputParams)
	require.NoError(t, err)

	type runResult struct {
		resp fantasy.ToolResponse
		err  error
	}
	resChan := make(chan runResult, 1)

	go func() {
		resp, err := tool.Run(ctx, fantasy.ToolCall{
			ID:    "batch-call-1",
			Name:  EvidenceBatchToolName,
			Input: string(inputBytes),
		})
		resChan <- runResult{resp: resp, err: err}
	}()

	// Verify progress was received
	select {
	case ev := <-progressCh:
		require.Equal(t, "batch-call-1", ev.Payload.ToolCallID)
		require.Equal(t, 2, ev.Payload.Total)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for progress event")
	}

	var runRes runResult
	select {
	case runRes = <-resChan:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tool completion")
	}

	require.NoError(t, runRes.err)
	require.False(t, runRes.resp.IsError)
	resp := runRes.resp

	var batchResp BatchResponse
	err = json.Unmarshal([]byte(resp.Metadata), &batchResp)
	require.NoError(t, err)

	require.Equal(t, 2, batchResp.Summary.Completed)
	require.Equal(t, 0, batchResp.Summary.Failed)

	// Validate nodes
	var hitFound, readFound bool
	for _, res := range batchResp.Nodes {
		if res.ID == "hits-alpha" {
			hitFound = true
			require.Equal(t, "completed", res.Status)
			require.Contains(t, res.Output, "alpha.txt")
		} else if res.ID == "read-beta" {
			readFound = true
			require.Equal(t, "completed", res.Status)
			require.Contains(t, res.Output, "needle-beta")
		}
	}
	require.True(t, hitFound)
	require.True(t, readFound)
}

func TestBatchToolBlocksRecursion(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewEvidenceBatchTool(nil, &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir, nil)
	tool.SetRegistry(map[string]fantasy.AgentTool{
		EvidenceBatchToolName: tool,
	})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	inputParams := BatchParams{
		Nodes: []BatchNode{
			{
				ID:   "nested-batch",
				Tool: "Batch",
			},
		},
	}

	inputBytes, err := json.Marshal(inputParams)
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "batch-call-2",
		Name:  EvidenceBatchToolName,
		Input: string(inputBytes),
	})
	require.NoError(t, err)
	
	var batchResp BatchResponse
	err = json.Unmarshal([]byte(resp.Metadata), &batchResp)
	require.NoError(t, err)

	require.Equal(t, 1, batchResp.Summary.Failed)
	require.Contains(t, batchResp.Nodes[0].Error, "nested tool Batch is blocked")
}

// The model sometimes jams a node label into the "tool" field instead of "id"
// (observed in production: tool="ReadDir业务层入口及后端适配层目录树"). resolveTool
// must extract the leading identifier and still resolve it.
func TestBatchResolvesPollutedToolName(t *testing.T) {
	vt := NewViewTool(nil, &mockBashPermissionService{}, mockFileTracker{}, nil, t.TempDir())
	reg := map[string]fantasy.AgentTool{"ReadDir": vt, "Read": vt}

	if name, tool := resolveTool(reg, "ReadDir业务层入口及后端适配层目录树"); name != "ReadDir" || tool == nil {
		t.Fatalf("polluted tool name must resolve to ReadDir, got %q", name)
	}
	if name, tool := resolveTool(reg, "  read  "); name != "Read" || tool == nil {
		t.Fatalf("trimmed/case-folded name must resolve to Read, got %q", name)
	}
	if name, _ := resolveTool(reg, "Nonexistent工具"); name != "" {
		t.Fatalf("unknown tool must not match, got %q", name)
	}
}
