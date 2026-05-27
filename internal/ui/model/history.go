package model

import (
	"context"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
)

const (
	summaryPromptHistoryPrefix     = "Compress the conversation into durable memory for the next agent."
	interruptedPromptHistoryPrefix = "The previous session was interrupted because it got too long"
)

// promptHistoryLoadedMsg is sent when prompt history is loaded.
type promptHistoryLoadedMsg struct {
	messages []string
}

// loadPromptHistory loads user messages for history navigation.
func (m *UI) loadPromptHistory() tea.Cmd {
	sessionID := ""
	if m.session != nil {
		sessionID = m.session.ID
	}
	return func() tea.Msg {
		ctx := context.Background()
		var messages []message.Message
		var err error
		if sessionID != "" {
			messages, err = m.com.Workspace.ListUserMessages(ctx, sessionID)
		} else {
			messages, err = m.com.Workspace.ListAllUserMessages(ctx)
		}
		if err != nil {
			slog.Error("Failed to load prompt history", "error", err)
			return promptHistoryLoadedMsg{messages: nil}
		}

		texts := make([]string, 0, len(messages))
		seen := make(map[string]bool)
		for _, msg := range messages {
			if text := msg.Content().Text; text != "" {
				if !shouldIncludePromptHistoryText(text) {
					continue
				}
				if !seen[text] {
					seen[text] = true
					texts = append(texts, text)
				}
			}
		}
		return promptHistoryLoadedMsg{messages: texts}
	}
}

func shouldIncludePromptHistoryText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, summaryPromptHistoryPrefix) {
		return false
	}
	if strings.HasPrefix(trimmed, interruptedPromptHistoryPrefix) {
		return false
	}
	return true
}

// handleHistoryUp handles up arrow for history navigation.
func (m *UI) handleHistoryUp(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	var cmds []tea.Cmd
	if cmd := m.focusEditor(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.historyPrev() {
		// we send this so that the textarea moves the view to the correct position
		// without this the cursor will show up in the wrong place.
		cmds = append(cmds, m.updateTextareaWithPrevHeight(nil, prevHeight))
		return tea.Batch(cmds...)
	}
	cmds = append(cmds, m.updateTextarea(msg))
	return tea.Batch(cmds...)
}

// handleHistoryDown handles down arrow for history navigation.
func (m *UI) handleHistoryDown(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	var cmds []tea.Cmd
	if cmd := m.focusEditor(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.historyNext() {
		// we send this so that the textarea moves the view to the correct position
		// without this the cursor will show up in the wrong place.
		cmds = append(cmds, m.updateTextareaWithPrevHeight(nil, prevHeight))
		return tea.Batch(cmds...)
	}
	cmds = append(cmds, m.updateTextarea(msg))
	return tea.Batch(cmds...)
}

// handleHistoryEscape handles escape for exiting history navigation.
func (m *UI) handleHistoryEscape(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	var cmds []tea.Cmd
	if cmd := m.focusEditor(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// Return to current draft when browsing history.
	if m.promptHistory.index >= 0 {
		m.promptHistory.index = -1
		m.textarea.Reset()
		m.textarea.InsertString(m.promptHistory.draft)
		cmds = append(cmds, m.updateTextareaWithPrevHeight(nil, prevHeight))
		return tea.Batch(cmds...)
	}

	// Let textarea handle escape normally.
	cmds = append(cmds, m.updateTextarea(msg))
	return tea.Batch(cmds...)
}

// updateHistoryDraft updates history state when text is modified.
func (m *UI) updateHistoryDraft(oldValue string) {
	if m.textarea.Value() != oldValue {
		m.promptHistory.draft = m.textarea.Value()
		m.promptHistory.index = -1
	}
}

// historyPrev changes the text area content to the previous message in the history
// it returns false if it could not find the previous message.
func (m *UI) historyPrev() bool {
	if len(m.promptHistory.messages) == 0 {
		return false
	}
	if m.promptHistory.index == -1 {
		m.promptHistory.draft = m.textarea.Value()
	}
	nextIndex := m.promptHistory.index + 1
	if nextIndex >= len(m.promptHistory.messages) {
		return false
	}
	m.promptHistory.index = nextIndex
	m.textarea.Reset()
	m.textarea.InsertString(m.promptHistory.messages[nextIndex])
	m.textarea.MoveToEnd()
	return true
}

// historyNext changes the text area content to the next message in the history
// it returns false if it could not find the next message.
func (m *UI) historyNext() bool {
	if m.promptHistory.index < 0 {
		return false
	}
	nextIndex := m.promptHistory.index - 1
	if nextIndex < 0 {
		m.promptHistory.index = -1
		m.textarea.Reset()
		m.textarea.InsertString(m.promptHistory.draft)
		return true
	}
	m.promptHistory.index = nextIndex
	m.textarea.Reset()
	m.textarea.InsertString(m.promptHistory.messages[nextIndex])
	m.textarea.MoveToEnd()
	return true
}

// historyReset resets the history, but does not clear the message
// it just sets the current draft to empty and the position in the history.
func (m *UI) historyReset() {
	m.promptHistory.index = -1
	m.promptHistory.draft = ""
}

// isAtEditorStart returns true if we are at the first line in the textarea.
func (m *UI) isAtEditorStart() bool {
	return m.textarea.Line() == 0
}

// isAtEditorEnd returns true if we are on the last line in the textarea.
func (m *UI) isAtEditorEnd() bool {
	lineCount := m.textarea.LineCount()
	if lineCount == 0 {
		return true
	}
	return m.textarea.Line() == lineCount-1
}
