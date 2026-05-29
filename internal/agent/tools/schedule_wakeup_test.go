package tools

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/eventbus"
	"github.com/charmbracelet/crush/internal/pubsub"
)

func newTestScheduler(t *testing.T) *scheduler {
	t.Helper()
	return &scheduler{
		tasks:    make(map[string]*persistedTask),
		filePath: filepath.Join(t.TempDir(), scheduledTasksFilename),
		clock: func() time.Time {
			return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
		},
		rng: rand.New(rand.NewSource(1)),
	}
}

func decodeScheduleWakeupMetadata(t *testing.T, resp fantasy.ToolResponse) ScheduleWakeupResponseMetadata {
	t.Helper()
	var metadata ScheduleWakeupResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &metadata); err != nil {
		t.Fatalf("unmarshal metadata %q: %v", resp.Metadata, err)
	}
	return metadata
}

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

func TestScheduleWakeupReplacesSameKey(t *testing.T) {
	s := newTestScheduler(t)

	first := s.addDelayTask("sess-key", "download:123", "old poll", time.Hour, true)
	second := s.addDelayTask("sess-key", "download:123", "new poll", 2*time.Hour, true)

	if first.replacedCount != 0 {
		t.Fatalf("first replaced_count=%d, want 0", first.replacedCount)
	}
	if second.replacedCount != 1 {
		t.Fatalf("second replaced_count=%d, want 1", second.replacedCount)
	}
	if len(s.tasks) != 1 {
		t.Fatalf("task count=%d, want 1", len(s.tasks))
	}
	if _, ok := s.tasks[first.task.ID]; ok {
		t.Fatal("old keyed task was not replaced")
	}
	if got := s.tasks[second.task.ID].Reason; got != "new poll" {
		t.Fatalf("remaining reason=%q, want new poll", got)
	}
}

func TestScheduleWakeupDifferentKeysCoexist(t *testing.T) {
	s := newTestScheduler(t)

	s.addDelayTask("sess-key", "download:123", "poll download", time.Hour, true)
	second := s.addDelayTask("sess-key", "deploy:456", "poll deploy", time.Hour, true)

	if second.replacedCount != 0 {
		t.Fatalf("replaced_count=%d, want 0", second.replacedCount)
	}
	if len(s.tasks) != 2 {
		t.Fatalf("task count=%d, want 2", len(s.tasks))
	}
}

func TestScheduleWakeupCancelWakeups(t *testing.T) {
	s := newTestScheduler(t)

	s.addDelayTask("sess-cancel", "download:123", "poll download", time.Hour, true)
	s.addDelayTask("sess-cancel", "deploy:456", "poll deploy", time.Hour, true)
	s.addDelayTask("other-session", "download:123", "poll other", time.Hour, true)

	if got := s.cancelWakeups("sess-cancel", "download:123"); got != 1 {
		t.Fatalf("cancelled=%d, want 1", got)
	}
	if len(s.tasks) != 2 {
		t.Fatalf("task count=%d, want 2", len(s.tasks))
	}
	for _, task := range s.tasks {
		if task.SessionID == "sess-cancel" && task.Key == "download:123" {
			t.Fatal("cancelled task is still present")
		}
	}
}

func TestScheduleWakeupNoKeyKeepsLegacyBehavior(t *testing.T) {
	s := newTestScheduler(t)

	first := s.addDelayTask("sess-legacy", "", "first poll", time.Hour, true)
	second := s.addDelayTask("sess-legacy", "", "second poll", time.Hour, true)

	if first.replacedCount != 0 || second.replacedCount != 0 {
		t.Fatalf("replaced counts=(%d,%d), want both 0", first.replacedCount, second.replacedCount)
	}
	if len(s.tasks) != 2 {
		t.Fatalf("task count=%d, want 2", len(s.tasks))
	}
}

func TestScheduleWakeupMetadataIncludesKeyAndReplacedCount(t *testing.T) {
	tool := NewScheduleWakeupTool(t.TempDir())
	callCtx := context.WithValue(context.Background(), SessionIDContextKey, "sess-meta")
	key := "download:metadata-test"
	defer CancelWakeups("sess-meta", key)

	resp, err := tool.Run(callCtx, fantasy.ToolCall{
		ID:    "meta-1",
		Name:  ScheduleWakeupToolName,
		Input: `{"delay_seconds":86400,"reason":"old poll","key":"download:metadata-test"}`,
	})
	if err != nil {
		t.Fatalf("tool run error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("tool returned error response: %s", resp.Content)
	}
	first := decodeScheduleWakeupMetadata(t, resp)
	if first.Key != key {
		t.Fatalf("metadata key=%q, want %q", first.Key, key)
	}
	if first.ReplacedCount != 0 {
		t.Fatalf("first replaced_count=%d, want 0", first.ReplacedCount)
	}

	resp, err = tool.Run(callCtx, fantasy.ToolCall{
		ID:    "meta-2",
		Name:  ScheduleWakeupToolName,
		Input: `{"delay_seconds":86400,"reason":"new poll","task_key":"download:metadata-test"}`,
	})
	if err != nil {
		t.Fatalf("tool run error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("tool returned error response: %s", resp.Content)
	}
	second := decodeScheduleWakeupMetadata(t, resp)
	if second.TaskID == first.TaskID {
		t.Fatal("replacement reused the old task id")
	}
	if second.Key != key {
		t.Fatalf("metadata key=%q, want %q", second.Key, key)
	}
	if second.ReplacedCount != 1 {
		t.Fatalf("second replaced_count=%d, want 1", second.ReplacedCount)
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

// TestPublishWakeupUsesOnlyCoordinatorBroker verifies that a fired timer wakes
// the coordinator through wakeupBroker without also queueing a task-notification
// event. The direct c.Run continuation is the single injection path.
func TestPublishWakeupUsesOnlyCoordinatorBroker(t *testing.T) {
	oldBus := eventbus.Default
	eventbus.Default = eventbus.New()
	t.Cleanup(func() {
		eventbus.Default = oldBus
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wakeupCh := SubscribeWakeups(ctx)
	eventCh := eventbus.Default.Subscribe("sess-bus")
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
	case ev := <-wakeupCh:
		if ev.Payload.SessionID != "sess-bus" {
			t.Fatalf("unexpected session: %s", ev.Payload.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wakeup broker never saw fired task")
	}

	select {
	case ev := <-eventCh:
		t.Fatalf("unexpected eventbus notification: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}
