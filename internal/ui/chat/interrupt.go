package chat

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// InterruptDividerItem renders a horizontal divider in the chat list
// to visually separate a cancelled assistant turn from whatever
// follows. Emitted by setSessionMessages right after any assistant
// message whose FinishReason is Canceled.
type InterruptDividerItem struct {
	*list.Versioned

	id  string
	sty *styles.Styles
}

// InterruptDividerID returns a stable ID for an interrupt divider tied
// to the cancelled assistant message it follows.
func InterruptDividerID(messageID string) string {
	return fmt.Sprintf("%s:interrupt-divider", messageID)
}

// NewInterruptDividerItem creates an interrupt divider for the given
// cancelled assistant message ID.
func NewInterruptDividerItem(sty *styles.Styles, assistantMessageID string) MessageItem {
	return &InterruptDividerItem{
		Versioned: list.NewVersioned(),
		id:        InterruptDividerID(assistantMessageID),
		sty:       sty,
	}
}

// ID implements MessageItem.
func (i *InterruptDividerItem) ID() string { return i.id }

// Finished implements list.Item. The divider is immutable.
func (i *InterruptDividerItem) Finished() bool { return true }

// RawRender implements MessageItem.
func (i *InterruptDividerItem) RawRender(width int) string {
	label := " interrupted "
	hrColor := i.sty.Messages.AssistantCanceled.GetForeground()
	style := lipgloss.NewStyle().Foreground(hrColor)
	if width <= len(label)+4 {
		return style.Render(strings.Repeat("─", max(0, width)))
	}
	side := (width - len(label)) / 2
	left := strings.Repeat("─", side)
	right := strings.Repeat("─", width-side-len(label))
	return style.Render(left + label + right)
}

// Render implements MessageItem.
func (i *InterruptDividerItem) Render(width int) string {
	innerWidth := max(0, width-MessageLeftPaddingTotal)
	prefix := i.sty.Messages.SectionHeader.Render()
	lines := strings.Split(i.RawRender(innerWidth), "\n")
	for j, line := range lines {
		lines[j] = prefix + line
	}
	return strings.Join(lines, "\n")
}
