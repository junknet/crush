package tools

import (
	"context"
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestRunToolExecutesShellScript(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewRunTool(&mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runRunTool(t, tool, ctx, RunParams{
		Language: "shell",
		Script:   "printf 'alpha\\n' | wc -l",
	})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "1")
}

func TestRunToolBlocksForegroundSleepPolling(t *testing.T) {
	workingDir := t.TempDir()
	tool := NewRunTool(&mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runRunTool(t, tool, ctx, RunParams{
		Language: "shell",
		Script:   "sleep 2 && echo done",
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "foreground sleep polling is blocked")
}

func runRunTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params RunParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  RunToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}
