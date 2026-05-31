package relay

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
)

// TaskTrace events are high-frequency host-local diagnostics that must NOT be
// mirrored to the phone. wrapEvent must skip them silently — returning nil
// without hitting the default branch, whose per-event WARN otherwise spammed
// the log hundreds of times per session (observed in production).
func TestWrapEventSkipsTaskTraceWithoutWarning(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	ev := pubsub.Event[agentruntime.TaskTrace]{
		Type:    pubsub.UpdatedEvent,
		Payload: agentruntime.TaskTrace{SessionID: "s1", NodeID: "n1"},
	}

	if got := WrapEvent(ev); got != nil {
		t.Fatalf("TaskTrace must not be relayed; got non-nil payload %+v", got)
	}
	if out := buf.String(); strings.Contains(out, "Unrecognized event type") {
		t.Fatalf("TaskTrace must be skipped silently, but a WARN was logged: %s", out)
	}
}
