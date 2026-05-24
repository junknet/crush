//go:build !windows

package shell

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// groupKillGrace is how long we wait after SIGTERM-ing a spawned process
// group before escalating to SIGKILL. It mirrors mvdan's default killTimeout
// (2s): long enough for a well-behaved tool to flush and exit on its own,
// short enough that a cancelled agent turn frees build locks promptly.
const groupKillGrace = 2 * time.Second

// terminalExecHandlers returns the bottom of the exec-handler chain. On
// unix it is a single handler that spawns each command in its OWN process
// group so cancellation tears down the entire subtree (see
// [groupExecHandler]). Returning a slice lets the windows build contribute
// nothing and fall through to mvdan's default exec instead.
func terminalExecHandlers() []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc{groupExecHandler}
}

// groupExecHandler is the terminal exec handler. Unlike mvdan's
// DefaultExecHandler it places each spawned command in its own detached
// session (Setsid) and, when ctx is cancelled, signals the entire group via
// kill(-pgid, …) rather than only the direct child. This guarantees that
// cancelling an agent turn or a background job kills the whole process
// subtree — e.g. `nimony c` together with every compiler subprocess it
// forked — so no orphan keeps holding a build lock after cancellation.
//
// It ignores next: it sits at the bottom of the chain and performs the real
// spawn, replacing mvdan's built-in exec.
func groupExecHandler(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return interp.ExitStatus(127)
		}
		cmd := &exec.Cmd{
			Path:   path,
			Args:   args,
			Env:    execEnvList(hc.Env),
			Dir:    hc.Dir,
			Stdin:  hc.Stdin,
			Stdout: hc.Stdout,
			Stderr: hc.Stderr,
			// Setsid (new session, NO controlling terminal) rather than just
			// Setpgid: a spawned command must never be able to touch crush's
			// controlling tty. Tools like valgrind/callgrind tcsetpgrp the
			// terminal foreground to the program they run; with a shared
			// session that steals the foreground from crush's TUI, and crush's
			// next tty read raises SIGTTIN and suspends the whole app
			// ("zsh: suspended (tty input)"). A detached session can't do that.
			SysProcAttr: &syscall.SysProcAttr{Setsid: true},
		}
		return runInProcessGroup(ctx, cmd, hc.Stderr)
	}
}

// runInProcessGroup starts cmd (which the caller has already configured with
// Setsid) and waits for it. On ctx cancellation it SIGTERMs the whole
// process group led by the child, waits [groupKillGrace], then SIGKILLs the
// group. Error classification matches mvdan's default handler so callers see
// the same [interp.ExitStatus] semantics. stderr receives spawn failures.
func runInProcessGroup(ctx context.Context, cmd *exec.Cmd, stderr io.Writer) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// New session: own process group AND detached from the controlling
	// terminal, so the command can never steal the tty foreground and SIGTTIN
	// crush. setsid makes the child the leader of a new group whose pgid is its
	// pid, so the kill(-pgid) below still reaps the whole subtree.
	cmd.SysProcAttr.Setsid = true

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return interp.ExitStatus(127)
	}

	// With Setsid the child is its own session/group leader, so its pid is the
	// pgid; a negative pid addresses the whole group.
	pgid := cmd.Process.Pid
	stop := context.AfterFunc(ctx, func() {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		time.Sleep(groupKillGrace)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
	defer stop()

	return classifyExecErr(ctx, stderr, cmd.Wait())
}
