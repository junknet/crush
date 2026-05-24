package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/eventbus"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

const (
	ScheduleWakeupToolName = "schedule_wakeup"
	// MinWakeupSeconds / MaxWakeupSeconds clamp the delay so a wakeup is neither
	// a tight busy-loop nor effectively never.
	MinWakeupSeconds = 5
	MaxWakeupSeconds = 24 * 60 * 60

	// scheduledTasksFilename is the on-disk JSON catalogue under DataDirectory.
	scheduledTasksFilename = "scheduled_tasks.json"
	// recurringTaskTTL caps how long a recurring cron task can keep firing
	// before being auto-pruned, matching the free-code reference 7-day rule.
	recurringTaskTTL = 7 * 24 * time.Hour
	// schedulerTickInterval is the polling cadence of the persistent scheduler.
	schedulerTickInterval = 1 * time.Second
)

//go:embed schedule_wakeup.md
var scheduleWakeupDescription string

// WakeupRequest asks the coordinator to resume a session after a timer fires.
type WakeupRequest struct {
	SessionID string
	Reason    string
}

var wakeupBroker = pubsub.NewBroker[WakeupRequest]()

// SubscribeWakeups returns a channel of scheduled wake-up requests. The agent
// coordinator subscribes to drive timer-based re-wakeups.
func SubscribeWakeups(ctx context.Context) <-chan pubsub.Event[WakeupRequest] {
	return wakeupBroker.Subscribe(ctx)
}

// persistedTask is the JSON-serialisable shape of a scheduled wake-up,
// covering both one-shot delays and recurring cron tasks.
type persistedTask struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"session_id"`
	Reason         string    `json:"reason"`
	CronExpression string    `json:"cron_expression,omitempty"`
	NextFireAt     time.Time `json:"next_fire_at"`
	LastFireAt     time.Time `json:"last_fire_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Recurring      bool      `json:"recurring"`
}

// scheduler is the singleton timer loop backing schedule_wakeup. It owns the
// on-disk task file, runs a 1s tick loop, and fires events through both the
// legacy wakeupBroker (so the coordinator's existing watch loop keeps
// working) and the unified eventbus (so PrepareStep can drain them as
// task-notifications).
type scheduler struct {
	mu       sync.Mutex
	tasks    map[string]*persistedTask
	dataDir  string
	filePath string
	started  bool
	clock    func() time.Time // overridable for tests
	rng      *rand.Rand       // jitter source; package-local for test seeding
}

var defaultScheduler = &scheduler{
	tasks: make(map[string]*persistedTask),
	clock: time.Now,
	rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
}

// startScheduler binds the scheduler to a DataDirectory and starts the 1s
// poll loop. Safe to call multiple times — the loop is launched once. The
// dataDir is honoured on the first call only; later calls retarget the
// on-disk file (useful for tests that spin up a fresh tempdir, but harmless
// in production where the coordinator only constructs the tool once).
func startScheduler(dataDir string) {
	defaultScheduler.mu.Lock()
	defer defaultScheduler.mu.Unlock()
	if dataDir != "" {
		defaultScheduler.dataDir = dataDir
		defaultScheduler.filePath = filepath.Join(dataDir, scheduledTasksFilename)
		if !defaultScheduler.started {
			if err := defaultScheduler.loadLocked(); err != nil {
				slog.Warn("Scheduler: failed to load persisted tasks", "path", defaultScheduler.filePath, "error", err)
			}
		}
	}
	if defaultScheduler.started {
		return
	}
	defaultScheduler.started = true
	go defaultScheduler.runLoop()
}

func (s *scheduler) loadLocked() error {
	if s.filePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var list []*persistedTask
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	now := s.clock().UTC()
	for _, t := range list {
		// Drop tasks past their TTL on restore.
		if !t.ExpiresAt.IsZero() && now.After(t.ExpiresAt) {
			continue
		}
		s.tasks[t.ID] = t
	}
	slog.Debug("Scheduler: loaded persisted tasks", "count", len(s.tasks), "path", s.filePath)
	return nil
}

