package runtime

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSessionSnapshotsAndEmit(t *testing.T) {
	t.Parallel()

	events := make([]tea.Msg, 0, 2)
	session := NewSession("/tmp/project", func(msg tea.Msg) {
		events = append(events, msg)
	})

	session.BindSession("session-1")
	session.SetCompactHistory([]string{"one", "two"})
	session.AppendCompactHistory("three")
	session.SetFact("mode", "write")
	session.RegisterTool("bash")
	session.SetLSPState("gopls", "ready")
	session.SetMCPState("docker", "connected")
	session.Emit(tea.Msg("task-started"))
	firstTrace := session.AppendTrace(TaskTrace{
		ConversationSessionID: "session-1",
		SessionID:             "node-1",
		NodeID:                "node-1",
		Kind:                  TraceKindTaskInput,
		Input:                 "plan the work",
	})
	secondTrace := session.AppendTrace(TaskTrace{
		ConversationSessionID: "session-1",
		SessionID:             "node-1",
		NodeID:                "node-1",
		Kind:                  TraceKindTaskOutput,
		Output:                "work complete",
	})

	require.Equal(t, "session-1", session.SessionID())
	require.Equal(t, "/tmp/project", session.RootPath())
	require.Equal(t, []string{"one", "two", "three"}, session.CompactHistory())
	require.Equal(t, "write", mustFact(t, session, "mode"))
	require.Equal(t, []string{"bash"}, session.Tools())
	require.Equal(t, map[string]string{"gopls": "ready"}, session.LSPStates())
	require.Equal(t, map[string]string{"docker": "connected"}, session.MCPStates())
	require.Equal(t, []tea.Msg{tea.Msg("task-started")}, events)
	require.Equal(t, int64(1), firstTrace.Sequence)
	require.Equal(t, int64(2), secondTrace.Sequence)
	require.True(t, firstTrace.RecordedAt.After(time.Time{}))
	require.True(t, secondTrace.RecordedAt.After(time.Time{}))
	require.Len(t, session.TraceEntries(), 2)
}

func TestRuntimeSessionCopiesAreIsolated(t *testing.T) {
	t.Parallel()

	session := NewSession("/tmp/project", nil)
	session.SetCompactHistory([]string{"one"})
	session.RegisterTool("bash")
	session.SetFact("mode", "write")
	session.ResetEphemeralState()

	history := session.CompactHistory()
	tools := session.Tools()

	require.Empty(t, history)
	require.Equal(t, []string{"bash"}, tools)
	tools[0] = "mutated"
	require.Equal(t, []string{"bash"}, session.Tools())
	require.Empty(t, session.CompactHistory())
	require.Empty(t, session.TraceEntries())
	_, ok := session.Fact("mode")
	require.False(t, ok)
}

func TestRuntimeSessionCloneForRunKeepsPersistentState(t *testing.T) {
	t.Parallel()

	base := NewSession("/tmp/project", nil)
	base.RegisterTool("bash")
	base.SetLSPState("gopls", "ready")
	base.SetMCPState("docker", "connected")
	base.SetFact("mode", "write")

	clone := base.CloneForRun("session-2")
	require.NotNil(t, clone)
	require.Equal(t, "session-2", clone.SessionID())
	require.Equal(t, "/tmp/project", clone.RootPath())
	require.Equal(t, []string{"bash"}, clone.Tools())
	require.Equal(t, map[string]string{"gopls": "ready"}, clone.LSPStates())
	require.Equal(t, map[string]string{"docker": "connected"}, clone.MCPStates())
	require.Empty(t, clone.CompactHistory())
	require.Empty(t, clone.TraceEntries())
	_, ok := clone.Fact("mode")
	require.False(t, ok)
}

func mustFact(t *testing.T, session *RuntimeSession, key string) string {
	t.Helper()
	value, ok := session.Fact(key)
	require.True(t, ok)
	return value
}
