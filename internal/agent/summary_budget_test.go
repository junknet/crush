package agent

import (
	"errors"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestSelectSummaryMessagesForBudgetKeepsNewestSuffix(t *testing.T) {
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first message"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "second message"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "third message"}}},
	}

	selected, trimmed, estimated := selectSummaryMessagesForBudget(msgs, true, 1)
	require.True(t, trimmed)
	require.Len(t, selected, 1)
	require.Equal(t, "third message", selected[0].Content().Text)
	require.Greater(t, estimated, int64(0))
}

func TestBuildSummaryPromptWithPartialContext(t *testing.T) {
	base := buildSummaryPromptWithPartialContext(nil, false)
	trimmed := buildSummaryPromptWithPartialContext([]session.Todo{}, true)

	require.Contains(t, base, "Compress the conversation into durable memory for the next agent.")
	require.Contains(t, base, "Preserve the minimum state needed to resume without rereading the whole transcript")
	require.Contains(t, base, "Separate durable memory from session-local execution state")
	require.Contains(t, base, "Keep confirmed facts, decisions, constraints, file paths, commands run, verification results, unresolved questions, and todo statuses.")
	require.NotContains(t, base, "trimmed to fit the model window")
	require.Contains(t, trimmed, "trimmed to fit the model window")
}

func TestIsSummaryContextTooLargeError(t *testing.T) {
	err := &fantasy.ProviderError{
		StatusCode: 400,
		Message:    "The input token count exceeds the maximum number of tokens allowed 1048576.",
	}
	require.True(t, isSummaryContextTooLargeError(err))
	require.False(t, isSummaryContextTooLargeError(errors.New("plain error")))
}

func TestSummaryTokenBudgets(t *testing.T) {
	model := catwalk.Model{
		ContextWindow:    100_000,
		DefaultMaxTokens: 1_000,
	}
	require.Equal(t, int64(4_096), summarizeOutputTokenBudget(model))
	require.Equal(t, int64(100_000-4_096-8_192), summarizeInputTokenBudget(model))
}
