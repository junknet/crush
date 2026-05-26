package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/stretchr/testify/require"
)

type tracedToolInput struct {
	Value string `json:"value"`
}

func TestTracedToolRecordsLifecycle(t *testing.T) {
	t.Parallel()

	runtime := agentruntime.NewSession("/tmp/project", nil)
	runtime.BindSession("conversation-1")
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "session-1")
	ctx = tools.WithTraceContext(ctx, runtime, "node-1", "parent-1", "brain_agent", "provider-1", "provider-type", "model-1")

	inner := fantasy.NewAgentTool("fake_tool", "Fake tool.", func(context.Context, tracedToolInput, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("tool output"), nil
	})
	wrapped := newTracedTool(inner)
	resp, err := wrapped.Run(ctx, fantasy.ToolCall{
		ID:    "call-1",
		Name:  "fake_tool",
		Input: `{"value":"x"}`,
	})

	require.NoError(t, err)
	require.Equal(t, "tool output", resp.Content)
	traces := runtime.TraceEntries()
	require.Len(t, traces, 2)
	require.Equal(t, agentruntime.TraceKindToolStarted, traces[0].Kind)
	require.Equal(t, agentruntime.TraceKindToolFinished, traces[1].Kind)
	require.Equal(t, "fake_tool", traces[1].ToolName)
	require.Equal(t, "call-1", traces[1].ToolCallID)
	require.Equal(t, len(`{"value":"x"}`), traces[1].ToolInputBytes)
	require.Equal(t, len("tool output"), traces[1].ToolOutputBytes)
	require.True(t, traces[1].Success)
	require.Equal(t, "conversation-1", traces[1].ConversationSessionID)
	require.Equal(t, "node-1", traces[1].NodeID)
}
