package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/shell"
)

func newLoopTestCoordinator() *coordinator {
	return &coordinator{
		bgLoopFP: make(map[string]string),
		bgLoopN:  make(map[string]int),
	}
}

// A failing job re-monitored forever produces a near-identical wake every turn
// (only the durations jitter). The fingerprint must collapse those and the
// counter must climb past the suppression threshold.
func TestBackgroundWakeLoopBreaker_TripsOnSimilarContent(t *testing.T) {
	c := newLoopTestCoordinator()
	mk := func(ms string) shell.BackgroundJobEvent {
		return shell.BackgroundJobEvent{
			SessionID:  "s1",
			ID:         "01F",
			Command:    "verify_lsp_cache.py",
			OutputTail: "Results: macro_expand_warm : [FAIL] " + ms + "ms",
		}
	}
	// Jittery durations + even a new job ID must not defeat the fingerprint.
	got := []int{}
	for i, ms := range []string{"60000.32", "60001.11", "59999.80", "60000.00", "60002.40"} {
		ev := mk(ms)
		if i == 3 {
			ev.ID = "07A" // relaunch with a new id — still the same situation
		}
		got = append(got, c.recordBackgroundWake(ev.SessionID, backgroundWakeFingerprint(ev)))
	}
	want := []int{1, 2, 3, 4, 5}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("repeat[%d]=%d, want %d (got=%v)", i, got[i], want[i], got)
		}
	}
	if maxSimilarBackgroundWakes != 3 {
		t.Fatalf("threshold changed: %d", maxSimilarBackgroundWakes)
	}
	// repeats 1..3 wake (3 == final stop message); 4+ are suppressed.
}

// Genuinely different output (a job making progress) must reset the counter so
// real work keeps waking the agent.
func TestBackgroundWakeLoopBreaker_ResetsOnContentChange(t *testing.T) {
	c := newLoopTestCoordinator()
	mk := func(line string) shell.BackgroundJobEvent {
		return shell.BackgroundJobEvent{SessionID: "s1", ID: "01F", Command: "build.sh", OutputTail: line}
	}
	if n := c.recordBackgroundWake("s1", backgroundWakeFingerprint(mk("step A done"))); n != 1 {
		t.Fatalf("first=%d", n)
	}
	if n := c.recordBackgroundWake("s1", backgroundWakeFingerprint(mk("step A done"))); n != 2 {
		t.Fatalf("repeat=%d", n)
	}
	// Different content -> reset to 1.
	if n := c.recordBackgroundWake("s1", backgroundWakeFingerprint(mk("step B done"))); n != 1 {
		t.Fatalf("after change=%d, want reset to 1", n)
	}
}

// A model that varies its command wrapper but produces the SAME failing output
// must NOT evade the breaker (fingerprint is output-only). Genuine progress
// (output changes FAIL->PASS) must reset.
func TestBackgroundWakeLoopBreaker_OutputInvariantAcrossCommandChange(t *testing.T) {
	c := newLoopTestCoordinator()
	fail := "Results: cache_verify : [FAIL] 60000.32ms"
	// Same failure, three different command wrappers + new job IDs.
	evs := []shell.BackgroundJobEvent{
		{SessionID: "s1", ID: "001", Command: "./fail.sh", OutputTail: fail},
		{SessionID: "s1", ID: "002", Command: "bash fail.sh", OutputTail: fail},
		{SessionID: "s1", ID: "003", Command: "bash -c './fail.sh # retry'", OutputTail: fail},
	}
	for i, ev := range evs {
		if n := c.recordBackgroundWake(ev.SessionID, backgroundWakeFingerprint(ev)); n != i+1 {
			t.Fatalf("command variation reset the counter at i=%d: got %d, want %d", i, n, i+1)
		}
	}
	// Real progress: the job now PASSES -> different output -> reset.
	pass := shell.BackgroundJobEvent{SessionID: "s1", ID: "004", Command: "./fail.sh", OutputTail: "Results: cache_verify : [PASS] 1.2ms"}
	if n := c.recordBackgroundWake(pass.SessionID, backgroundWakeFingerprint(pass)); n != 1 {
		t.Fatalf("FAIL->PASS must reset, got %d", n)
	}
}

// Distinct sessions must not share loop-breaker state.
func TestBackgroundWakeLoopBreaker_SessionScoped(t *testing.T) {
	c := newLoopTestCoordinator()
	ev := shell.BackgroundJobEvent{ID: "01F", Command: "x", OutputTail: "same"}
	fp := backgroundWakeFingerprint(ev)
	if n := c.recordBackgroundWake("s1", fp); n != 1 {
		t.Fatalf("s1=%d", n)
	}
	if n := c.recordBackgroundWake("s2", fp); n != 1 {
		t.Fatalf("s2 must be independent, got %d", n)
	}
}
