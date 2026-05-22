//go:build windows

package shell

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"mvdan.cc/sh/v3/interp"
)

// terminalExecHandlers contributes no extra handler on Windows: process-group
// teardown there needs a job object, which this build does not implement, so
// the chain falls through to mvdan's default exec handler unchanged.
func terminalExecHandlers() []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return nil
}

// runInProcessGroup on Windows runs cmd without process-group isolation. The
// shebang dispatch path shares this entry point with unix; here it simply
// starts, waits, and classifies, killing only the direct child on ctx
// cancellation (Go's default behavior for a context-bound command).
func runInProcessGroup(ctx context.Context, cmd *exec.Cmd, stderr io.Writer) error {
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return interp.ExitStatus(127)
	}
	stop := context.AfterFunc(ctx, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer stop()
	return classifyExecErr(ctx, stderr, cmd.Wait())
}
