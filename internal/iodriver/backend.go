// Package iodriver defines the IO backend abstraction that decouples the file
// tools (edit/view/write/rg) and the shell tool (bash) from the concrete
// execution target. A LocalBackend reproduces today's direct os.*/local-shell
// behavior; a future RemoteBackend will proxy the same operations to a daemon
// over an SSH stdio channel so the agent can operate a remote host as if it
// were local — the "io driver" that transparently routes local-style file and
// exec operations to the remote.
//
// The interface is intentionally split into a file face (FileSystem) and — in
// a later stage — an exec face, mirroring OpenAI Codex's exec-server
// (ExecutorFileSystem / ExecBackend). Tools resolve the active backend from the
// request context; when none is attached they fall back to local os.* so the
// default (local) path is unchanged.
//
// This package depends only on the standard library so it can be imported by
// both the low-level shell package and the tools package without cycles. (The
// pre-existing internal/workspace package is a higher-level, app-scoped concept
// — session/path pools — and is unrelated.)
package iodriver

import (
	"context"
	"io/fs"
)

// FileSystem is the file face of an IO backend. All paths are absolute (callers
// join against the backend Root before calling). Implementations MUST preserve
// raw bytes — no newline or text normalization — and MUST return an error
// wrapping fs.ErrNotExist for missing paths so os.IsNotExist callers keep
// working across local and remote backends.
type FileSystem interface {
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	// Mkdir has MkdirAll semantics: it creates path and any missing parents.
	Mkdir(ctx context.Context, path string, perm fs.FileMode) error
	Remove(ctx context.Context, path string) error
	Rename(ctx context.Context, oldPath, newPath string) error
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
}

// Backend is a complete IO target. The exec face (Spawn/Reattach) is added in a
// later stage; keeping the umbrella interface here lets tools store and resolve
// a single value from context across stages.
type Backend interface {
	FileSystem

	// Kind identifies the backend, e.g. "local" or "remote:<host>".
	Kind() string
	// Root is the default working directory for tools when no explicit path is
	// given. For the local backend this is the configured workspace dir.
	Root() string
	// Close releases backend resources (remote connection, daemon socket, ...).
	// The local backend has nothing to release.
	Close() error
}
