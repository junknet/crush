package agent

import (
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/eventbus"
)

func TestRenderTaskNotification(t *testing.T) {
	events := []eventbus.Event{
		{
			Kind:      "bash_done",
			Payload:   `{"shell_id":"abc","exit_code":0}`,
			Timestamp: time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC),
		},
		{
			Kind:      "cron_fired",
			Payload:   `{"task_id":"t1","reason":"daily ping"}`,
			Timestamp: time.Date(2026, 5, 23, 10, 1, 0, 0, time.UTC),
		},
	}
	out := renderTaskNotification(events)
	if !strings.Contains(out, "<task-notification>") || !strings.Contains(out, "</task-notification>") {
		t.Fatalf("missing wrapper: %s", out)
	}
	if !strings.Contains(out, "bash_done") || !strings.Contains(out, "cron_fired") {
		t.Fatalf("missing event kinds: %s", out)
	}
	if !strings.Contains(out, "2026-05-23T10:00:00Z") {
		t.Fatalf("missing timestamp: %s", out)
	}
}

func TestInjectTaskNotificationPrependsLastUser(t *testing.T) {
	messages := []fantasy.Message{
		fantasy.NewSystemMessage("system rules"),
		fantasy.NewUserMessage("first user msg"),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "ok"}}},
		fantasy.NewUserMessage("second user msg"),
	}
	out := injectTaskNotification(messages, "<task-notification>hi</task-notification>")
	last := out[len(out)-1]
	if last.Role != fantasy.MessageRoleUser {
		t.Fatalf("expected last role user, got %v", last.Role)
	}
	if len(last.Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(last.Content))
	}
	first, ok := last.Content[0].(fantasy.TextPart)
	if !ok || !strings.Contains(first.Text, "<task-notification>") {
		t.Fatalf("notification not prepended: %+v", last.Content[0])
	}
	second, ok := last.Content[1].(fantasy.TextPart)
	if !ok || second.Text != "second user msg" {
		t.Fatalf("original user text not preserved: %+v", last.Content[1])
	}
	// First user message must be untouched.
	if got := out[1].Content[0].(fantasy.TextPart).Text; got != "first user msg" {
		t.Fatalf("first user msg mutated: %s", got)
	}
}

func TestInjectTaskNotificationNoUserAppendsSynthetic(t *testing.T) {
	messages := []fantasy.Message{
		fantasy.NewSystemMessage("system rules"),
	}
	out := injectTaskNotification(messages, "<task-notification>hi</task-notification>")
	if len(out) != 2 {
		t.Fatalf("expected synthetic user appended, got %d msgs", len(out))
	}
	if out[1].Role != fantasy.MessageRoleUser {
		t.Fatalf("synthetic message has wrong role: %v", out[1].Role)
	}
}

func TestInjectTaskNotificationEmptyNoop(t *testing.T) {
	messages := []fantasy.Message{fantasy.NewUserMessage("hi")}
	out := injectTaskNotification(messages, "")
	if len(out) != 1 {
		t.Fatalf("empty notification should be no-op, got %d msgs", len(out))
	}
	if got := out[0].Content[0].(fantasy.TextPart).Text; got != "hi" {
		t.Fatalf("message mutated: %s", got)
	}
}

// TestDrainToInjectionEndToEnd: publish events on the bus, drain, render,
// inject — mimics what PrepareStep does each turn.
func TestDrainToInjectionEndToEnd(t *testing.T) {
	bus := eventbus.New()
	bus.Publish(eventbus.Event{
		Kind:      "bash_done",
		SessionID: "sess-end",
		Payload:   `{"shell_id":"x","exit_code":0}`,
		Priority:  eventbus.PriorityNext,
	})

	pending := bus.Drain("sess-end")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending event, got %d", len(pending))
	}
	notification := renderTaskNotification(pending)
	if !strings.Contains(notification, "bash_done") {
		t.Fatalf("rendered notification missing bash_done: %s", notification)
	}

	msgs := []fantasy.Message{fantasy.NewUserMessage("please continue")}
	out := injectTaskNotification(msgs, notification)
	first, ok := out[0].Content[0].(fantasy.TextPart)
	if !ok || !strings.Contains(first.Text, "<task-notification>") {
		t.Fatal("notification not prepended onto user message")
	}
}
