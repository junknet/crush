package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRgSearchFilesUsesRegexPattern(t *testing.T) {
	t.Parallel()

	if getRg() == "" {
		t.Skip("rg not available")
	}

	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "readme.md"), []byte("# readme\n"), 0o644))

	matches, truncated, err := RgSearchFiles(context.Background(), `\.go$`, workingDir, "", 100)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, matches, 1)
	require.Equal(t, "main.go", filepath.Base(matches[0].Path))
}
