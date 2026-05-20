package scheduler

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
)

// EventKind reports the lifecycle state of a task node.
type EventKind string

const (
	EventTaskPlanned  EventKind = "task_planned"
	EventTaskStarted  EventKind = "task_started"
	EventTaskProgress EventKind = "task_progress"
	EventTaskFinished EventKind = "task_finished"
	EventTaskFailed   EventKind = "task_failed"
)

// Event is a semantic task event that the UI can render directly.
type Event struct {
	ConversationSessionID string
	SessionID             string
	NodeID                string
	ParentID              string
	Goal                  string
	Status                string
	Profile               WorkerProfile
	Kind                  EventKind
	Success               bool
	Error                 string
	Output                string
	Scope                 []string
	Sequence              int64
	RecordedAt            time.Time
	TraceID               string
}

var schedulerBroker = pubsub.NewBroker[Event]()

// SubscribeEvents returns a channel of task lifecycle events.
func SubscribeEvents(ctx context.Context) <-chan pubsub.Event[Event] {
	return schedulerBroker.Subscribe(ctx)
}

// PublishEvent emits a task lifecycle event.
func PublishEvent(ev Event) {
	schedulerBroker.Publish(pubsub.UpdatedEvent, ev)
}

// NewEventFromNode constructs a task event from a task node snapshot.
func NewEventFromNode(kind EventKind, node *TaskNode, trace agentruntime.TaskTrace, status string, success bool, errText, output string) Event {
	if node == nil {
		return Event{}
	}
	parentID := node.Intent.ParentID
	if node.Parent != nil {
		parentID = node.Parent.ID
	}
	conversationSessionID := node.ConversationSessionID
	if conversationSessionID == "" {
		conversationSessionID = trace.ConversationSessionID
	}
	traceIDSource := conversationSessionID
	if traceIDSource == "" {
		traceIDSource = trace.SessionID
	}
	if traceIDSource == "" {
		traceIDSource = node.SessionID
	}
	traceID := ""
	if traceIDSource != "" {
		traceID = fmt.Sprintf("%s:%d", traceIDSource, trace.Sequence)
	}
	return Event{
		ConversationSessionID: conversationSessionID,
		SessionID:             node.SessionID,
		NodeID:                node.ID,
		ParentID:              parentID,
		Goal:                  node.Intent.Goal,
		Status:                status,
		Profile:               node.Profile,
		Kind:                  kind,
		Success:               success,
		Error:                 errText,
		Output:                output,
		Scope:                 append([]string(nil), node.Intent.Scope...),
		Sequence:              trace.Sequence,
		RecordedAt:            trace.RecordedAt,
		TraceID:               traceID,
	}
}

// AsMessage converts the event to a Bubble Tea message.
func (e Event) AsMessage() tea.Msg {
	return e
}
