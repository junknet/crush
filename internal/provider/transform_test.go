package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAnthropicRequest(t *testing.T) {
	t.Parallel()

	request, err := BuildRequest(
		Adapter{ID: "claude", Protocol: ProtocolAnthropic, OutputLimit: 4096},
		RequestIntent{ThinkingBudget: BudgetMedium, MaxOutputTokens: 2048, ToolMode: ToolModeAuto},
		"system prompt",
		[]map[string]any{{"role": "user", "content": "hello"}},
		[]map[string]any{{"name": "read"}},
	)
	require.NoError(t, err)
	require.Equal(t, "claude", request["model"])
	require.Equal(t, 2048, request["max_tokens"])
	require.Contains(t, request, "thinking")
	require.Contains(t, request, "system")
	require.Contains(t, request, "tools")
	require.Contains(t, request, "tool_choice")
}

func TestBuildOpenAIChatRequest(t *testing.T) {
	t.Parallel()

	request, err := BuildRequest(
		Adapter{ID: "gpt", Protocol: ProtocolOpenAIChat, OutputLimit: 2048},
		RequestIntent{ThinkingBudget: BudgetLow, ToolMode: ToolModeAny},
		"system prompt",
		[]map[string]any{{"role": "user", "content": "hello"}},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "gpt", request["model"])
	require.Equal(t, true, request["stream"])
	require.Contains(t, request, "reasoning_effort")
	require.Equal(t, "required", request["tool_choice"])
}

func TestResponsesInputSkipsEmptyMessages(t *testing.T) {
	t.Parallel()

	input := responsesInput([]map[string]any{
		{"role": "user", "content": ""},
		{"role": "user", "content": []map[string]any{}},
		{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "hello"}}},
	})

	require.Len(t, input, 1)
	require.Equal(t, "user", input[0]["role"])
	require.NotEmpty(t, input[0]["content"])
}

func TestFilterEmptyUserMessagesSkipsEmptyUserContent(t *testing.T) {
	t.Parallel()

	messages := filterEmptyUserMessages([]map[string]any{
		{"role": "system", "content": ""},
		{"role": "user", "content": ""},
		{"role": "user", "content": []map[string]any{}},
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": ""},
	})

	require.Len(t, messages, 3)
	require.Equal(t, "system", messages[0]["role"])
	require.Equal(t, "user", messages[1]["role"])
	require.Equal(t, "assistant", messages[2]["role"])
}
