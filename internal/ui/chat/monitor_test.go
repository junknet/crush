package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestMonitorToolRendererSuppressesContinuationBody(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(
		&sty,
		"msg-1",
		message.ToolCall{
			ID:       "tool-1",
			Name:     tools.MonitorToolName,
			Input:    `{"shell_id":"015","pattern":"DONE|ERROR","timeout_seconds":300}`,
			Finished: true,
		},
		&message.ToolResult{
			ToolCallID: "tool-1",
			Content:    "Monitoring job 015. This turn will end now; you'll be automatically woken.",
		},
		false,
	)

	rendered := ansi.Strip(item.RawRender(120))

	require.Contains(t, rendered, "Monitor")
	require.Contains(t, rendered, "job 015")
	require.Contains(t, rendered, "pattern=DONE|ERROR")
	require.NotContains(t, rendered, "This turn will end now")
	require.NotContains(t, rendered, "automatically woken")
}
