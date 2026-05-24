// Package tools — workspace IO helpers.
//
// These wrappers exist so that file-system-touching tools (view, edit,
// write, multiedit, grep, glob, ls, download) can read/write paths
// through the active workspace driver — local FS by default, remote
// SFTP+exec when the session targets `ssh://...` — without each tool
// having to thread an iodriver.Driver argument through its
// constructor.
//
// Each helper resolves the driver from ctx at call time so mid-session
// `set_workspace` switches take effect on the very next tool call.
// When ctx carries no driver (legacy paths, tests), the helpers fall
// back to direct os.* calls so behavior is identical to before the
// driver abstraction landed.
//
// The functions intentionally mirror the os.* signatures the existing
// tool code uses, so the patch in each tool is a one-line replacement
// (s/os\./ctx/) rather than a control-flow rewrite.
package tools

import (
	"context"
	"io/fs"
	"os"

	"github.com/charmbracelet/crush/internal/agent/iodriver"
)

func ctxDriver(ctx context.Context) iodriver.Driver {
	return iodriver.FromContext(ctx)
}

// CtxStat returns fs.FileInfo for path, routed through the driver in
// ctx when present, otherwise os.Stat. Path may be absolute or
// driver-relative; absolute paths are passed through unchanged so the
// existing "absolute-path-first" tool conventions keep working.
func CtxStat(ctx context.Context, path string) (fs.FileInfo, error) {
	if d := ctxDriver(ctx); d != nil {
		return d.Stat(ctx, path)
	}
	return os.Stat(path)
}

// CtxReadFile reads path through the active driver.
func CtxReadFile(ctx context.Context, path string) ([]byte, error) {
	if d := ctxDriver(ctx); d != nil {
		return d.ReadFile(ctx, path)
	}
	return os.ReadFile(path)
}

// CtxWriteFile writes data to path through the active driver.
func CtxWriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	if d := ctxDriver(ctx); d != nil {
		return d.WriteFile(ctx, path, data, perm)
	}
	return os.WriteFile(path, data, perm)
}

// CtxMkdirAll creates directories through the active driver.
func CtxMkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	if d := ctxDriver(ctx); d != nil {
		return d.MkdirAll(ctx, path, perm)
	}
	return os.MkdirAll(path, perm)
}

// CtxRemove removes path through the active driver.
func CtxRemove(ctx context.Context, path string) error {
	if d := ctxDriver(ctx); d != nil {
		return d.Remove(ctx, path)
	}
	return os.Remove(path)
}
