package eventbus

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationHandler_DefaultOffNoOverhead(t *testing.T) {
	t.Cleanup(func() { InstallNotificationHandler(nil) })

	InstallNotificationHandler(nil)
	b := New()
	b.Publish(Event{
		SessionID: "s1",
		Kind:      "test",
	})
	// Subscribe and drain so the test does not leak a buffered event.
	_ = b.Subscribe("s1")
}

func TestNotificationHandler_FiresAsync(t *testing.T) {
	t.Cleanup(func() { InstallNotificationHandler(nil) })

	var calls int32
	done := make(chan Event, 4)
	InstallNotificationHandler(func(ev Event) {
		atomic.AddInt32(&calls, 1)
		done <- ev
	})

	b := New()
	b.Publish(Event{SessionID: "s1", Kind: "k1"})
	b.Publish(Event{SessionID: "s1", Kind: "k2"})

	collected := make(map[string]bool)
	deadline := time.After(2 * time.Second)
	for len(collected) < 2 {
		select {
		case ev := <-done:
			collected[ev.Kind] = true
		case <-deadline:
			t.Fatalf("expected 2 handler calls, got %d", atomic.LoadInt32(&calls))
		}
	}
	if !collected["k1"] || !collected["k2"] {
		t.Errorf("handler missed an event: %v", collected)
	}
}

func TestNotificationHandler_NoDeadlockWithSlowHandler(t *testing.T) {
	t.Cleanup(func() { InstallNotificationHandler(nil) })

	// Handler intentionally blocks past Publish's lifetime; bus must
	// publish via a goroutine and never wait for it.
	var wg sync.WaitGroup
	wg.Add(1)
	InstallNotificationHandler(func(_ Event) {
		defer wg.Done()
		time.Sleep(150 * time.Millisecond)
	})

	b := New()
	start := time.Now()
	b.Publish(Event{SessionID: "s1", Kind: "k"})
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Errorf("Publish should not block on slow handler; took %v", took)
	}
	wg.Wait()
}
