package eventbus

import (
	"testing"
	"time"
)

func TestPublishAndDrain(t *testing.T) {
	b := New()
	b.Publish(Event{Kind: "bash_done", SessionID: "s1", Payload: "p1", Priority: PriorityNext})
	b.Publish(Event{Kind: "monitor_match", SessionID: "s1", Payload: "p2", Priority: PriorityNext})

	got := b.Drain("s1")
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Kind != "bash_done" || got[1].Kind != "monitor_match" {
		t.Fatalf("FIFO order broken: %+v", got)
	}

	// Drain again must be empty.
	if again := b.Drain("s1"); again != nil {
		t.Fatalf("expected empty after drain, got %d", len(again))
	}
}

func TestDrainPrioritySort(t *testing.T) {
	b := New()
	now := time.Now()
	b.Publish(Event{Kind: "late", SessionID: "s1", Priority: PriorityLater, Timestamp: now})
	b.Publish(Event{Kind: "next", SessionID: "s1", Priority: PriorityNext, Timestamp: now.Add(time.Millisecond)})
	b.Publish(Event{Kind: "now", SessionID: "s1", Priority: PriorityNow, Timestamp: now.Add(2 * time.Millisecond)})

	got := b.Drain("s1")
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	if got[0].Kind != "now" || got[1].Kind != "next" || got[2].Kind != "late" {
		t.Fatalf("priority sort broken: %+v", got)
	}
}

func TestDrainPrioritySortStableByTimestamp(t *testing.T) {
	b := New()
	base := time.Now()
	// Same priority, two events; earlier timestamp must come first regardless
	// of insertion order.
	b.Publish(Event{Kind: "second", SessionID: "s1", Priority: PriorityNext, Timestamp: base.Add(time.Second)})
	b.Publish(Event{Kind: "first", SessionID: "s1", Priority: PriorityNext, Timestamp: base})

	got := b.Drain("s1")
	if len(got) != 2 || got[0].Kind != "first" || got[1].Kind != "second" {
		t.Fatalf("FIFO-within-priority broken: %+v", got)
	}
}

func TestPublishDropsEmpty(t *testing.T) {
	b := New()
	b.Publish(Event{Kind: "", SessionID: "s1"}) // no kind
	b.Publish(Event{Kind: "x", SessionID: ""})  // no session
	if ev := b.Drain("s1"); ev != nil {
		t.Fatalf("malformed event should not have been queued: %+v", ev)
	}
}

func TestPublishAutoFillsIDAndTimestamp(t *testing.T) {
	b := New()
	b.Publish(Event{Kind: "k", SessionID: "s1"})
	got := b.Drain("s1")
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].ID == "" {
		t.Fatal("expected ID to be auto-filled")
	}
	if got[0].Timestamp.IsZero() {
		t.Fatal("expected Timestamp to be auto-filled")
	}
}

func TestSubscribeChannelReceives(t *testing.T) {
	b := New()
	ch := b.Subscribe("s1")
	b.Publish(Event{Kind: "k", SessionID: "s1", Payload: "hi"})

	select {
	case ev := <-ch:
		if ev.Kind != "k" || ev.Payload != "hi" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered to subscriber")
	}
}

func TestDrainScopedToSession(t *testing.T) {
	b := New()
	b.Publish(Event{Kind: "k", SessionID: "s1"})
	b.Publish(Event{Kind: "k", SessionID: "s2"})

	got1 := b.Drain("s1")
	got2 := b.Drain("s2")
	if len(got1) != 1 || len(got2) != 1 {
		t.Fatalf("session isolation broken: s1=%d s2=%d", len(got1), len(got2))
	}
}

func TestBufferOverflowDropsOldest(t *testing.T) {
	b := New()
	// Fill past capacity to force the drop path; bus must remain functional.
	for i := 0; i < busBufferSize+10; i++ {
		b.Publish(Event{Kind: "k", SessionID: "s1", Payload: "x"})
	}
	got := b.Drain("s1")
	if len(got) == 0 || len(got) > busBufferSize {
		t.Fatalf("unexpected drained count: %d (cap=%d)", len(got), busBufferSize)
	}
}
