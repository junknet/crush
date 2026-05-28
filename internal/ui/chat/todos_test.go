package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

func TestFormatTodosListUsesBoxStateIcons(t *testing.T) {
	sty := styles.CharmtonePantera()
	output := FormatTodosList(&sty, []session.Todo{
		{Content: "done", Status: session.TodoStatusCompleted},
		{Content: "running", Status: session.TodoStatusInProgress},
		{Content: "pending", Status: session.TodoStatusPending},
	}, 120, true)
	plain := ansi.Strip(output)

	if !strings.Contains(plain, "■ done") {
		t.Fatalf("completed todo must use stable solid box, got: %q", plain)
	}
	if !strings.Contains(plain, "□ pending") {
		t.Fatalf("pending todo must use empty box, got: %q", plain)
	}
	if strings.Contains(plain, "✓ done") || strings.Contains(plain, "▶ running") {
		t.Fatalf("todo list must not use old check/arrow icons, got: %q", plain)
	}
}

func TestFormatTodosListPrioritizesOpenWorkAndSummarizesOverflow(t *testing.T) {
	sty := styles.CharmtonePantera()
	output := FormatTodosListWithLimit(&sty, []session.Todo{
		{Content: "old done", Status: session.TodoStatusCompleted},
		{Content: "running", Status: session.TodoStatusInProgress, ActiveForm: "doing focused work"},
		{Content: "pending", Status: session.TodoStatusPending},
		{Content: "failed", Status: session.TodoStatusFailed},
	}, 120, 4, true)
	plain := ansi.Strip(output)
	lines := strings.Split(plain, "\n")

	if len(lines) != 4 {
		t.Fatalf("limited todo list must fit requested height, got %d lines: %q", len(lines), plain)
	}
	if !strings.Contains(lines[0], "running") || !strings.Contains(lines[1], "doing focused work") {
		t.Fatalf("in-progress todo and activity must be first, got: %q", plain)
	}
	if !strings.Contains(lines[2], "pending") {
		t.Fatalf("pending work should remain visible before older terminal states, got: %q", plain)
	}
	if !strings.Contains(lines[3], "+1 failed, 1 completed") {
		t.Fatalf("hidden terminal states should be summarized, got: %q", plain)
	}
}

func TestUserMessageItemDoubleClickCopyTextReturnsOriginalInput(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "user-copy",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "  original input\nwith spacing  "},
		},
	}
	renderer := attachments.NewRenderer(
		sty.Attachments.Normal,
		sty.Attachments.Deleting,
		sty.Attachments.Image,
		sty.Attachments.Text,
	)
	item := NewUserMessageItem(&sty, msg, renderer).(*UserMessageItem)

	text, notification, ok := item.DoubleClickCopyText()

	if !ok {
		t.Fatalf("user input should be copyable")
	}
	if text != "  original input\nwith spacing  " {
		t.Fatalf("copy text must preserve original input, got: %q", text)
	}
	if notification != "Input copied to clipboard" {
		t.Fatalf("unexpected notification: %q", notification)
	}
}
