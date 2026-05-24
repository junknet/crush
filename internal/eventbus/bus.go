// Package eventbus is the unified, per-session notification queue for
// external wake-up events: backgrounded shells finishing, monitor patterns
// hitting, scheduled cron timers firing. The agent's PrepareStep drains it
// each turn and folds the events into a <task-notification> system-reminder
// so the model sees them in-band rather than as separate runs.
//
// Design notes:
//   - One channel per session, lazily created. Drain() removes everything
//     currently queued and returns it sorted by Priority (Now < Next < Later)
//     then by Timestamp.
//   - Channels are buffered (busBufferSize) so a publisher never blocks a
//     coordinator goroutine; if the buffer fills the oldest event is dropped
//     and logged. Tasks waking a stuck session should not silently pile up.
//   - This bus is intentionally separate from internal/event (PostHog
//     telemetry). The name "eventbus" reflects its in-process pub/sub role.
package eventbus

import (
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Priority orders pending events when PrepareStep drains the queue.
// PriorityNow events lead, followed by Next, followed by Later. Within the
// same bucket, older events win (FIFO by Timestamp).
type Priority int

const (
	// PriorityNow — fire immediately; user-facing or destructive events.
	PriorityNow Priority = iota
	// PriorityNext — default for background completions and monitor hits.
	PriorityNext
	// PriorityLater — opportunistic; informational, may be batched.
	PriorityLater
)

const (
	// busBufferSize bounds per-session pending events to avoid unbounded
	// memory growth when a session is idle for a long time.
	busBufferSize = 256
)

// Event is one externally-originated notification destined for a session's
// next agent turn. Payload is opaque to the bus; it is rendered into the
// system-reminder by the consumer.
type Event struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id"`
	Payload   string    `json:"payload"`
	Priority  Priority  `json:"priority"`
	Timestamp time.Time `json:"timestamp"`
}

// MarshalJSONPayload is a tiny helper for callers that want to stuff a
// structured payload into Event.Payload as JSON. Errors are swallowed and
// the string "" returned so a publisher never fails on serialization.
func MarshalJSONPayload(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Bus is the consumer-side surface used by the agent and tools. Implementations
// must be safe for concurrent use across many goroutines.
type Bus interface {
	// Publish enqueues an event for the event's SessionID. If the event has
	// no SessionID or no Kind it is dropped (the bus is per-session).
	Publish(ev Event)
	// Subscribe returns a receive-only channel of events for the given
	// session. The channel is created lazily and persists for the lifetime
	// of the bus; callers should not close it.
	Subscribe(sessionID string) <-chan Event
	// Drain removes all currently-pending events for the session and returns
	// them sorted by Priority then Timestamp. Returns nil if nothing is
	// pending. Drain is the primary consumer entrypoint from PrepareStep.
	Drain(sessionID string) []Event
}

type bus struct {
	mu       sync.Mutex
	sessions map[string]chan Event
}

// New returns a fresh in-process Bus.
func New() Bus {
	return &bus{sessions: make(map[string]chan Event)}
}

// channelFor returns (or lazily creates) the channel backing a session.
// Caller must hold b.mu.
func (b *bus) channelFor(sessionID string) chan Event {
	ch, ok := b.sessions[sessionID]
	if !ok {
		ch = make(chan Event, busBufferSize)
		b.sessions[sessionID] = ch
	}
	return ch
}

// NotificationHandler is invoked asynchronously for every event that
// reaches the bus. Set via InstallNotificationHandler at coordinator
// startup so user-configured Notification hooks can fire without the
// bus knowing anything about hooks.Runner. Nil — the default — disables
// the path entirely and adds zero overhead per Publish.
var notificationHandler func(ev Event)
var notificationMu sync.RWMutex

// InstallNotificationHandler registers a function to receive every event
// that lands on the bus. Pass nil to clear. Safe to call multiple times
// — the most recent registration wins.
func InstallNotificationHandler(h func(ev Event)) {
	notificationMu.Lock()
	notificationHandler = h
	notificationMu.Unlock()
}

func (b *bus) Publish(ev Event) {
	if ev.SessionID == "" || ev.Kind == "" {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}

	notificationMu.RLock()
	h := notificationHandler
	notificationMu.RUnlock()
	if h != nil {
		// Async so a slow hook command can never block the bus. The
		// handler is responsible for its own context and timeout.
		go h(ev)
	}

	b.mu.Lock()
	ch := b.channelFor(ev.SessionID)
	b.mu.Unlock()

	select {
	case ch <- ev:
	default:
		// Buffer full — drop oldest to make room, then enqueue. We log so
		// it's visible during debugging; missing a notification silently is
		// worse than overwriting one.
		select {
		case dropped := <-ch:
			slog.Warn("Eventbus buffer full, dropped oldest event",
				"session_id", ev.SessionID,
				"dropped_kind", dropped.Kind,
				"dropped_id", dropped.ID,
			)
		default:
		}
		select {
		case ch <- ev:
		default:
			slog.Warn("Eventbus failed to enqueue even after dropping oldest",
				"session_id", ev.SessionID, "kind", ev.Kind)
		}
	}
}

func (b *bus) Subscribe(sessionID string) <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.channelFor(sessionID)
}

func (b *bus) Drain(sessionID string) []Event {
	b.mu.Lock()
	ch, ok := b.sessions[sessionID]
	b.mu.Unlock()
	if !ok {
		return nil
	}

	var events []Event
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		default:
			if len(events) == 0 {
				return nil
			}
			sort.SliceStable(events, func(i, j int) bool {
				if events[i].Priority != events[j].Priority {
					return events[i].Priority < events[j].Priority
				}
				return events[i].Timestamp.Before(events[j].Timestamp)
			})
			return events
		}
	}
}

// Default is a process-wide Bus instance. Crush is a single-process CLI/TUI;
// the agent coordinator and the tools that publish events all reach for the
// same bus, so a package-level singleton keeps wiring trivial without
// dragging a new dependency through every constructor.
var Default = New()
