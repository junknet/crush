package tools

import (
	"context"
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

func TestMonitorToolUnknownShellErrorIncludesKnownIDs(t *testing.T) {
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session-monitor-tool")
	workingDir := t.TempDir()
	bgManager := shell.NewBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "sleep 10", "", "session-monitor-tool")
	require.NoError(t, err)
	defer bgManager.Kill(bgShell.ID)

	resp := runMonitorTool(t, NewMonitorTool(bgManager), ctx, MonitorParams{
		ShellID: "missing-shell",
		Pattern: "ready",
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "background shell not found: missing-shell")
	require.Contains(t, resp.Content, "known shell IDs: "+bgShell.ID)
}

func runMonitorTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params MonitorParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-monitor-call",
		Name:  MonitorToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}
