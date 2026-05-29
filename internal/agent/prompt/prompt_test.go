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

func TestClaudeGlobalPromptDiscovery(t *testing.T) {
	// Create a mock home directory
	mockHome, err := os.MkdirTemp("", "crush-test-home-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockHome)

	// Override home.Dir()
	home.SetDir(mockHome)
	defer home.ResetDir()

	// Create .claude/global_prompt.md
	claudeDir := filepath.Join(mockHome, ".claude")
	err = os.MkdirAll(claudeDir, 0755)
	require.NoError(t, err)

	promptContent := "Custom Global Prompt for Testing"
	err = os.WriteFile(filepath.Join(claudeDir, "global_prompt.md"), []byte(promptContent), 0644)
	require.NoError(t, err)

	p, err := NewPrompt("test", "template")
	require.NoError(t, err)

	// Mock ConfigStore
	tmpDir, err := os.MkdirTemp("", "crush-test-wd-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := config.Init(tmpDir, "", false)
	require.NoError(t, err)

	data, err := p.promptData(context.Background(), "provider", "model", store)
	require.NoError(t, err)

	require.Contains(t, data.ClaudeGlobalPrompt, promptContent)
	require.Contains(t, data.ClaudeGlobalPrompt, "global_prompt.md")
}

func TestClaudeGlobalPromptLoadsPersonalConstitution(t *testing.T) {
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

	require.Contains(t, data.ClaudeGlobalPrompt, "Personal Constitution")
	require.Contains(t, data.ClaudeGlobalPrompt, "CLAUDE.md")
}
