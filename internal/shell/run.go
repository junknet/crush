package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"mvdan.cc/sh/moreinterp/coreutils"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// RunOptions configures a single stateless shell execution via [Run].
//
// The zero value is not useful; at minimum Command must be set. Stdin,
// Stdout, and Stderr may be nil (nil readers/writers are treated as
// empty/discard). BlockFuncs may be nil to disable block-list enforcement —
// hooks use this to run user-authored commands with the same trust level as
// a shell alias.
type RunOptions struct {
	// Command is the shell source to parse and execute.
	Command string
	// Cwd is the working directory for the execution. Required: callers
	// must supply a non-empty value. Run does not silently fall back to
	// the Crush process cwd — hooks and the bash tool have different
	// notions of "default" and each owns that decision.
	Cwd string
	// Env is the full environment visible to the command. The caller is
	// responsible for inheriting from os.Environ() if that's desired.
	Env []string
	// Stdin is the command's standard input. nil is equivalent to an empty
	// input stream.
	Stdin io.Reader
	// Stdout receives the command's standard output. nil discards output.
	Stdout io.Writer
	// Stderr receives the command's standard error. nil discards output.
	Stderr io.Writer
	// BlockFuncs is an optional list of deny-list matchers applied before
	// each command reaches the exec layer. nil disables blocking entirely.
	BlockFuncs []BlockFunc
}

// Run parses and executes a shell command using the same mvdan.cc/sh
// interpreter stack that the stateful [Shell] type uses (builtins,
// optional block list, optional Go coreutils). It is safe to call
// concurrently from multiple goroutines: each call builds its own
// [interp.Runner] and shares no state with other callers or with any
// [Shell] instance.
//
// Errors returned from the command itself (non-zero exit, context
// cancellation, parse failures) follow the same conventions as
// [Shell.Exec]: inspect with [IsInterrupt] and [ExitCode].
func Run(ctx context.Context, opts RunOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("command execution panic: %v", r)
		}
	}()

	if opts.Cwd == "" {
		return fmt.Errorf("shell.Run: Cwd is required")
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	line, err := syntax.NewParser().Parse(strings.NewReader(opts.Command), "")
	if err != nil {
		return fmt.Errorf("could not parse command: %w", err)
	}

	stdin := opts.Stdin
	if stdin == nil {
		devNull, oerr := os.Open(os.DevNull)
		if oerr == nil {
			stdin = devNull
			defer devNull.Close()
		} else {
			stdin = strings.NewReader("")
		}
	}

	runner, err := newRunner(opts.Cwd, opts.Env, stdin, stdout, stderr, opts.BlockFuncs)
	if err != nil {
		return fmt.Errorf("could not run command: %w", err)
	}

	start := time.Now()
	slog.Debug("Shell command started", "command", opts.Command, "cwd", opts.Cwd)
	err = runner.Run(ctx, line)
	var exitCode int
	var exitStatus interp.ExitStatus
	if errors.As(err, &exitStatus) {
		exitCode = int(exitStatus)
	} else if err != nil {
		exitCode = -1
	}
	slog.Debug("Shell command finished",
		"command", opts.Command,
		"cwd", opts.Cwd,
		"exit_code", exitCode,
		"duration_ms", time.Since(start).Milliseconds())
	return err
}

// newRunner constructs an [interp.Runner] configured with the standard
// Crush handler stack. Shared by the stateless [Run] entrypoint and the
// stateful [Shell] so the two surfaces cannot drift.
func newRunner(cwd string, env []string, stdin io.Reader, stdout, stderr io.Writer, blockFuncs []BlockFunc) (*interp.Runner, error) {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	return interp.New(
		interp.StdIO(stdin, stdout, stderr),
		interp.Interactive(false),
		interp.Env(expand.ListEnviron(env...)),
		interp.Dir(cwd),
		interp.CallHandler(rewriteUnsupportedBuiltins),
		interp.ExecHandlers(standardHandlers(blockFuncs)...),
	)
}

// unsupportedBuiltins lists names that mvdan/sh classifies as builtins via
// [interp.IsBuiltin] but does not actually implement, causing the interpreter
// to bail with "unsupported builtin". They are all standard system utilities
// with no state inside the Crush shell, so rewriting them to an absolute path
// via PATH lookup makes the interpreter route them through the exec handler
// chain (PATH binaries) instead of the builtin dispatcher.
//
// Job-control builtins (bg/fg/jobs/wait/disown) are deliberately left out:
// their state lives inside the Runner, so a PATH binary cannot stand in.
var unsupportedBuiltins = map[string]bool{
	"kill":    true,
	"umask":   true,
	"fc":      true,
	"pushd":   true,
	"popd":    true,
	"dirs":    true,
	"history": true,
	"suspend": true,
	"newgrp":  true,
}

// rewriteUnsupportedBuiltins is the [interp.CallHandlerFunc] that forces
// known unsupported builtins onto the exec path by replacing the bare name
// with its absolute PATH location. If lookup fails we leave args unchanged
// so the original (clearer) "executable file not found" error surfaces.
func rewriteUnsupportedBuiltins(_ context.Context, args []string) ([]string, error) {
	if len(args) == 0 || !unsupportedBuiltins[args[0]] {
		return args, nil
	}
	abs, err := exec.LookPath(args[0])
	if err != nil {
		return args, nil
	}
	out := make([]string, len(args))
	copy(out, args)
	out[0] = abs
	return out, nil
}

// standardHandlers returns the exec-handler middleware chain used by both
// [Run] and [Shell]. Order matters:
//  1. builtins first (so Crush's in-process jq wins over any PATH binary);
//  2. script dispatch (shebang / binary / shell-source for path-prefixed
//     argv[0], no-op for bare commands) — runs before the block list so
//     that deny rules see the already-resolved argv of anything the
//     script exec's rather than the outer path-prefixed wrapper;
//  3. block list;
//  4. optional Go coreutils (only when useGoCoreUtils is on).
func standardHandlers(blockFuncs []BlockFunc) []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	handlers := []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc{
		builtinHandler(),
		scriptDispatchHandler(blockFuncs),
		blockHandler(blockFuncs),
	}
	if useGoCoreUtils {
		handlers = append(handlers, coreutils.ExecHandler)
	}
	// Terminal handler: spawns the real process. On unix it isolates each
	// command in its own process group so cancellation kills the whole
	// subtree (no orphaned grandchildren holding locks); on windows it is
	// empty and the chain falls through to mvdan's default exec.
	handlers = append(handlers, terminalExecHandlers()...)
	return handlers
}

// builtinHandler returns middleware that dispatches recognized Crush
// builtins to their in-process Go implementations. Currently: jq.
func builtinHandler() func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			switch args[0] {
			case "jq":
				hc := interp.HandlerCtx(ctx)
				return handleJQ(ctx, args, hc.Stdin, hc.Stdout, hc.Stderr)
			default:
				return next(ctx, args)
			}
		}
	}
}

// blockHandler returns middleware that rejects commands matched by any of
// the provided [BlockFunc]s before they reach the underlying exec path.
// A nil or empty blockFuncs slice is a no-op.
func blockHandler(blockFuncs []BlockFunc) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			for _, blockFunc := range blockFuncs {
				if blockFunc(args) {
					return fmt.Errorf("command is not allowed for security reasons: %q", args[0])
				}
			}
			return next(ctx, args)
		}
	}
}
