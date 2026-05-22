package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

func TestPromptHistoryUpAlwaysShowsPreviousPrompt(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.promptHistory.messages = []string{"newest prompt", "older prompt"}
	ui.promptHistory.index = -1
	ui.textarea.SetValue("draft")

	ui.handleHistoryUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	require.Equal(t, "newest prompt", ui.textarea.Value())
	require.Equal(t, 0, ui.promptHistory.index)
	require.Equal(t, "draft", ui.promptHistory.draft)
	require.True(t, ui.isAtEditorEnd())
}

func TestPromptHistoryDownRestoresDraft(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.promptHistory.messages = []string{"newest prompt", "older prompt"}
	ui.promptHistory.index = -1
	ui.textarea.SetValue("draft")
	_ = ui.handleHistoryUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	ui.handleHistoryDown(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))

	require.Equal(t, "draft", ui.textarea.Value())
	require.Equal(t, -1, ui.promptHistory.index)
}

func TestPromptHistoryUpWalksOlderEntries(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.promptHistory.messages = []string{"newest prompt", "older prompt"}
	ui.promptHistory.index = -1

	_ = ui.handleHistoryUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	ui.handleHistoryUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	require.Equal(t, "older prompt", ui.textarea.Value())
	require.Equal(t, 1, ui.promptHistory.index)
}
