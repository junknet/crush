package iodriver

import "context"

// ExecRequest is a run-to-completion command for a remote backend. Each request
// runs independently with the given working directory; a `cd` inside Command
// does not persist to the next request — matching how the local bash tool runs
// each invocation in its working dir.
type ExecRequest struct {
	// Command runs through `sh -c` (shell features: pipes, redirection). Used
	// by the bash tool.
	Command string
	// Argv, when non-empty, runs the program directly WITHOUT a shell. Used by
	// structured tools (grep/find) so a pattern with shell metacharacters needs
	// no quoting and cannot be reinterpreted by the shell. Takes precedence
	// over Command.
	Argv []string
	Cwd  string
	Env  []string
}

// ExecResult is the outcome of a finished remote command.
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Execer is the exec face of a backend, kept as a SEPARATE interface (not part
// of Backend) so the LocalBackend need not reimplement the rich local shell
// (BackgroundShellManager) it already has: the bash/rg tools type-assert the
// active backend for Execer and route to the remote daemon when present, else
// fall back to the existing local path. Only RemoteBackend implements it.
//
// This is a synchronous run-to-completion call; persistent/streaming remote
// sessions (background jobs, reattach after disconnect) layer on in a later
// stage.
type Execer interface {
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
}
