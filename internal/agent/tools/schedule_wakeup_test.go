package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/eventbus"
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

	tool := NewScheduleWakeupTool(t.TempDir())
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

// TestCronExpressionParse covers the small standalone parser.
func TestCronExpressionParse(t *testing.T) {
	cases := []struct {
		expr    string
		wantErr bool
	}{
		{"* * * * *", false},
		{"*/5 * * * *", false},
		{"0 9 * * 1-5", false},
		{"0,15,30,45 * * * *", false},
		{"60 * * * *", true},
		{"* * * * 8", true},
		{"not a cron", true},
		{"", true},
	}
	for _, c := range cases {
		_, err := parseCronExpression(c.expr)
		gotErr := err != nil
		if gotErr != c.wantErr {
			t.Errorf("parseCronExpression(%q) err=%v wantErr=%v", c.expr, err, c.wantErr)
		}
	}
}

// TestCronNextEveryMinute verifies the next-time search advances by exactly
// one minute for the every-minute schedule.
func TestCronNextEveryMinute(t *testing.T) {
	spec, err := parseCronExpression("* * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	from := time.Date(2026, 5, 23, 10, 30, 15, 0, time.UTC)
	got, err := spec.next(from)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	// Expect 10:31:00.
	if got.Hour() != 10 || got.Minute() != 31 || got.Second() != 0 {
		t.Fatalf("unexpected next: %v", got)
	}
}

// TestCronNextWeekdayMorning verifies day-of-week + hour interaction.
func TestCronNextWeekdayMorning(t *testing.T) {
	spec, err := parseCronExpression("0 9 * * 1-5")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Saturday at 12:00 — next match is Monday 09:00.
	from := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC) // 2026-05-23 is a Saturday.
	got, err := spec.next(from)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got.Weekday() != time.Monday || got.Hour() != 9 || got.Minute() != 0 {
		t.Fatalf("unexpected next: %v (weekday=%v)", got, got.Weekday())
	}
}

// TestSchedulerPersistsCronTask round-trips a cron task through the on-disk
// JSON file: addCronTask → loadLocked from a fresh scheduler reads it back.
func TestSchedulerPersistsCronTask(t *testing.T) {
	dir := t.TempDir()
	tool := NewScheduleWakeupTool(dir)
	callCtx := context.WithValue(context.Background(), SessionIDContextKey, "sess-cron")
	resp, err := tool.Run(callCtx, fantasy.ToolCall{
		ID:    "c1",
		Name:  ScheduleWakeupToolName,
		Input: `{"cron_expression":"0 9 * * 1-5","reason":"daily standup ping"}`,
	})
	if err != nil {
		t.Fatalf("tool run error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("tool returned error response: %s", resp.Content)
	}

	path := filepath.Join(dir, scheduledTasksFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tasks file: %v", err)
	}
	var list []persistedTask
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected a persisted cron task, got none")
	}
	// At least one of the persisted tasks should be ours.
	var ours *persistedTask
	for i := range list {
		if list[i].SessionID == "sess-cron" && list[i].CronExpression == "0 9 * * 1-5" {
			ours = &list[i]
			break
		}
	}
	if ours == nil {
		t.Fatalf("did not find our cron task in %+v", list)
	}
	if !ours.Recurring {
		t.Fatal("expected Recurring=true")
	}
	if ours.ExpiresAt.IsZero() {
		t.Fatal("expected ExpiresAt to be set for a recurring task")
	}
}

// TestEventBusReceivesCronFire verifies that when the scheduler tick fires a
// task, it publishes a cron_fired event onto the unified eventbus.
func TestEventBusReceivesCronFire(t *testing.T) {
	ch := eventbus.Default.Subscribe("sess-bus")
	task := &persistedTask{
		ID:             "t1",
		SessionID:      "sess-bus",
		Reason:         "ping",
		CronExpression: "* * * * *",
		Recurring:      true,
		LastFireAt:     time.Now().UTC(),
	}
	publishWakeup(task)

	select {
	case ev := <-ch:
		if ev.Kind != "cron_fired" {
			t.Fatalf("unexpected kind: %s", ev.Kind)
		}
		if ev.SessionID != "sess-bus" {
			t.Fatalf("unexpected session: %s", ev.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("eventbus never saw cron_fired")
	}
}
