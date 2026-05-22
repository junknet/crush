package shell

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGroupKillReapsGrandchildren proves cancelling the ctx tears down the
// whole subtree: the interpreter child AND the grandchild it forks. The
// grandchild writes its own pid to a file; after cancel we probe that exact
// pid with kill -0. Without process-group teardown the grandchild orphans
// and the probe keeps succeeding.
func TestGroupKillReapsGrandchildren(t *testing.T) {
	pidFile := t.TempDir() + "/gc.pid"
	ctx, cancel := context.WithCancel(context.Background())
	sh := NewShell(&Options{WorkingDir: "/tmp"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sh.ExecStream(ctx, "sh -c 'sleep 600 & echo $! > "+pidFile+"; wait'", nil, nil)
	}()

	var gcPid int
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && p > 0 {
				gcPid = p
				break
			}
		}
	}
	if gcPid == 0 {
		t.Fatal("grandchild never recorded its pid")
	}
	if exec.Command("kill", "-0", strconv.Itoa(gcPid)).Run() != nil {
		t.Fatalf("grandchild pid %d not alive before cancel", gcPid)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("ExecStream did not return after cancel")
	}

	time.Sleep(3 * time.Second) // group-kill grace is 2s
	if exec.Command("kill", "-0", strconv.Itoa(gcPid)).Run() == nil {
		t.Fatalf("ORPHAN: grandchild pid %d survived cancel", gcPid)
	}
}

// alive reports whether pid is still a live process.
func alive(pid int) bool {
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

// startJobWithGrandchild starts a background job under sessionID whose shell
// forks a `sleep` grandchild and records that grandchild's pid to pidFile.
// Returns the recorded grandchild pid once it appears.
func startJobWithGrandchild(t *testing.T, m *BackgroundShellManager, sessionID, pidFile string) int {
	t.Helper()
	_, err := m.Start(context.Background(), "/tmp", nil,
		"sh -c 'sleep 600 & echo $! > "+pidFile+"; wait'", "", sessionID)
	if err != nil {
		t.Fatalf("Start(%s): %v", sessionID, err)
	}
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		if b, e := os.ReadFile(pidFile); e == nil {
			if p, pe := strconv.Atoi(strings.TrimSpace(string(b))); pe == nil && p > 0 {
				return p
			}
		}
	}
	t.Fatalf("job for session %s never recorded grandchild pid", sessionID)
	return 0
}

// TestKillBySessionScopesToSession proves the disconnect-reaping primitive
// kills exactly the target session's jobs — process subtrees included — while
// jobs belonging to another session in the same manager keep running.
func TestKillBySessionReapsOnlyTargetSession(t *testing.T) {
	m := NewBackgroundShellManager()
	dir := t.TempDir()

	victimA := startJobWithGrandchild(t, m, "session-victim", dir+"/a.pid")
	victimB := startJobWithGrandchild(t, m, "session-victim", dir+"/b.pid")
	survivor := startJobWithGrandchild(t, m, "session-keep", dir+"/c.pid")

	if !alive(victimA) || !alive(victimB) || !alive(survivor) {
		t.Fatal("not all grandchildren started")
	}

	if killed := m.KillBySession("session-victim"); killed != 2 {
		t.Fatalf("KillBySession killed %d jobs, want 2", killed)
	}

	time.Sleep(3 * time.Second) // group-kill grace is 2s
	if alive(victimA) || alive(victimB) {
		t.Fatalf("ORPHAN: victim grandchildren survived (a=%v b=%v)", alive(victimA), alive(victimB))
	}
	if !alive(survivor) {
		t.Fatal("survivor grandchild was wrongly killed")
	}
	_ = m.KillBySession("session-keep") // cleanup
}
