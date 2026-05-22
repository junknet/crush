package backend

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// alive reports whether pid is a live process.
func alive(pid int) bool {
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

// waitGrandchildPid blocks until the background job records its forked
// grandchild's pid to pidFile, then returns it.
func waitGrandchildPid(t *testing.T, pidFile string) int {
	t.Helper()
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && p > 0 {
				return p
			}
		}
	}
	t.Fatal("background job never recorded its grandchild pid")
	return 0
}

// TestClientDisconnectReapsWorkspaceJobs exercises the full Fix C path with
// real components: a real Backend, a real per-workspace App (real DB/config),
// and a real background job whose shell forks a grandchild process. When the
// last client of the workspace disconnects, the job and its whole subtree must
// be killed, while the workspace App survives for reconnect.
func TestClientDisconnectReapsWorkspaceJobs(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()

	bcfg, err := config.Init(workDir, dataDir, false)
	require.NoError(t, err)

	b := New(context.Background(), bcfg, func() {})
	ws, _, err := b.CreateWorkspace(proto.Workspace{Path: workDir, DataDir: dataDir})
	require.NoError(t, err)
	require.NotNil(t, ws.BackgroundShells)

	// Start a real background job that forks a sleep grandchild and records
	// the grandchild's pid. This is the same shape as an agent-launched bash
	// background job, minus the LLM turn that triggers it.
	pidFile := workDir + "/gc.pid"
	_, err = ws.BackgroundShells.Start(context.Background(), workDir, nil,
		"sh -c 'sleep 600 & echo $! > "+pidFile+"; wait'", "", "session-1")
	require.NoError(t, err)

	gcPid := waitGrandchildPid(t, pidFile)
	require.True(t, alive(gcPid), "grandchild should be running before disconnect")

	// One client attaches then detaches: the count returns to zero, which is
	// the "no client is watching this workspace" signal that triggers reaping.
	b.ClientConnected(ws.ID)
	b.ClientDisconnected(ws.ID)

	// Group-kill grace is 2s; allow margin then assert the subtree is gone.
	time.Sleep(3 * time.Second)
	require.False(t, alive(gcPid), "job subtree must be reaped after last client disconnects")

	// The workspace App must survive the reap so a client can reconnect.
	_, err = b.GetWorkspace(ws.ID)
	require.NoError(t, err, "workspace must survive reaping")
}

// TestClientDisconnectKeepsJobsWhileWatched verifies reaping only fires when
// the client count actually reaches zero: with one client still attached, a
// transient disconnect of another must not kill running jobs.
func TestClientDisconnectKeepsJobsWhileWatched(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()

	bcfg, err := config.Init(workDir, dataDir, false)
	require.NoError(t, err)

	b := New(context.Background(), bcfg, func() {})
	ws, _, err := b.CreateWorkspace(proto.Workspace{Path: workDir, DataDir: dataDir})
	require.NoError(t, err)

	pidFile := workDir + "/gc.pid"
	_, err = ws.BackgroundShells.Start(context.Background(), workDir, nil,
		"sh -c 'sleep 600 & echo $! > "+pidFile+"; wait'", "", "session-1")
	require.NoError(t, err)
	gcPid := waitGrandchildPid(t, pidFile)

	// Two clients attach; one leaves. Count is still 1 → no reap.
	b.ClientConnected(ws.ID)
	b.ClientConnected(ws.ID)
	b.ClientDisconnected(ws.ID)

	time.Sleep(1 * time.Second)
	require.True(t, alive(gcPid), "job must keep running while a client is still watching")

	// Last client leaves → reap.
	b.ClientDisconnected(ws.ID)
	time.Sleep(3 * time.Second)
	require.False(t, alive(gcPid), "job must be reaped once the last client leaves")
}

// startWorkspaceJob creates a workspace and starts one background job that
// records its grandchild pid; returns the workspace and that pid.
func startWorkspaceJob(t *testing.T, b *Backend) (*Workspace, int) {
	t.Helper()
	workDir := t.TempDir()
	dataDir := t.TempDir()
	ws, _, err := b.CreateWorkspace(proto.Workspace{Path: workDir, DataDir: dataDir})
	require.NoError(t, err)
	pidFile := workDir + "/gc.pid"
	_, err = ws.BackgroundShells.Start(context.Background(), workDir, nil,
		"sh -c 'sleep 600 & echo $! > "+pidFile+"; wait'", "", "session-1")
	require.NoError(t, err)
	return ws, waitGrandchildPid(t, pidFile)
}

// TestReapIsolatedPerWorkspace proves Fix B's per-workspace scoping: reaping
// one workspace (its own BackgroundShellManager) never touches jobs in another
// workspace. Different cwds are fully isolated.
func TestReapIsolatedPerWorkspace(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()
	bcfg, err := config.Init(workDir, dataDir, false)
	require.NoError(t, err)
	b := New(context.Background(), bcfg, func() {})

	wsA, pidA := startWorkspaceJob(t, b)
	wsB, pidB := startWorkspaceJob(t, b)
	require.NotEqual(t, wsA.ID, wsB.ID)
	require.True(t, alive(pidA) && alive(pidB))

	// Reap workspace A only.
	b.ClientConnected(wsA.ID)
	b.ClientDisconnected(wsA.ID)

	time.Sleep(3 * time.Second)
	require.False(t, alive(pidA), "workspace A job must be reaped")
	require.True(t, alive(pidB), "workspace B job must be untouched by A's reap")

	b.ClientConnected(wsB.ID)
	b.ClientDisconnected(wsB.ID) // cleanup
}
