package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// Grep/Find are registered aliases of Search with the mode forced, so the
// model's habitual "Grep"/"Find" calls (which omit mode) resolve and work
// instead of failing with "tool not found".
func TestGrepFindAliasesForceMode(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n// needle here\n"), 0o644))
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")

	// Grep with NO mode → forced content search.
	grep := NewGrepTool(dir)
	require.Equal(t, "Grep", grep.Info().Name)
	resp, err := grep.Run(ctx, fantasy.ToolCall{ID: "1", Name: "Grep", Input: `{"pattern":"needle"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "a.go")

	// Find with NO mode → forced files search.
	find := NewFindTool(dir)
	require.Equal(t, "Find", find.Info().Name)
	resp, err = find.Run(ctx, fantasy.ToolCall{ID: "2", Name: "Find", Input: `{"pattern":"*.go"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "a.go")
}
