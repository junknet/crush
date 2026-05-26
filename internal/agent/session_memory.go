package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
)

const (
	sessionMemoryInitTokens   int64 = 10_000
	sessionMemoryUpdateTokens int64 = 5_000
	sessionMemoryToolCalls          = 3
	sessionMemoryMaxOutput    int64 = 6_000
	sessionMemoryTimeout            = 2 * time.Minute
	sessionMemoryRecentBytes        = 30 * 1024
)

type sessionMemoryState struct {
	LastMessageID      string `json:"last_message_id"`
	TokensAtLastUpdate int64  `json:"tokens_at_last_update"`
	Initialized        bool   `json:"initialized"`
}

const sessionMemoryTemplate = `# Session Title
_A short and distinctive 5-10 word descriptive title for the session. Super info dense, no filler_

# Current State
_What is actively being worked on right now? Pending tasks not yet completed. Immediate next steps._

# Task Specification
_What did the user ask to build? Any design decisions or other explanatory context._

# Files and Functions
_Important files, functions, and why they matter._

# Workflow
_Commands usually run, order, and how to interpret output._

# Errors & Corrections
_Errors encountered, fixes, user corrections, and approaches to avoid._

# Codebase and System Documentation
_Important system components and how they fit together._

# Learnings
_What worked, what did not, what to avoid. Do not duplicate other sections._

# Key Results
_Specific outputs the user requested, if any._

# Worklog
_Terse step-by-step record of what was attempted and done._
`

func (a *sessionAgent) sessionMemoryDir(sessionID string) string {
	return filepath.Join(a.dataDir, "sessions", sessionID, "session-memory")
}

func (a *sessionAgent) sessionMemoryPath(sessionID string) string {
	return filepath.Join(a.sessionMemoryDir(sessionID), "summary.md")
}

func (a *sessionAgent) sessionMemoryStatePath(sessionID string) string {
	return filepath.Join(a.sessionMemoryDir(sessionID), "state.json")
}

func (a *sessionAgent) ensureSessionMemoryFile(sessionID string) (string, error) {
	dir := a.sessionMemoryDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session memory dir: %w", err)
	}
	path := a.sessionMemoryPath(sessionID)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(sessionMemoryTemplate), 0o600); err != nil {
		return "", fmt.Errorf("seed session memory: %w", err)
	}
	return path, nil
}

func (a *sessionAgent) readSessionMemoryState(sessionID string) sessionMemoryState {
	data, err := os.ReadFile(a.sessionMemoryStatePath(sessionID))
	if err != nil {
		return sessionMemoryState{}
	}
	var state sessionMemoryState
	if err := json.Unmarshal(data, &state); err != nil {
		return sessionMemoryState{}
	}
	return state
}

func (a *sessionAgent) writeSessionMemoryState(sessionID string, state sessionMemoryState) error {
	if err := os.MkdirAll(a.sessionMemoryDir(sessionID), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.sessionMemoryStatePath(sessionID), append(data, '\n'), 0o600)
}

