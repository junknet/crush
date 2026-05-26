package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveModelContextWindow(t *testing.T) {
	t.Parallel()

	require.Equal(t, int64(123), ResolveModelContextWindow(123, "custom", "", "model"))
	require.Equal(t, int64(1_048_576), ResolveModelContextWindow(0, "antigravity", "antigravity", "gemini-2.5-pro"))
	require.Equal(t, int64(1_048_576), ResolveModelContextWindow(0, "google", "gemini", "gemini-2.5-flash"))
	require.Equal(t, int64(200_000), ResolveModelContextWindow(0, "anthropic", "anthropic", "claude-sonnet-4-5"))
	require.Equal(t, int64(0), ResolveModelContextWindow(0, "custom", "openai-compat", "unknown"))
}
