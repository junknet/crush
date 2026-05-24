// Package iodriver abstracts the IO surface that file/shell/search tools
// touch so the same toolset can run against a local filesystem
// (LocalDriver) or a remote SSH host (SSHDriver) without the individual
// tools knowing the difference. The brain stays local, only the actual
// filesystem reads/writes and command execution hop to wherever the
// active session's WorkspaceURI points.
//
// Driver instances are stateful (SSH connections, persistent PTY shells,
// caches) and must be Close()'d when the session that owns them ends.
// Lookup happens in two layers:
//
//  1. A Factory maps URI strings to live Driver instances, deduplicating
//     so multiple sessions to the same host share one SSH connection.
//  2. Per-tool-call, the Driver lives in context.Context — tools call
//     iodriver.FromContext(ctx) and fall back to a LocalDriver pinned to
//     the workingDir baked into the tool at construction time. This means
//     no tool constructor signature changes; SSH activation is a runtime
//     property of the call.
package iodriver

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"strings"
)

// Kind enumerates the supported driver implementations.
type Kind string

const (
	KindLocal Kind = "local"
	KindSSH   Kind = "ssh"
)

// Driver is the IO surface every file/shell/search tool talks to. All
// paths are interpreted relative to WorkingDir(ctx) unless absolute.
// Errors should preserve the underlying io/os semantics where possible
// (os.IsNotExist still works for Stat misses, etc.) so tool error paths
// don't need driver-specific branching.
type Driver interface {
	Kind() Kind

	// URI is the canonical workspace URI this driver materialises
	// ("local:/abs/path" or "ssh://user@host:port/path"). Used for
	// logging, telemetry, and factory dedup keying.
	URI() string

	// WorkingDir returns the absolute working directory on the target
	// filesystem. SSH drivers cache this after the first connection.
	WorkingDir(ctx context.Context) string

	// Filesystem
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	Remove(ctx context.Context, path string) error
	MkdirAll(ctx context.Context, path string, perm fs.FileMode) error
	// Walk visits the file tree rooted at root. fn follows fs.WalkDirFunc
	// semantics: returning fs.SkipDir skips a subtree, fs.SkipAll stops
	// the walk, any other error halts it.
	Walk(ctx context.Context, root string, fn fs.WalkDirFunc) error

	// Exec runs argv once and returns the captured output and exit code.
	// For shell-style commands ("a | b && c"), wrap in bash explicitly:
	//   driver.Exec(ctx, []string{"bash", "-c", "a | b && c"}, nil)
	Exec(ctx context.Context, argv []string, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error)

	// Search (one round-trip even when driver is remote)
	Grep(ctx context.Context, opts GrepOpts) ([]GrepHit, error)
	Glob(ctx context.Context, opts GlobOpts) ([]string, error)

	// Close releases connections, cached PTY shells, sftp sessions, etc.
	// Safe to call multiple times.
	Close() error
}

// GrepOpts describes a recursive content search rooted at Path.
// Implementations are expected to invoke ripgrep when available; the
// embedded rg binary (see internal/agent/tools/embed_rg.go) is auto-
// pushed to remote hosts by the SSH driver.
type GrepOpts struct {
	Pattern     string
	Path        string
	Include     string // glob filter, e.g. "*.go"
	Literal     bool
	IgnoreCase  bool
	MaxResults  int
	ContextLine int
}

// GrepHit is one match from Grep.
type GrepHit struct {
	Path    string
	Line    int
	Column  int
	Content string
}

// GlobOpts is a filename-only search rooted at Path.
type GlobOpts struct {
	Pattern    string
	Path       string
	MaxResults int
}

// ErrNotImplemented is returned by partial driver implementations.
var ErrNotImplemented = errors.New("iodriver: not implemented")

// ParseURI parses a workspace URI string into a normalized form. Empty
// or "local" defaults to a local driver pinned to fallback. Returns the
// driver Kind, parsed *url.URL (nil for local-only), and the effective
// working directory.
//
// Supported shapes:
//   - "" or "local"                            → local, cwd = fallback
//   - "local:/abs/path" or "local:relative"    → local, cwd = path
//   - "ssh://user@host[:port][/path][?key=...]" → ssh
func ParseURI(uri, fallbackCwd string) (Kind, *url.URL, string, error) {
	s := strings.TrimSpace(uri)
	if s == "" || s == "local" {
		return KindLocal, nil, fallbackCwd, nil
	}
	if strings.HasPrefix(s, "local:") {
		path := strings.TrimPrefix(s, "local:")
		if path == "" {
			path = fallbackCwd
		}
		return KindLocal, nil, path, nil
	}
	if strings.HasPrefix(s, "ssh://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", nil, "", err
		}
		if u.User == nil || u.User.Username() == "" {
			return "", nil, "", errors.New("iodriver: ssh URI missing user (use ssh://user@host[:port]/path)")
		}
		if u.Host == "" {
			return "", nil, "", errors.New("iodriver: ssh URI missing host")
		}
		cwd := u.Path
		if cwd == "" {
			cwd = "."
		}
		return KindSSH, u, cwd, nil
	}
	return "", nil, "", errors.New("iodriver: unsupported URI scheme: " + s)
}
