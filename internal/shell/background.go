package shell

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/pubsub"
)

const (
	// MaxBackgroundJobs is the maximum number of concurrent background jobs allowed
	MaxBackgroundJobs = 50
	// CompletedJobRetentionMinutes is how long to keep completed jobs before auto-cleanup (8 hours)
	CompletedJobRetentionMinutes = 8 * 60
	// maxBackgroundTailBytes bounds how much trailing output a completion event
	// carries, so a follow-up agent run gets enough context without dragging a
	// huge log into the prompt.
	maxBackgroundTailBytes = 4096
)

// BackgroundEventKind classifies why a background event fired.
type BackgroundEventKind string

const (
	// BackgroundKindDone — a backgrounded job finished on its own.
	BackgroundKindDone BackgroundEventKind = "done"
	// BackgroundKindMonitorHit — a monitor's pattern matched the job output.
	BackgroundKindMonitorHit BackgroundEventKind = "monitor_hit"
	// BackgroundKindMonitorEOF — the monitored job ended before its pattern appeared.
	BackgroundKindMonitorEOF BackgroundEventKind = "monitor_eof"
	// BackgroundKindMonitorTimeout — a monitor expired without matching.
	BackgroundKindMonitorTimeout BackgroundEventKind = "monitor_timeout"
)

// BackgroundJobEvent is the wake-up signal that lets the agent loop continue a
// turn that earlier detached work (a long command, or a monitor watching a
// running job) instead of blocking on it. Kind says why it fired. Only jobs
// handed back to the agent as background jobs emit BackgroundKindDone; the
// monitor kinds fire from an explicit [StartMonitor] watch.
type BackgroundJobEvent struct {
	Kind        BackgroundEventKind
	ID          string
	SessionID   string
	Command     string
	Description string
	ExitCode    int
	Interrupted bool
	OutputTail  string
	// Monitor-only fields.
	Pattern   string
	MatchLine string
}

var backgroundBroker = pubsub.NewBroker[BackgroundJobEvent]()

// SubscribeBackgroundJobs returns a channel of background-job completion events.
// The agent coordinator subscribes to drive event-driven re-wakeups; the UI
// subscribes to surface "background job finished" notices.
func SubscribeBackgroundJobs(ctx context.Context) <-chan pubsub.Event[BackgroundJobEvent] {
	return backgroundBroker.Subscribe(ctx)
}

// syncBuffer is a thread-safe wrapper around bytes.Buffer.
type syncBuffer struct {
	buf bytes.Buffer
	mu  sync.RWMutex
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) WriteString(s string) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.WriteString(s)
}

func (sb *syncBuffer) String() string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.buf.String()
}

// BackgroundShell represents a shell running in the background.
type BackgroundShell struct {
	ID          string
	Command     string
	Description string
	SessionID   string
	Shell       *Shell
	WorkingDir  string
	ctx         context.Context
	cancel      context.CancelFunc
	stdout      *syncBuffer
	stderr      *syncBuffer
	done        chan struct{}
	exitErr     error
	completedAt atomic.Int64 // Unix timestamp when job completed (0 if still running)
	backgrounded atomic.Bool // true once handed back to the agent as a background job
	notifyOnce   sync.Once   // guards single completion event publish
}

// BackgroundShellManager manages background shell instances.
type BackgroundShellManager struct {
	shells *csync.Map[string, *BackgroundShell]
}

// idCounter assigns globally-unique background job IDs across all managers.
// Each App owns its own [BackgroundShellManager] (per workspace), but keeping
// the counter package-global means IDs never collide between workspaces, so a
// job ID is unambiguous in logs and cross-workspace events.
var idCounter atomic.Uint64

// NewBackgroundShellManager creates a BackgroundShellManager. Each App (one
// per workspace/cwd) owns exactly one, so List/KillAll/KillBySession are
// naturally scoped to that workspace's jobs and never reach across workspaces.
func NewBackgroundShellManager() *BackgroundShellManager {
	return &BackgroundShellManager{
		shells: csync.NewMap[string, *BackgroundShell](),
	}
}

