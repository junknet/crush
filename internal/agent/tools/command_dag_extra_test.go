package tools

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// validate-layer guards: bad inputs must short-circuit before any command runs.

func TestCommandDAGTool_RejectsUnknownDependency(t *testing.T) {
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "unknown dep",
		Commands: []CommandDAGNode{
			{ID: "a", Command: "true", Deps: []string{"ghost"}},
		},
	})
	require.True(t, resp.IsError, "expected validation error response")
	require.Contains(t, resp.Content, `depends on unknown node "ghost"`)
}

func TestCommandDAGTool_RejectsDuplicateNodeID(t *testing.T) {
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "dup id",
		Commands: []CommandDAGNode{
			{ID: "a", Command: "true"},
			{ID: "a", Command: "true"},
		},
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, `duplicate node id "a"`)
}

func TestCommandDAGTool_RejectsEmptyCommands(t *testing.T) {
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "empty",
		Commands:    nil,
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "at least one node")
}

func TestCommandDAGTool_RejectsCyclicDependency(t *testing.T) {
	// Cycle (a → b → a) passes shallow validation (both nodes exist) but produces
	// no ready nodes and no skip-eligible nodes, so executeCommandDAG must
	// surface the cycle as an explicit error.
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "cycle",
		Commands: []CommandDAGNode{
			{ID: "a", Command: "true", Deps: []string{"b"}},
			{ID: "b", Command: "true", Deps: []string{"a"}},
		},
	})
	require.False(t, resp.IsError, "validation should pass; cycle is an execution-time signal")
	var meta CommandDAGResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Success)
	require.Contains(t, resp.Content, "dependency cycle or unresolved dependency")
}

// execution semantics

func TestCommandDAGTool_DiamondAllSucceed(t *testing.T) {
	// a → b, a → c, b → d, c → d.  d only runs after both b and c.
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "diamond",
		Commands: []CommandDAGNode{
			{ID: "a", Command: "echo a"},
			{ID: "b", Command: "echo b", Deps: []string{"a"}},
			{ID: "c", Command: "echo c", Deps: []string{"a"}},
			{ID: "d", Command: "echo d", Deps: []string{"b", "c"}},
		},
		MaxParallel: 4,
	})
	require.False(t, resp.IsError)
	var meta CommandDAGResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Success)
	require.Len(t, meta.Results, 4)
	for _, r := range meta.Results {
		require.Equal(t, "succeeded", r.Outcome, "node %s", r.ID)
		require.True(t, r.Success, "node %s", r.ID)
	}
}

func TestCommandDAGTool_TransitiveSkipPropagatesThroughChain(t *testing.T) {
	// fail → mid → tail.  tail must be skipped because mid was skipped.
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "transitive skip",
		Commands: []CommandDAGNode{
			{ID: "fail", Command: "exit 3"},
			{ID: "mid", Command: "echo mid", Deps: []string{"fail"}},
			{ID: "tail", Command: "echo tail", Deps: []string{"mid"}},
		},
	})
	require.False(t, resp.IsError)
	var meta CommandDAGResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Success)
	outcomes := map[string]string{}
	for _, r := range meta.Results {
		outcomes[r.ID] = r.Outcome
	}
	require.Equal(t, "failed", outcomes["fail"])
	require.Equal(t, "skipped_dependency_failed", outcomes["mid"])
	require.Equal(t, "skipped_dependency_failed", outcomes["tail"])
}

func TestCommandDAGTool_TimeoutMarksFailedWithExitCodeNonZero(t *testing.T) {
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "timeout",
		Commands: []CommandDAGNode{
			{ID: "slow", Command: "sleep 5", TimeoutSeconds: 1},
		},
	})
	require.False(t, resp.IsError)
	var meta CommandDAGResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Success, "timed-out command must not be reported as success")
	require.Len(t, meta.Results, 1)
	result := meta.Results[0]
	require.False(t, result.Success)
	// Outcome is either "interrupted" (if shell.IsInterrupt picks up the
	// deadline-induced kill) or "failed" (signal-killed shell). Both are
	// acceptable; what matters is that success=false and the kill landed fast.
	require.Contains(t, []string{"interrupted", "failed"}, result.Outcome)
	require.Less(t, result.DurationMs, int64(4000), "should not have waited the full 5s sleep")
}

func TestCommandDAGTool_MaxParallelLimitsConcurrency(t *testing.T) {
	// 6 sleepers, MaxParallel=2.  We assert the wall clock is at least 3*sleep
	// (ceil(6/2)) -- if the semaphore were ignored it would be ~1*sleep.
	// Per-node sleep is intentionally short to keep the test fast.
	const sleepMs = 250
	const total = 6
	const maxPar = 2

	nodes := make([]CommandDAGNode, total)
	for i := 0; i < total; i++ {
		nodes[i] = CommandDAGNode{
			ID:      "n" + itoa(i),
			Command: "sleep 0." + msFraction(sleepMs),
		}
	}

	start := time.Now()
	resp := runCommandDAGTool(t, NewCommandDAGTool(t.TempDir()), context.Background(), CommandDAGParams{
		Description: "parallel cap",
		Commands:    nodes,
		MaxParallel: maxPar,
	})
	wall := time.Since(start)

	require.False(t, resp.IsError)
	var meta CommandDAGResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Success)

	minExpected := time.Duration(total/maxPar) * time.Duration(sleepMs) * time.Millisecond
	require.GreaterOrEqual(t, wall, minExpected,
		"wall %s should be ≥ %s (n=%d, max_parallel=%d, sleep=%dms)",
		wall, minExpected, total, maxPar, sleepMs)

	// Sanity: shouldn't have taken sequential time either.
	maxExpected := time.Duration(total) * time.Duration(sleepMs) * time.Millisecond
	require.Less(t, wall, maxExpected,
		"wall %s should be < sequential %s (semaphore should overlap pairs)",
		wall, maxExpected)
}

// Sanity guard: 'rg' / 'ripgrep' alias for grep no-match semantics.
func TestCommandDAGTool_RipgrepNoMatchAlsoTreatedAsSemanticSuccess(t *testing.T) {
	cmd := "rg --quiet zzznomatch /etc/hostname"
	sem := deriveCommandSemantics(cmd, "", fakeExitErr(1))
	require.Equal(t, commandOutcomeNoMatch, sem.Outcome)
	require.True(t, sem.Success)
	require.Equal(t, 1, sem.ExitCode)
}

// --- helpers -----------------------------------------------------------------

// Unused atomic to silence -unused if helpers shift; intentionally kept.
var _ = atomic.AddInt32

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func msFraction(ms int) string {
	// turns 250 -> "25" (for "sleep 0.25"), 100 -> "10", etc.
	// Caller composes "sleep 0." + msFraction(ms).
	if ms < 1000 {
		s := itoa(ms)
		// pad-left to width 3, then trim trailing zeros to keep it short.
		for len(s) < 3 {
			s = "0" + s
		}
		// trim trailing zeros (e.g. "250" -> "25", "100" -> "1")
		for len(s) > 1 && s[len(s)-1] == '0' {
			s = s[:len(s)-1]
		}
		return s
	}
	return itoa(ms / 1000)
}

// fakeExitErr is a minimal stand-in for exec.ExitError so we can probe
// deriveCommandSemantics without spawning a subprocess.
type fakeExitError struct{ code int }

func (f *fakeExitError) Error() string { return "exit status " + itoa(f.code) }
func (f *fakeExitError) ExitCode() int { return f.code }

func fakeExitErr(code int) error { return &fakeExitError{code: code} }
