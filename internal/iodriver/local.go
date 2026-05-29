package iodriver

import (
	"context"
	"io/fs"
	"os"
)

// LocalBackend executes file operations directly against the local filesystem.
// It is the default backend and reproduces the behavior tools had before the
// abstraction existed (direct os.* calls), so attaching no backend is
// byte-for-byte identical to using a LocalBackend.
type LocalBackend struct {
	root string
}

// NewLocalBackend returns a LocalBackend rooted at the given workspace dir.
func NewLocalBackend(root string) *LocalBackend {
	return &LocalBackend{root: root}
}

func (b *LocalBackend) Kind() string { return "local" }
func (b *LocalBackend) Root() string { return b.root }
func (b *LocalBackend) Close() error { return nil }

func (b *LocalBackend) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

func (b *LocalBackend) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (b *LocalBackend) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (b *LocalBackend) Mkdir(_ context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (b *LocalBackend) Remove(_ context.Context, path string) error {
	return os.Remove(path)
}

func (b *LocalBackend) Rename(_ context.Context, oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (b *LocalBackend) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
}
