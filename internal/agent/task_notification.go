package agent

import (
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/eventbus"
)

// renderTaskNotification turns the drained event list into the body of a
// <task-notification> system-reminder, one stanza per event. The stanza
// records Kind, Timestamp, and Payload so the model can decide whether to
// react now or defer.
func renderTaskNotification(events []eventbus.Event) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<task-notification>\n")
	b.WriteString("The following events arrived from out-of-band signals (backgrounded shells finishing, monitors firing, scheduled wake-ups). React to them as part of your next step.\n")
	for _, ev := range events {
		ts := ev.Timestamp.UTC().Format(time.RFC3339)
		b.WriteString(fmt.Sprintf("\n- Kind: %s\n  Time: %s\n", ev.Kind, ts))
		if ev.Payload != "" {
			b.WriteString(fmt.Sprintf("  Payload: %s\n", ev.Payload))
		}
	}
	b.WriteString("</task-notification>")
	return b.String()
}

// injectTaskNotification splices the notification into the message tail so the
// model treats it as part of the current user turn rather than a fresh system
// instruction. Strategy:
//
//   - find the last MessageRoleUser message and prepend a TextPart to it;
//   - if none exists (shouldn't normally happen here), append a synthetic
//     user message carrying only the notification.
//
// We mutate a copy of the message; the input slice's other entries are not
// touched.
func injectTaskNotification(messages []fantasy.Message, notification string) []fantasy.Message {
	if notification == "" {
		return messages
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != fantasy.MessageRoleUser {
			continue
		}
		updated := messages[i]
		newContent := make([]fantasy.MessagePart, 0, len(updated.Content)+1)
		newContent = append(newContent, fantasy.TextPart{Text: notification})
		newContent = append(newContent, updated.Content...)
		updated.Content = newContent
		messages[i] = updated
		return messages
	}
	return append(messages, fantasy.NewUserMessage(notification))
}