// Start creates and starts a new background shell with the given command.
// sessionID ties the job back to the conversation that launched it so a
// completion event can wake the right session; pass "" if unknown.
func (m *BackgroundShellManager) Start(ctx context.Context, workingDir string, blockFuncs []BlockFunc, command string, description string, sessionID string) (*BackgroundShell, error) {
	// Check job limit
	if m.shells.Len() >= MaxBackgroundJobs {
		return nil, fmt.Errorf("maximum number of background jobs (%d) reached. Please terminate or wait for some jobs to complete", MaxBackgroundJobs)
	}

	id := fmt.Sprintf("%03X", idCounter.Add(1))

	shell := NewShell(&Options{
		WorkingDir: workingDir,
		BlockFuncs: blockFuncs,
	})

	shellCtx, cancel := context.WithCancel(ctx)

	bgShell := &BackgroundShell{
		ID:          id,
		Command:     command,
		Description: description,
		SessionID:   sessionID,
		WorkingDir:  workingDir,
		Shell:       shell,
		ctx:         shellCtx,
		cancel:      cancel,
		stdout:      &syncBuffer{},
		stderr:      &syncBuffer{},
		done:        make(chan struct{}),
	}

	m.shells.Set(id, bgShell)

	go func() {
		defer close(bgShell.done)

		err := shell.ExecStream(shellCtx, command, bgShell.stdout, bgShell.stderr)

		bgShell.exitErr = err
		bgShell.completedAt.Store(time.Now().Unix())
		// If the job was already handed back to the agent as a background job,
		// fire the wake-up now. If it completed before that decision, the flag
		// is false and bash.go's synchronous path handles the result instead.
		bgShell.maybePublishDone()
	}()

	return bgShell, nil
}

// MarkBackgrounded records that this job was returned to the agent as a
// background job (rather than completing inline). It must be called by the
// caller at the moment it hands the job back, so completion can later wake the
// agent. If the job already finished, the event fires immediately.
func (bs *BackgroundShell) MarkBackgrounded() {
	bs.backgrounded.Store(true)
	bs.maybePublishDone()
}

// maybePublishDone publishes the completion event exactly once, but only for a
// job that was both handed back as a background job and has finished.
func (bs *BackgroundShell) maybePublishDone() {
	if !bs.backgrounded.Load() || bs.completedAt.Load() == 0 {
		return
	}
	bs.notifyOnce.Do(func() {
		backgroundBroker.Publish(pubsub.CreatedEvent, BackgroundJobEvent{
			Kind:        BackgroundKindDone,
			ID:          bs.ID,
			SessionID:   bs.SessionID,
			Command:     bs.Command,
			Description: bs.Description,
			ExitCode:    ExitCode(bs.exitErr),
			Interrupted: IsInterrupt(bs.exitErr),
			OutputTail:  backgroundOutputTail(bs.stdout.String(), bs.stderr.String()),
		})
	})
}

// StartMonitor watches a running background job's output and fires a single
// wake-up event when: a new line matches pattern (BackgroundKindMonitorHit),
// the job ends before matching (BackgroundKindMonitorEOF), or timeout elapses
// (BackgroundKindMonitorTimeout). It returns immediately; the watch runs in a
// goroutine. sessionID ties the wake-up back to the calling conversation.
func (m *BackgroundShellManager) StartMonitor(shellID, pattern string, timeout time.Duration, sessionID string) error {
	bgShell, ok := m.shells.Get(shellID)
	if !ok {
		return fmt.Errorf("StartMonitor: background shell not found: %s", shellID)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("StartMonitor: invalid pattern %q: %w", pattern, err)
	}

	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.After(timeout)
		base := BackgroundJobEvent{
			ID:        shellID,
			SessionID: sessionID,
			Command:   bgShell.Command,
			Pattern:   pattern,
		}
		for {
			select {
			case <-deadline:
				ev := base
				ev.Kind = BackgroundKindMonitorTimeout
				ev.OutputTail = backgroundOutputTail(bgShell.stdout.String(), bgShell.stderr.String())
				backgroundBroker.Publish(pubsub.CreatedEvent, ev)
				return
			case <-ticker.C:
				stdout, stderr, done, exitErr := bgShell.GetOutput()
				if line, matched := firstMatchingLine(re, stdout, stderr); matched {
					ev := base
					ev.Kind = BackgroundKindMonitorHit
					ev.MatchLine = line
					ev.OutputTail = backgroundOutputTail(stdout, stderr)
					backgroundBroker.Publish(pubsub.CreatedEvent, ev)
					return
				}
				if done {
					ev := base
					ev.Kind = BackgroundKindMonitorEOF
					ev.ExitCode = ExitCode(exitErr)
					ev.Interrupted = IsInterrupt(exitErr)
					ev.OutputTail = backgroundOutputTail(stdout, stderr)
					backgroundBroker.Publish(pubsub.CreatedEvent, ev)
					return
				}
			}
		}
	}()
	return nil
}

