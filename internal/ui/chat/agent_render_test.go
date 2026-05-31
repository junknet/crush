package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestAgentToolRendererCollapsesNestedToolsByDefault(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := NewAgentToolMessageItem(&sty, message.ToolCall{
		ID:       "agent-1",
		Name:     agent.AgentToolName,
		Input:    `{"role":"explore","prompt":"inspect the repo"}`,
		Finished: false,
	}, nil, false)
	item.AddNestedTool(NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID:       "tool-1",
		Name:     tools.SearchToolName,
		Input:    `{"pattern":"TODO"}`,
		Finished: true,
	}, &message.ToolResult{ToolCallID: "tool-1", Content: "TODO found"}, false))
	item.AddNestedTool(NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID:       "tool-2",
		Name:     tools.LSToolName,
		Input:    `{"path":"."}`,
		Finished: true,
	}, &message.ToolResult{ToolCallID: "tool-2", Content: "internal\nREADME.md"}, false))

	collapsed := ansi.Strip(item.RawRender(120))
	require.Contains(t, collapsed, "+2 tool uses (ctrl+o to expand)")
	require.NotContains(t, collapsed, "TODO found")
	require.NotContains(t, collapsed, "internal")

	require.True(t, item.ToggleExpanded())
	expanded := ansi.Strip(item.RawRender(120))
	require.Contains(t, expanded, "Search")
	require.Contains(t, expanded, "ReadDir")
	require.NotContains(t, expanded, "+2 tool uses (ctrl+o to expand)")
}
