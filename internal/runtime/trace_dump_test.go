package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteTraceJSONLFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	traceFile := filepath.Join(dir, "trace", "run.jsonl")

	traces := []TaskTrace{
		{
			Sequence:              1,
			RecordedAt:            time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
			StartedAt:             time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
			FinishedAt:            time.Date(2026, 5, 19, 10, 0, 1, 0, time.UTC),
			DurationMs:            1000,
			ConversationSessionID: "session-1",
			SessionID:             "session-1",
			NodeID:                "node-1",
			ProviderID:            "waitai",
			ProviderType:          "anthropic",
			ModelID:               "claude-opus-4-7",
			Kind:                  TraceKindTaskInput,
			Status:                "dispatching",
			Goal:                  "plan the work",
			Input:                 "prompt text",
			InputBytes:            len("prompt text"),
		},
		{
			Sequence:                     2,
			RecordedAt:                   time.Date(2026, 5, 19, 10, 1, 0, 0, time.UTC),
			ConversationSessionID:        "session-1",
			SessionID:                    "session-1",
			NodeID:                       "node-1",
			Kind:                         TraceKindTaskOutput,
			Status:                       "completed",
			Goal:                         "plan the work",
			Output:                       "result text",
			Success:                      true,
			InputTokens:                  10,
			OutputTokens:                 5,
			EstimatedCostUSD:             0.001,
			ContextWindowTokens:          1_000_000,
			AutoSummarizeThresholdRatio:  0.70,
			AutoSummarizeThresholdTokens: 700_000,
			AutoSummarizeUsedTokens:      500_000,
		},
	}

	require.NoError(t, WriteTraceJSONLFile(traceFile, traces))

	data, err := os.ReadFile(traceFile)
	require.NoError(t, err)

	lines := splitNonEmptyLines(string(data))
	require.Len(t, lines, 2)

	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Equal(t, "session-1:node-1:task_input", first["trace_key"])
	require.Equal(t, float64(1), first["sequence"])
	require.Equal(t, "task_input", first["kind"])
	require.Equal(t, "prompt text", first["input"])
	require.Equal(t, "waitai", first["provider_id"])
	require.Equal(t, "claude-opus-4-7", first["model_id"])
	require.Equal(t, float64(1000), first["duration_ms"])
	require.Equal(t, float64(len("prompt text")), first["input_bytes"])

	var second map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	require.Equal(t, "task_output", second["kind"])
	require.Equal(t, "result text", second["output"])
	require.Equal(t, float64(10), second["input_tokens"])
	require.Equal(t, float64(5), second["output_tokens"])
	require.Equal(t, 0.001, second["estimated_cost_usd"])
	require.Equal(t, float64(1_000_000), second["context_window_tokens"])
	require.Equal(t, 0.70, second["auto_summarize_threshold_ratio"])
	require.Equal(t, float64(700_000), second["auto_summarize_threshold_tokens"])
	require.Equal(t, float64(500_000), second["auto_summarize_used_tokens"])
}

func TestTraceJSONLFileWriterAppendsImmediately(t *testing.T) {
	t.Parallel()

	traceFile := filepath.Join(t.TempDir(), "trace", "live.jsonl")
	writer, err := NewTraceJSONLFileWriter(traceFile)
	require.NoError(t, err)

	require.NoError(t, writer.Append(TaskTrace{
		Sequence:              1,
		RecordedAt:            time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
		ConversationSessionID: "session-1",
		SessionID:             "session-1",
		NodeID:                "node-1",
		Kind:                  TraceKindLLMStarted,
		Status:                "streaming",
		ContextWindowTokens:   1_048_576,
	}))
	require.NoError(t, writer.Append(TaskTrace{
		Sequence:              1,
		RecordedAt:            time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
		ConversationSessionID: "session-1",
		SessionID:             "session-1",
		NodeID:                "node-1",
		Kind:                  TraceKindLLMStarted,
		Status:                "streaming",
		ContextWindowTokens:   1_048_576,
	}))

	data, err := os.ReadFile(traceFile)
	require.NoError(t, err)
	lines := splitNonEmptyLines(string(data))
	require.Len(t, lines, 1)

	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Equal(t, "llm_request_started", first["kind"])
	require.Equal(t, float64(1_048_576), first["context_window_tokens"])

	require.NoError(t, writer.Close())
	require.Error(t, writer.Append(TaskTrace{Kind: TraceKindTaskOutput}))
}

func splitNonEmptyLines(content string) []string {
	var lines []string
	for _, line := range splitLines(content) {
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func splitLines(content string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}
