package prompt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/stretchr/testify/require"
)

func TestUserConstitutionIgnoresLegacyGlobalPrompt(t *testing.T) {
	mockHome, err := os.MkdirTemp("", "crush-test-home-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockHome)

	home.SetDir(mockHome)
	defer home.ResetDir()

	claudeDir := filepath.Join(mockHome, ".claude")
	err = os.MkdirAll(claudeDir, 0755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(claudeDir, "global_prompt.md"), []byte("Legacy Global Prompt"), 0644)
	require.NoError(t, err)

	p, err := NewPrompt("test", "template")
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "crush-test-wd-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := config.Init(tmpDir, "", false)
	require.NoError(t, err)

	data, err := p.promptData(context.Background(), "provider", "model", store)
	require.NoError(t, err)

	require.Empty(t, data.UserConstitution)
}

func TestUserConstitutionLoadsPersonalConstitution(t *testing.T) {
	mockHome, err := os.MkdirTemp("", "crush-test-home-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockHome)

	home.SetDir(mockHome)
	defer home.ResetDir()

	claudeDir := filepath.Join(mockHome, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("Personal Constitution"), 0o644))

	p, err := NewPrompt("test", "template")
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "crush-test-wd-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := config.Init(tmpDir, "", false)
	require.NoError(t, err)

	data, err := p.promptData(context.Background(), "provider", "model", store)
	require.NoError(t, err)

	require.Contains(t, data.UserConstitution, "Personal Constitution")
	require.Contains(t, data.UserConstitution, "CLAUDE.md")
}

// explore is a read-only fact-retrieval agent; its parent enforces the
// constitution. promptData must skip the ~17KB injection for it even when a
// personal CLAUDE.md exists, so the thin prompt is not dwarfed.
func TestExploreSkipsConstitution(t *testing.T) {
	mockHome, err := os.MkdirTemp("", "crush-test-home-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockHome)

	home.SetDir(mockHome)
	defer home.ResetDir()

	claudeDir := filepath.Join(mockHome, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("Personal Constitution"), 0o644))

	explore, err := NewPrompt("explore", "template")
	require.NoError(t, err)

	tmpDir, err := os.MkdirTemp("", "crush-test-wd-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := config.Init(tmpDir, "", false)
	require.NoError(t, err)

	data, err := explore.promptData(context.Background(), "provider", "model", store)
	require.NoError(t, err)

	require.Empty(t, data.UserConstitution, "explore must not receive the constitution")
}
