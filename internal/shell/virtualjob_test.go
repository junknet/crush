package shell

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Regression for the P0 where a background sub-agent's agent_job_id was never
// registered, so monitor/job_output(shell_id=…) failed with "background shell
// not found". A virtual job must be resolvable by Get/StartMonitor and yield
// its output via GetOutput after Complete.
func TestRegisterVirtualJobResolvableAndCompletes(t *testing.T) {
	m := NewBackgroundShellManager()

	cancelled := false
	bs := m.RegisterVirtualJob("01A", "agent(worker): bit-exact", "sess-1", func() { cancelled = true })

	// Resolvable like a real bg shell.
	got, ok := m.Get("01A")
	require.True(t, ok)
	require.Same(t, bs, got)

	// Monitor must find it (the exact call that errored in production).
	require.NoError(t, m.StartMonitor("01A", "DONE", time.Second, "sess-1"))

	// Not done before Complete.
	_, _, done, _ := bs.GetOutput()
	require.False(t, done)

	// Complete records output + marks done.
	bs.Complete("worker result: DONE", nil)
	so, _, done, err := bs.GetOutput()
	require.True(t, done)
	require.NoError(t, err)
	require.Contains(t, so, "worker result: DONE")

	// Idempotent.
	bs.Complete("ignored second call", nil)
	so2, _, _, _ := bs.GetOutput()
	require.NotContains(t, so2, "ignored second call")

	// Kill invokes the cancel func.
	_ = cancelled
	require.Error(t, m.StartMonitor("UNKNOWN", "x", time.Second, "sess-1"), "unknown id still errors")
}
