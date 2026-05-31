package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/iodriver"
	"github.com/stretchr/testify/require"
)

// ctxWithBackend returns a context whose session resolves to a LocalBackend
// rooted at backendRoot — the same wiring a git-worktree/remote attach uses.
func ctxWithBackend(backendRoot string) context.Context {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "be-session")
	reg := csync.NewMap[string, iodriver.Backend]()
	reg.Set("be-session", iodriver.NewLocalBackend(backendRoot))
	return WithBackendRegistry(ctx, reg)
}

// Linchpin: with an active backend, a relative file_path resolves against the
// backend root (worktree), NOT the tool's construction-time workingDir. This is
// what lets a worker run isolated in a worktree while the tool set is built once
// against the main workspace.
func TestBackendRootRedirectsRelativePaths(t *testing.T) {
	bakedDir := t.TempDir()   // construction-time workingDir (e.g. main workspace)
	backendDir := t.TempDir() // active backend root (e.g. a git worktree)

	// Same relative name exists in BOTH dirs with different content, so a wrong
	// base would silently read/write the wrong file instead of erroring.
	require.NoError(t, os.WriteFile(filepath.Join(bakedDir, "f.txt"), []byte("FROM-BAKED"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(backendDir, "f.txt"), []byte("FROM-BACKEND"), 0o644))

	ctx := ctxWithBackend(backendDir)

	t.Run("View reads from backend root", func(t *testing.T) {
		view := NewViewTool(nil, &mockBashPermissionService{}, mockFileTracker{}, nil, bakedDir)
		resp, err := view.Run(ctx, fantasy.ToolCall{ID: "1", Name: "Read", Input: `{"file_path":"f.txt"}`})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		require.Contains(t, resp.Content, "FROM-BACKEND")
		require.NotContains(t, resp.Content, "FROM-BAKED")
	})

	t.Run("Write lands in backend root", func(t *testing.T) {
		write := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, bakedDir)
		_, err := write.Run(ctx, fantasy.ToolCall{ID: "2", Name: "Write", Input: `{"file_path":"new.txt","content":"HELLO-WORKTREE"}`})
		require.NoError(t, err)

		got, err := os.ReadFile(filepath.Join(backendDir, "new.txt"))
		require.NoError(t, err, "file must be created under the backend root")
		require.Equal(t, "HELLO-WORKTREE", string(got))

		_, statErr := os.Stat(filepath.Join(bakedDir, "new.txt"))
		require.True(t, os.IsNotExist(statErr), "must NOT be created under the baked workingDir")
	})
}

// Regression: with NO backend attached, relative paths resolve against the baked
// workingDir exactly as before (byte-for-byte default path).
func TestNoBackendUsesBakedWorkingDir(t *testing.T) {
	bakedDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bakedDir, "f.txt"), []byte("FROM-BAKED"), 0o644))

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "no-be")
	view := NewViewTool(nil, &mockBashPermissionService{}, mockFileTracker{}, nil, bakedDir)
	resp, err := view.Run(ctx, fantasy.ToolCall{ID: "1", Name: "Read", Input: `{"file_path":"f.txt"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "FROM-BAKED")
}