func (a *sessionAgent) maybeUpdateSessionMemory(parent context.Context, sessionID string, model Model) {
	if a.dataDir == "" || sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, sessionMemoryTimeout)
	defer cancel()

	if err := a.messages.FlushAll(ctx); err != nil {
		slog.Debug("Session memory skipped: flush failed", "session", sessionID, "error", err)
		return
	}
	currentSession, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		slog.Debug("Session memory skipped: session lookup failed", "session", sessionID, "error", err)
		return
	}
	msgs, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		slog.Debug("Session memory skipped: message lookup failed", "session", sessionID, "error", err)
		return
	}
	state := a.readSessionMemoryState(sessionID)
	tokenCount := estimateSummaryMessageTokens(msgs, model.CatwalkCfg.SupportsImages)
	if !shouldUpdateSessionMemory(msgs, state, tokenCount) {
		return
	}

	notesPath, err := a.ensureSessionMemoryFile(sessionID)
	if err != nil {
		slog.Debug("Session memory skipped: ensure failed", "session", sessionID, "error", err)
		return
	}
	currentNotesBytes, err := os.ReadFile(notesPath)
	if err != nil {
		slog.Debug("Session memory skipped: read failed", "session", sessionID, "error", err)
		return
	}

	aiMsgs, _ := a.preparePrompt(currentSession, msgs, model.CatwalkCfg.SupportsImages, model.ProviderType)
	agent := fantasy.NewAgent(
		model.Model,
		fantasy.WithSystemPrompt(sessionMemoryUpdateSystemPrompt),
		fantasy.WithUserAgent(userAgent),
	)
	resp, err := agent.Stream(ctx, fantasy.AgentStreamCall{
		Prompt:          buildSessionMemoryUpdatePrompt(notesPath, string(currentNotesBytes)),
		Messages:        aiMsgs,
		MaxOutputTokens: ptrInt64(sessionMemoryMaxOutput),
	})
	if err != nil {
		slog.Debug("Session memory update failed", "session", sessionID, "error", err)
		return
	}
	updatedNotes := cleanSessionMemoryOutput(resp.Response.Content.Text())
	if !sessionMemoryHasContent(updatedNotes) {
		slog.Debug("Session memory update produced empty notes", "session", sessionID)
		return
	}
	if err := os.WriteFile(notesPath, []byte(updatedNotes), 0o600); err != nil {
		slog.Debug("Session memory write failed", "session", sessionID, "error", err)
		return
	}
	lastID := ""
	if len(msgs) > 0 {
		lastID = msgs[len(msgs)-1].ID
	}
	state = sessionMemoryState{
		LastMessageID:      lastID,
		TokensAtLastUpdate: tokenCount,
		Initialized:        true,
	}
	if err := a.writeSessionMemoryState(sessionID, state); err != nil {
		slog.Debug("Session memory state write failed", "session", sessionID, "error", err)
	}
	slog.Debug("Session memory updated", "session", sessionID, "tokens", tokenCount, "path", notesPath)
}

func ptrInt64(v int64) *int64 {
	return &v
}

func shouldUpdateSessionMemory(msgs []message.Message, state sessionMemoryState, tokenCount int64) bool {
	if len(msgs) == 0 {
		return false
	}
	if !state.Initialized && tokenCount < sessionMemoryInitTokens {
		return false
	}
	tokensSinceUpdate := tokenCount - state.TokensAtLastUpdate
	if tokensSinceUpdate < sessionMemoryUpdateTokens {
		return false
	}
	toolCalls := countToolCallsSinceMessage(msgs, state.LastMessageID)
	return toolCalls >= sessionMemoryToolCalls || !lastAssistantHasToolCalls(msgs)
}

func countToolCallsSinceMessage(msgs []message.Message, lastMessageID string) int {
	found := lastMessageID == ""
	count := 0
	for _, msg := range msgs {
		if !found {
			if msg.ID == lastMessageID {
				found = true
			}
			continue
		}
		if msg.Role != message.Assistant {
			continue
		}
		count += len(msg.ToolCalls())
	}
	return count
}

func lastAssistantHasToolCalls(msgs []message.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.Assistant {
			return len(msgs[i].ToolCalls()) > 0
		}
	}
	return false
}

const sessionMemoryUpdateSystemPrompt = `You update a private session-memory markdown file for a coding-agent session.

Return the COMPLETE updated markdown file only. Do not call tools. Do not mention note-taking. Preserve the existing headings and italic section descriptions exactly. Update only the content below each section description.
Prefer dense, concrete facts: file paths, function names, commands, decisions, user corrections, current state, and next steps. Remove stale details when they are superseded.`

func buildSessionMemoryUpdatePrompt(path, currentNotes string) string {
	return fmt.Sprintf(`Update the session memory file at %s.

Current notes:
<current_notes>
%s
</current_notes>

Use the conversation above as the source of truth. Output the full updated markdown file only.`, path, currentNotes)
}

func cleanSessionMemoryOutput(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			s = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	return strings.TrimRight(s, "\n") + "\n"
}

func sessionMemoryHasContent(notes string) bool {
	for _, line := range strings.Split(notes, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "_") && strings.HasSuffix(line, "_") {
			continue
		}
		return true
	}
	return false
}

