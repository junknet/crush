// Package tools contains local workspace IO helpers.
//
// These wrappers intentionally mirror os.* signatures used by the file tools.
package tools

import (
	"context"
	"io/fs"
	"os"
)

// CtxStat returns fs.FileInfo for path.
func CtxStat(ctx context.Context, path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

// CtxReadFile reads path from the local filesystem.
func CtxReadFile(ctx context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

// CtxWriteFile writes data to the local filesystem.
func CtxWriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// CtxMkdirAll creates local directories.
func CtxMkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

// CtxRemove removes a local path.
func CtxRemove(ctx context.Context, path string) error {
	return os.Remove(path)
}
