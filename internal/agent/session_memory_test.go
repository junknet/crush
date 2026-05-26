package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
)

func TestShouldUpdateSessionMemory(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do work"}}},
		{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "done"}}},
	}
	if shouldUpdateSessionMemory(msgs, sessionMemoryState{}, sessionMemoryInitTokens-1) {
		t.Fatal("should not initialize below token threshold")
	}
	if !shouldUpdateSessionMemory(msgs, sessionMemoryState{}, sessionMemoryInitTokens) {
		t.Fatal("should initialize at token threshold on natural break")
	}

	toolMsgs := []message.Message{
		{ID: "a0", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "1", Name: "rg"},
			message.ToolCall{ID: "2", Name: "view"},
			message.ToolCall{ID: "3", Name: "edit"},
		}},
	}
	state := sessionMemoryState{Initialized: true, TokensAtLastUpdate: 10_000}
	if !shouldUpdateSessionMemory(toolMsgs, state, 15_000) {
		t.Fatal("should update after enough token growth and tool calls")
	}
	if shouldUpdateSessionMemory(toolMsgs, state, 14_999) {
		t.Fatal("should not update before token growth threshold")
	}
}

func TestSessionMemoryHasContent(t *testing.T) {
	t.Parallel()

	if sessionMemoryHasContent(sessionMemoryTemplate) {
		t.Fatal("template-only notes should be empty")
	}
	if !sessionMemoryHasContent(sessionMemoryTemplate + "\nUse `go test ./...` before handoff.\n") {
		t.Fatal("notes with real content should be non-empty")
	}
}

func TestBuildRecentSessionMemoryContext(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{ID: "old", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "old prompt"}}},
		{ID: "new", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "new answer"}}},
	}
	got := buildRecentSessionMemoryContext(msgs, "old")
	if got == "" || got == "old prompt" {
		t.Fatalf("expected only recent context after boundary, got %q", got)
	}
}