func (s *scheduler) saveLocked() {
	if s.filePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		slog.Warn("Scheduler: failed to create data directory", "dir", filepath.Dir(s.filePath), "error", err)
		return
	}
	list := make([]*persistedTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		list = append(list, t)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		slog.Warn("Scheduler: failed to marshal tasks", "error", err)
		return
	}
	// Write+rename to avoid a partial file if we're killed mid-write.
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("Scheduler: failed to write tasks", "path", tmp, "error", err)
		return
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		slog.Warn("Scheduler: failed to rename tasks file", "from", tmp, "to", s.filePath, "error", err)
	}
}

// addDelayTask enqueues a one-shot delay task.
func (s *scheduler) addDelayTask(sessionID, reason string, delay time.Duration) *persistedTask {
	now := s.clock().UTC()
	task := &persistedTask{
		ID:         uuid.NewString(),
		SessionID:  sessionID,
		Reason:     reason,
		NextFireAt: now.Add(delay),
		CreatedAt:  now,
		Recurring:  false,
	}
	s.mu.Lock()
	s.tasks[task.ID] = task
	s.saveLocked()
	s.mu.Unlock()
	return task
}

// addCronTask enqueues a recurring cron-driven task. Returns an error if the
// cron expression is unparseable.
func (s *scheduler) addCronTask(sessionID, reason, expr string) (*persistedTask, error) {
	spec, err := parseCronExpression(expr)
	if err != nil {
		return nil, err
	}
	now := s.clock().UTC()
	next, err := spec.next(now)
	if err != nil {
		return nil, err
	}
	next = s.jitter(next)
	task := &persistedTask{
		ID:             uuid.NewString(),
		SessionID:      sessionID,
		Reason:         reason,
		CronExpression: expr,
		NextFireAt:     next,
		CreatedAt:      now,
		ExpiresAt:      now.Add(recurringTaskTTL),
		Recurring:      true,
	}
	s.mu.Lock()
	s.tasks[task.ID] = task
	s.saveLocked()
	s.mu.Unlock()
	return task, nil
}

// jitter spreads identical schedules to avoid thundering-herd polling.
//
//   - periodic-like schedules: ±10% of distance to now, capped at ±15 min;
//   - top-of-hour schedules (minute == 0): ±90 seconds.
func (s *scheduler) jitter(next time.Time) time.Time {
	now := s.clock().UTC()
	gap := next.Sub(now)
	if gap <= 0 {
		return next
	}
	maxJitter := time.Duration(float64(gap) * 0.1)
	if maxJitter > 15*time.Minute {
		maxJitter = 15 * time.Minute
	}
	if next.Minute() == 0 {
		if maxJitter < 90*time.Second {
			maxJitter = 90 * time.Second
		}
	}
	if maxJitter <= 0 {
		return next
	}
	// rand.Int63n needs positive arg; symmetric ±maxJitter around the target.
	offset := time.Duration(s.rng.Int63n(int64(maxJitter*2))) - maxJitter
	return next.Add(offset)
}

// runLoop is the 1s tick. On each tick: fire any task whose NextFireAt has
// passed, then for recurring tasks recompute NextFireAt; for one-shot tasks
// or expired recurring tasks, delete.
func (s *scheduler) runLoop() {
	ticker := time.NewTicker(schedulerTickInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.tick()
	}
}

func (s *scheduler) tick() {
	s.mu.Lock()
	now := s.clock().UTC()
	var toFire []*persistedTask
	var toDelete []string
	dirty := false
	for id, task := range s.tasks {
		// Drop expired recurring tasks.
		if !task.ExpiresAt.IsZero() && now.After(task.ExpiresAt) {
			toDelete = append(toDelete, id)
			continue
		}
		if now.Before(task.NextFireAt) {
			continue
		}
		toFire = append(toFire, task)
		task.LastFireAt = now
		dirty = true
		if !task.Recurring {
			toDelete = append(toDelete, id)
			continue
		}
		// Recurring: compute next slot. If parsing fails for some reason
		// (e.g. corrupt on-disk state), drop the task.
		spec, err := parseCronExpression(task.CronExpression)
		if err != nil {
			slog.Warn("Scheduler: corrupt cron expression, dropping task", "id", id, "expr", task.CronExpression, "error", err)
			toDelete = append(toDelete, id)
			continue
		}
		nxt, err := spec.next(now)
		if err != nil {
			slog.Warn("Scheduler: cron has no future match, dropping task", "id", id, "expr", task.CronExpression, "error", err)
			toDelete = append(toDelete, id)
			continue
		}
		task.NextFireAt = s.jitter(nxt)
	}
	for _, id := range toDelete {
		delete(s.tasks, id)
		dirty = true
	}
	if dirty {
		s.saveLocked()
	}
	s.mu.Unlock()

	// Publish fires outside the lock so a slow subscriber cannot deadlock the
	// scheduler.
	for _, task := range toFire {
		publishWakeup(task)
	}
}

