package tools

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// Real resume scenario: a background sub-agent finished and persisted its result
// in a PRIOR process; the current process has a FRESH (empty) manager. JobOutput
// must recover the persisted result instead of failing "not found" — the exact
// bug seen on `crush-dev -c`. A genuinely-unknown id gets a clear, actionable
// message (re-dispatch), not a cryptic one.
func TestJobOutputRecoversPersistedAgentJobAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	sessionID := "sess-1"
	require.NoError(t, PersistAgentJobResult(dataDir, sessionID, "01A", "completed", "worker bit-exact result", 0))

	mgr := shell.NewBackgroundShellManager() // fresh process — job 01A is NOT live
	tool := NewJobOutputTool(mgr, dataDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, sessionID)

	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "1", Name: "JobOutput", Input: `{"shell_id":"01A","wait":false}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "worker bit-exact result")
	require.Contains(t, resp.Content, "recovered")

	// Unknown job: no live shell, no persisted record → clear re-dispatch guidance.
	resp, err = tool.Run(ctx, fantasy.ToolCall{ID: "2", Name: "JobOutput", Input: `{"shell_id":"0FF","wait":false}`})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Re-dispatch")
}