func (a *sessionAgent) trySessionMemoryCompaction(ctx context.Context, sessionID string, model Model, currentSession session.Session, msgs []message.Message) (bool, error) {
	if a.dataDir == "" || sessionID == "" {
		return false, nil
	}
	notesPath := a.sessionMemoryPath(sessionID)
	notesBytes, err := os.ReadFile(notesPath)
	if err != nil {
		return false, nil
	}
	notes := string(notesBytes)
	if !sessionMemoryHasContent(notes) {
		return false, nil
	}

	state := a.readSessionMemoryState(sessionID)
	recentContext := buildRecentSessionMemoryContext(msgs, state.LastMessageID)
	summaryText := "Session memory compaction summary.\n\n<session_memory>\n" + notes + "\n</session_memory>"
	if recentContext != "" {
		summaryText += "\n\n<recent_unsummarized_context>\n" + recentContext + "\n</recent_unsummarized_context>"
	}
	summaryText += fmt.Sprintf("\n\nFull session memory file: %s\n", notesPath)

	compactionStartedAt := time.Now()
	a.appendConversationCompactionTrace(ctx, agentruntime.TraceKindConversationCompactionStarted, compactionTraceOptions{
		startedAt:                     compactionStartedAt,
		currentModel:                  model,
		currentSession:                currentSession,
		sourceMessages:                len(msgs),
		contextBytes:                  jsonSize(msgs),
		preflightEstimatedInputTokens: estimateSummaryMessageTokens(msgs, model.CatwalkCfg.SupportsImages),
		contextWindowTokens:           resolvedModelContextWindow(model),
		maxOutputTokens:               sessionMemoryMaxOutput,
	})

	summaryMessage, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:             message.Assistant,
		Parts:            []message.ContentPart{message.TextContent{Text: summaryText}},
		Model:            model.Model.Model(),
		Provider:         model.Model.Provider(),
		IsSummaryMessage: true,
	})
	if err != nil {
		return false, err
	}
	summaryMessage.AddFinish(message.FinishReasonEndTurn, "", "")
	if err := a.messages.Update(ctx, summaryMessage); err != nil {
		return false, err
	}

	currentSession.SummaryMessageID = summaryMessage.ID
	currentSession.PromptTokens = 0
	currentSession.CompletionTokens = int64(skills.ApproxTokenCount(summaryText))
	if _, err := a.sessions.Save(ctx, currentSession); err != nil {
		return false, err
	}
	finishedAt := time.Now()
	a.appendConversationCompactionTrace(ctx, agentruntime.TraceKindConversationCompactionFinished, compactionTraceOptions{
		startedAt:                     compactionStartedAt,
		finishedAt:                    finishedAt,
		currentModel:                  model,
		currentSession:                currentSession,
		sourceMessages:                len(msgs),
		contextBytes:                  jsonSize(msgs),
		preflightEstimatedInputTokens: estimateSummaryMessageTokens(msgs, model.CatwalkCfg.SupportsImages),
		contextWindowTokens:           resolvedModelContextWindow(model),
		maxOutputTokens:               sessionMemoryMaxOutput,
		outputBytes:                   len(summaryText),
		outputTokens:                  int64(skills.ApproxTokenCount(summaryText)),
		totalTokens:                   int64(skills.ApproxTokenCount(summaryText)),
	})
	slog.Debug("Session memory compaction finished", "session", sessionID, "summary_message", summaryMessage.ID)
	return true, nil
}

func buildRecentSessionMemoryContext(msgs []message.Message, lastMessageID string) string {
	if lastMessageID == "" {
		return tailSessionMessages(msgs)
	}
	for i, msg := range msgs {
		if msg.ID == lastMessageID {
			return tailSessionMessages(msgs[i+1:])
		}
	}
	return tailSessionMessages(msgs)
}

func tailSessionMessages(msgs []message.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		switch msg.Role {
		case message.User:
			if text := strings.TrimSpace(msg.Content().Text); text != "" {
				fmt.Fprintf(&b, "\n[user]\n%s\n", text)
			}
		case message.Assistant:
			if text := strings.TrimSpace(msg.Content().Text); text != "" {
				fmt.Fprintf(&b, "\n[assistant]\n%s\n", text)
			}
			for _, tc := range msg.ToolCalls() {
				fmt.Fprintf(&b, "\n[tool_call %s]\n%s\n", tc.Name, tc.Input)
			}
		case message.Tool:
			for _, tr := range msg.ToolResults() {
				content := tr.Content
				if len(content) > 2000 {
					content = content[:2000] + "\n[tool result truncated]"
				}
				fmt.Fprintf(&b, "\n[tool_result %s]\n%s\n", tr.Name, content)
			}
		}
		if b.Len() > sessionMemoryRecentBytes {
			s := b.String()
			return s[len(s)-sessionMemoryRecentBytes:]
		}
	}
	return strings.TrimSpace(b.String())
}
