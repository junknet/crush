package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// newPostRunner builds an event-routed Runner with cmd wired to the
// PostToolUse slot. The Pre slot stays empty so a PreToolUse Run() call
// from hookedTool is a no-op and the test isolates post-hook behaviour.
func newPostRunner(t *testing.T, cmd string) *hooks.Runner {
	t.Helper()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventPostToolUse: {{Command: cmd}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	return hooks.NewEventRunner(cfg.Hooks, t.TempDir(), t.TempDir())
}

func TestHookedTool_PostToolUse_AppendsContext(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "view", resp: fantasy.NewTextResponse("file body")}
	runner := newPostRunner(t, `echo '{"context":"reviewed by hook"}'`)
	tool := newHookedTool(inner, runner)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "view", Input: "{}"})
	require.NoError(t, err)
	require.True(t, inner.called)
	require.Contains(t, resp.Content, "file body")
	require.Contains(t, resp.Content, "reviewed by hook")
}

func TestHookedTool_PostToolUse_HaltStopsTurn(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("done")}
	runner := newPostRunner(t, `echo '{"halt":true,"reason":"policy"}'`)
	tool := newHookedTool(inner, runner)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c2", Name: "bash", Input: "{}"})
	require.NoError(t, err)
	require.True(t, inner.called, "post hook cannot un-run the tool")
	require.True(t, resp.StopTurn, "halt must promote to StopTurn")
}

func TestHookedTool_PostToolUse_MetadataIsolated(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "view", resp: fantasy.NewTextResponse("body")}
	runner := newPostRunner(t, `echo '{"context":"audited"}'`)
	tool := newHookedTool(inner, runner)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c3", Name: "view", Input: "{}"})
	require.NoError(t, err)
	// Post-hook metadata must live under hook.post so it cannot trample any
	// pre-hook metadata under hook.
	require.True(t, strings.Contains(resp.Metadata, `"post"`),
		"expected hook.post key in metadata; got %q", resp.Metadata)
}

func TestHookedTool_PostToolUse_RunsOnError(t *testing.T) {
	t.Parallel()

	// Inner tool returns an error response with no Go error; PostToolUse
	// should still get a chance to observe the failure.
	inner := &fakeTool{name: "bash", resp: fantasy.NewTextErrorResponse("nope")}
	runner := newPostRunner(t, `echo '{"context":"saw failure"}'`)
	tool := newHookedTool(inner, runner)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c4", Name: "bash", Input: "{}"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "saw failure")
}