// publishWakeup fans a fired task out to both surfaces: the legacy
// pubsub broker (existing coordinator drives c.Run from it) and the unified
// eventbus (PrepareStep drains it as a task-notification).
func publishWakeup(task *persistedTask) {
	wakeupBroker.Publish(pubsub.CreatedEvent, WakeupRequest{
		SessionID: task.SessionID,
		Reason:    task.Reason,
	})
	payload := map[string]any{
		"task_id":         task.ID,
		"reason":          task.Reason,
		"cron_expression": task.CronExpression,
		"recurring":       task.Recurring,
		"fired_at":        task.LastFireAt.Format(time.RFC3339),
	}
	eventbus.Default.Publish(eventbus.Event{
		Kind:      "cron_fired",
		SessionID: task.SessionID,
		Payload:   eventbus.MarshalJSONPayload(payload),
		Priority:  eventbus.PriorityNext,
	})
}

type ScheduleWakeupParams struct {
	DelaySeconds   int    `json:"delay_seconds,omitempty" description:"Seconds from now to wake the agent once (5..86400). Ignored if cron_expression is set."`
	CronExpression string `json:"cron_expression,omitempty" description:"Standard 5-field cron expression for a recurring wake-up. Wins over delay_seconds. Auto-expires after 7 days."`
	Reason         string `json:"reason" description:"What to do or check when woken (concrete, e.g. 're-check CI run 123')"`
}

type ScheduleWakeupResponseMetadata struct {
	DelaySeconds   int       `json:"delay_seconds,omitempty"`
	CronExpression string    `json:"cron_expression,omitempty"`
	Reason         string    `json:"reason"`
	TaskID         string    `json:"task_id"`
	NextFireAt     time.Time `json:"next_fire_at"`
	Recurring      bool      `json:"recurring"`
}

// NewScheduleWakeupTool returns the tool wired to a per-workspace DataDirectory
// where recurring cron tasks survive process restart. dataDir may be empty in
// tests; in that case persistence is skipped but in-memory scheduling still
// works.
func NewScheduleWakeupTool(dataDir string) fantasy.AgentTool {
	startScheduler(dataDir)
	return fantasy.NewAgentTool(
		ScheduleWakeupToolName,
		scheduleWakeupDescription,
		func(ctx context.Context, params ScheduleWakeupParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Reason == "" {
				return fantasy.NewTextErrorResponse("missing reason"), nil
			}
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.NewTextErrorResponse("schedule_wakeup requires an active session"), nil
			}

			if params.CronExpression != "" {
				task, err := defaultScheduler.addCronTask(sessionID, params.Reason, params.CronExpression)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid cron_expression: %v", err)), nil
				}
				metadata := ScheduleWakeupResponseMetadata{
					CronExpression: params.CronExpression,
					Reason:         params.Reason,
					TaskID:         task.ID,
					NextFireAt:     task.NextFireAt,
					Recurring:      true,
				}
				response := fmt.Sprintf(
					"Scheduled a recurring wake-up (%s). First fire at %s UTC. Task auto-expires in 7 days. Reason: %s. This turn will end now; you'll be automatically continued each time it fires.",
					params.CronExpression, task.NextFireAt.UTC().Format(time.RFC3339), params.Reason)
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
			}

			delay := params.DelaySeconds
			if delay < MinWakeupSeconds {
				delay = MinWakeupSeconds
			}
			if delay > MaxWakeupSeconds {
				delay = MaxWakeupSeconds
			}
			task := defaultScheduler.addDelayTask(sessionID, params.Reason, time.Duration(delay)*time.Second)
			metadata := ScheduleWakeupResponseMetadata{
				DelaySeconds: delay,
				Reason:       params.Reason,
				TaskID:       task.ID,
				NextFireAt:   task.NextFireAt,
				Recurring:    false,
			}
			response := fmt.Sprintf(
				"Scheduled a wake-up in %ds. This turn will end now; you'll be automatically continued then. Reason: %s. Do not poll.",
				delay, params.Reason)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		},
	)
}
