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

func TestKeyboardInputFromChatFocusSnapsToEditor(t *testing.T) {
	t.Parallel()

	ui := newTestUI()
	ui.focus = uiFocusMain
	ui.textarea.Blur()

	ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))

	require.Equal(t, uiFocusEditor, ui.focus)
	require.True(t, ui.textarea.Focused())
	require.Equal(t, "a", ui.textarea.Value())
}

func TestHistoryUpFromChatFocusOverridesDraft(t *testing.T) {
	t.Parallel()

	ui := newTestUI()
	ui.focus = uiFocusMain
	ui.textarea.SetValue("draft")
	ui.promptHistory.messages = []string{"newest prompt", "older prompt"}
	ui.promptHistory.index = -1

	ui.handleHistoryUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	require.Equal(t, uiFocusEditor, ui.focus)
	require.Equal(t, "newest prompt", ui.textarea.Value())
	require.Equal(t, "draft", ui.promptHistory.draft)
	require.Equal(t, 0, ui.promptHistory.index)
}

func TestTabKeepsEditorFocused(t *testing.T) {
	t.Parallel()

	ui := newTestUI()
	ui.focus = uiFocusMain
	ui.textarea.Blur()

	ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))

	require.Equal(t, uiFocusEditor, ui.focus)
	require.True(t, ui.textarea.Focused())
}

func TestPromptHistoryBoundaryChecks(t *testing.T) {
	t.Parallel()

	ui := newTestUI()
	ui.promptHistory.messages = []string{"older prompt", "oldest prompt"}
	ui.promptHistory.index = -1

	// Single line
	ui.textarea.SetValue("single line")
	ui.textarea.MoveToEnd()
	require.True(t, ui.isAtEditorStart())
	require.True(t, ui.isAtEditorEnd())

	// Multiline
	ui.textarea.SetValue("line 1\nline 2")
	ui.textarea.MoveToEnd()
	require.False(t, ui.isAtEditorStart())
	require.True(t, ui.isAtEditorEnd())

	// Move cursor up via handleKeyPressMsg.
	// Since isAtEditorStart() is false, KeyUp should move cursor to line 0.
	_ = ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	require.True(t, ui.isAtEditorStart())
	require.False(t, ui.isAtEditorEnd())
}
