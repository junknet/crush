package relay

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// msgEvent builds a message event for the persistence-gate table test.
func msgEvent(evType pubsub.EventType, role message.MessageRole, finished bool) pubsub.Event[message.Message] {
	m := message.Message{ID: "m1", Role: role}
	if finished {
		m.Parts = []message.ContentPart{message.Finish{Reason: message.FinishReasonEndTurn}}
	}
	return pubsub.Event[message.Message]{Type: evType, Payload: m}
}

// TestShouldPersistMessage pins the relay's live-vs-durable split: an assistant
// turn streams ~30 full-snapshot UpdatedEvents/sec that must NOT each land in
// JetStream (only the finished snapshot does), while user/tool messages arrive
// complete and deletions must propagate so a cold-open never resurrects a
// removed message.
func TestShouldPersistMessage(t *testing.T) {
	cases := []struct {
		name string
		ev   pubsub.Event[message.Message]
		want bool
	}{
		{"streaming assistant update is ephemeral", msgEvent(pubsub.UpdatedEvent, message.Assistant, false), false},
		{"created assistant shell is ephemeral", msgEvent(pubsub.CreatedEvent, message.Assistant, false), false},
		{"finished assistant is durable", msgEvent(pubsub.UpdatedEvent, message.Assistant, true), true},
		{"user message is durable", msgEvent(pubsub.CreatedEvent, message.User, false), true},
		{"tool message is durable", msgEvent(pubsub.CreatedEvent, message.Tool, false), true},
		{"deletion is durable", msgEvent(pubsub.DeletedEvent, message.Assistant, false), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPersistMessage(c.ev); got != c.want {
				t.Fatalf("shouldPersistMessage(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}