// firstMatchingLine returns the first line across stdout then stderr that
// matches re, and whether any matched.
func firstMatchingLine(re *regexp.Regexp, stdout, stderr string) (string, bool) {
	for _, block := range []string{stdout, stderr} {
		for _, line := range strings.Split(block, "\n") {
			if re.MatchString(line) {
				return line, true
			}
		}
	}
	return "", false
}

// backgroundOutputTail returns the trailing slice of combined stdout+stderr,
// capped at maxBackgroundTailBytes, for embedding in a wake-up prompt.
func backgroundOutputTail(stdout, stderr string) string {
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}
	if len(combined) > maxBackgroundTailBytes {
		combined = "…(truncated)…\n" + combined[len(combined)-maxBackgroundTailBytes:]
	}
	return combined
}

// Get retrieves a background shell by ID.
func (m *BackgroundShellManager) Get(id string) (*BackgroundShell, bool) {
	return m.shells.Get(id)
}

// Remove removes a background shell from the manager without terminating it.
// This is useful when a shell has already completed and you just want to clean up tracking.
func (m *BackgroundShellManager) Remove(id string) error {
	_, ok := m.shells.Take(id)
	if !ok {
		return fmt.Errorf("background shell not found: %s", id)
	}
	return nil
}

// Kill terminates a background shell by ID.
func (m *BackgroundShellManager) Kill(id string) error {
	shell, ok := m.shells.Take(id)
	if !ok {
		return fmt.Errorf("background shell not found: %s", id)
	}

	shell.cancel()
	<-shell.done
	return nil
}

// KillBySession terminates every background shell launched by the given
// session and returns how many were killed. It is the reaping primitive
// behind client-disconnect cleanup: when a session's controlling client goes
// away, its in-flight background jobs (and their whole process subtrees, via
// the process-group exec handler) are torn down rather than orphaned. Jobs
// belonging to other sessions in the same workspace are untouched.
func (m *BackgroundShellManager) KillBySession(sessionID string) int {
	if sessionID == "" {
		return 0
	}
	var victims []*BackgroundShell
	for shell := range m.shells.Seq() {
		if shell.SessionID == sessionID {
			victims = append(victims, shell)
		}
	}
	for _, shell := range victims {
		m.shells.Take(shell.ID)
		shell.cancel()
		<-shell.done
	}
	return len(victims)
}

// BackgroundShellInfo contains information about a background shell.
type BackgroundShellInfo struct {
	ID          string
	Command     string
	Description string
}

// List returns all background shell IDs.
func (m *BackgroundShellManager) List() []string {
	ids := make([]string, 0, m.shells.Len())
	for id := range m.shells.Seq2() {
		ids = append(ids, id)
	}
	return ids
}

// Cleanup removes completed jobs that have been finished for more than the retention period
func (m *BackgroundShellManager) Cleanup() int {
	now := time.Now().Unix()
	retentionSeconds := int64(CompletedJobRetentionMinutes * 60)

	var toRemove []string
	for shell := range m.shells.Seq() {
		completedAt := shell.completedAt.Load()
		if completedAt > 0 && now-completedAt > retentionSeconds {
			toRemove = append(toRemove, shell.ID)
		}
	}

	for _, id := range toRemove {
		m.Remove(id)
	}

	return len(toRemove)
}

// KillAll terminates all background shells. The provided context bounds how
// long the function waits for each shell to exit.
func (m *BackgroundShellManager) KillAll(ctx context.Context) {
	shells := slices.Collect(m.shells.Seq())
	m.shells.Reset(map[string]*BackgroundShell{})

	var wg sync.WaitGroup
	for _, shell := range shells {
		wg.Go(func() {
			shell.cancel()
			select {
			case <-shell.done:
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
}

// GetOutput returns the current output of a background shell.
func (bs *BackgroundShell) GetOutput() (stdout string, stderr string, done bool, err error) {
	select {
	case <-bs.done:
		return bs.stdout.String(), bs.stderr.String(), true, bs.exitErr
	default:
		return bs.stdout.String(), bs.stderr.String(), false, nil
	}
}

// IsDone checks if the background shell has finished execution.
func (bs *BackgroundShell) IsDone() bool {
	select {
	case <-bs.done:
		return true
	default:
		return false
	}
}

// Wait blocks until the background shell completes.
func (bs *BackgroundShell) Wait() {
	<-bs.done
}

func (bs *BackgroundShell) WaitContext(ctx context.Context) bool {
	select {
	case <-bs.done:
		return true
	case <-ctx.Done():
		return false
	}
}
