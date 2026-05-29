// Package tools contains workspace IO helpers.
//
// These wrappers mirror os.* signatures used by the file tools, but route
// through the active IO backend resolved from the context (see
// GetBackendFromContext). When no backend is attached they fall back to direct
// local os.* calls, so the default local path is byte-for-byte unchanged. When
// a remote backend is attached (via remote_attach), the same edit/view/write
// calls transparently operate on the remote host.
package tools

import (
	"context"
	"io/fs"
	"os"
)

// CtxStat returns fs.FileInfo for path on the active backend.
func CtxStat(ctx context.Context, path string) (fs.FileInfo, error) {
	if b := GetBackendFromContext(ctx); b != nil {
		return b.Stat(ctx, path)
	}
	return os.Stat(path)
}

// CtxReadFile reads path from the active backend.
func CtxReadFile(ctx context.Context, path string) ([]byte, error) {
	if b := GetBackendFromContext(ctx); b != nil {
		return b.ReadFile(ctx, path)
	}
	return os.ReadFile(path)
}

// CtxWriteFile writes data to the active backend.
func CtxWriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	if b := GetBackendFromContext(ctx); b != nil {
		return b.WriteFile(ctx, path, data, perm)
	}
	return os.WriteFile(path, data, perm)
}

// CtxMkdirAll creates directories on the active backend (MkdirAll semantics).
func CtxMkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	if b := GetBackendFromContext(ctx); b != nil {
		return b.Mkdir(ctx, path, perm)
	}
	return os.MkdirAll(path, perm)
}

// CtxRemove removes a path on the active backend.
func CtxRemove(ctx context.Context, path string) error {
	if b := GetBackendFromContext(ctx); b != nil {
		return b.Remove(ctx, path)
	}
	return os.Remove(path)
}

// CtxRename renames a path on the active backend.
func CtxRename(ctx context.Context, oldPath, newPath string) error {
	if b := GetBackendFromContext(ctx); b != nil {
		return b.Rename(ctx, oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

// CtxReadDir lists a directory on the active backend.
func CtxReadDir(ctx context.Context, path string) ([]fs.DirEntry, error) {
	if b := GetBackendFromContext(ctx); b != nil {
		return b.ReadDir(ctx, path)
	}
	return os.ReadDir(path)
}
