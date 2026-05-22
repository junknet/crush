package tools

import (
	"context"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// TestScheduleWakeupBrokerRoundTrip proves the subscribe/publish path the
// coordinator relies on to receive timer wake-ups.
func TestScheduleWakeupBrokerRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := SubscribeWakeups(ctx)

	wakeupBroker.Publish(pubsub.CreatedEvent, WakeupRequest{SessionID: "s1", Reason: "r1"})

	select {
	case ev := <-ch:
		if ev.Payload.SessionID != "s1" || ev.Payload.Reason != "r1" {
			t.Fatalf("unexpected payload: %+v", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive published wake-up request")
	}
}

// TestScheduleWakeupToolFires proves invoking the tool actually schedules a
// timer that publishes a wake-up for the calling session.
func TestScheduleWakeupToolFires(t *testing.T) {
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := SubscribeWakeups(subCtx)

	tool := NewScheduleWakeupTool()
	callCtx := context.WithValue(context.Background(), SessionIDContextKey, "sess-x")

	resp, err := tool.Run(callCtx, fantasy.ToolCall{
		ID:    "call-1",
		Name:  ScheduleWakeupToolName,
		Input: `{"delay_seconds":5,"reason":"check CI"}`,
	})
	if err != nil {
		t.Fatalf("tool run error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("tool returned error response: %s", resp.Content)
	}

	// Delay is clamped to MinWakeupSeconds (5s); allow margin.
	select {
	case ev := <-ch:
		if ev.Payload.SessionID != "sess-x" {
			t.Fatalf("wrong session woken: %q", ev.Payload.SessionID)
		}
		if ev.Payload.Reason != "check CI" {
			t.Fatalf("wrong reason: %q", ev.Payload.Reason)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("scheduled wake-up never fired")
	}
}
