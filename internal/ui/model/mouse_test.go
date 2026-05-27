package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/x/ansi"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func TestChatHandleMouseDownFiltersButtons(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	msg := &message.Message{
		ID:   "msg-1",
		Role: message.Assistant,
	}
	msg.Parts = append(msg.Parts, message.TextContent{Text: "test content"})
	item := chat.NewAssistantMessageItem(ui.com.Styles, msg)
	ui.chat.SetMessages(item)
	ui.chat.list.SetSize(80, 20)
	// Force a draw to ensure layout is computed
	ui.chat.Draw(uv.NewScreenBuffer(80, 20), uv.Rect(0, 0, 80, 20))

	// Left click should be handled (MouseButton1 = 0 in ansi package, but tea.MouseLeft = 0 too)
	// Actually ansi.MouseButton1 is 0.
	handled, _ := ui.chat.HandleMouseDown(ansi.MouseButton1, 2, 0)
	require.True(t, handled, "Left click should be handled")

	// Middle click should NOT be handled
	handled, _ = ui.chat.HandleMouseDown(ansi.MouseButton2, 2, 0)
	require.False(t, handled, "Middle click should NOT be handled")

	// Right click should NOT be handled
	handled, _ = ui.chat.HandleMouseDown(ansi.MouseButton3, 2, 0)
	require.False(t, handled, "Right click should NOT be handled")
}
