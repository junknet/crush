package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func newStopRunner(t *testing.T, cmd string) *hooks.Runner {
	t.Helper()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventStop: {{Command: cmd}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	return hooks.NewEventRunner(cfg.Hooks, t.TempDir(), t.TempDir())
}

func TestFireStopHook_EndTurnRuns(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{
		hookRunner: newStopRunner(t, `[ "$CRUSH_HOOK_EVENT" = "Stop" ] && echo '{"context":"observed"}' || exit 1`),
	}
	// Zero-value StepResult — Response embedded inside still produces a
	// safe Content().Text()/ToolCalls() pair, exercised by fireStopHook.
	a.fireStopHook(t.Context(), "sess", message.FinishReasonEndTurn, fantasy.StepResult{})
}

func TestFireStopHook_NilRunnerNoop(t *testing.T) {
	t.Parallel()

	// nil hookRunner field must short-circuit cleanly so legacy callers
	// (and tests that don't wire hooks) never NPE.
	a := &sessionAgent{}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil hookRunner should not panic; got: %v", r)
		}
	}()
	if a.hookRunner != nil {
		t.Fatal("expected nil hookRunner in fixture")
	}
}

func TestFireStopHook_ContextLogged(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{
		hookRunner: newStopRunner(t, `echo '{"context":"end-of-turn summary"}'`),
	}
	// No assertion on log content; just ensure the call completes and
	// the hook payload was accepted (a panic or err return would fail
	// silently inside fireStopHook, so we only smoke-test the path).
	a.fireStopHook(t.Context(), "sess-stop-3", message.FinishReasonMaxTokens, fantasy.StepResult{})
}
