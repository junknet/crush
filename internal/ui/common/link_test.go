package common

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

func TestTextLinkRendersOSC8Hyperlink(t *testing.T) {
	t.Parallel()

	out := TextLink(lipgloss.NewStyle(), "example", "https://example.com")

	require.Contains(t, out, "\x1b]8;")
	require.Contains(t, out, "https://example.com")
	require.Contains(t, out, "example")
}

func TestLinkTargetDetectsURLsAndPaths(t *testing.T) {
	t.Parallel()

	urlTarget, ok := LinkTarget("https://example.com/docs")
	require.True(t, ok)
	require.Equal(t, "https://example.com/docs", urlTarget)

	fileTarget, ok := LinkTarget("~/notes/todo.md")
	require.True(t, ok)
	require.True(t, strings.HasPrefix(fileTarget, "file://"))
	require.Contains(t, fileTarget, "/notes/todo.md")

	_, ok = LinkTarget("plain words")
	require.False(t, ok)
}
