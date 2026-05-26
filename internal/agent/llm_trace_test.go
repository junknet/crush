package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	crushlog "github.com/charmbracelet/crush/internal/log"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestLLMTraceObserverRecordsLifecycle(t *testing.T) {
	t.Parallel()

	runtime := agentruntime.NewSession("/tmp/project", nil)
	runtime.BindSession("conversation-1")
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "session-1")
	ctx = tools.WithTraceContext(ctx, runtime, "node-1", "parent-1", "brain_agent", "provider-1", "provider-type", "model-1")
	ctx = crushlog.WithTraceID(ctx, "http-trace-1")

	observer := newLLMTraceObserver(ctx, "request-1", 2)
	observer.start(3, llmRequestMetrics{
		contextMessageCount:           4,
		contextBytes:                  1200,
		preflightEstimatedInputTokens: 300,
		contextWindowTokens:           1_000_000,
		autoSummarizeThresholdRatio:   0.70,
		autoSummarizeThresholdTokens:  700_000,
		attachmentCount:               1,
		fileCount:                     2,
		toolCount:                     5,
		toolSchemaBytes:               600,
		maxOutputTokens:               1000,
	})
	observer.recordFirstEvent("reasoning_delta")
	observer.recordFirstText()
	observer.finish("stop", fantasy.Usage{
		InputTokens:         11,
		OutputTokens:        7,
		TotalTokens:         18,
		ReasoningTokens:     3,
		CacheCreationTokens: 2,
		CacheReadTokens:     5,
	}, llmUsageAudit{
		autoSummarizeUsedTokens: 18,
	})

	traces := runtime.TraceEntries()
	require.Len(t, traces, 4)
	require.Equal(t, agentruntime.TraceKindLLMStarted, traces[0].Kind)
	require.Equal(t, agentruntime.TraceKindLLMFirstEvent, traces[1].Kind)
	require.Equal(t, agentruntime.TraceKindLLMFirstText, traces[2].Kind)
	require.Equal(t, agentruntime.TraceKindLLMFinished, traces[3].Kind)
	require.Equal(t, "request-1", traces[3].RequestID)
	require.Equal(t, "http-trace-1", traces[3].HTTPTraceID)
	require.Equal(t, 2, traces[3].Attempt)
	require.Equal(t, 3, traces[3].StepNumber)
	require.Equal(t, 4, traces[3].ContextMessageCount)
	require.Equal(t, int64(300), traces[3].PreflightEstimatedInputTokens)
	require.Equal(t, int64(1_000_000), traces[3].ContextWindowTokens)
	require.Equal(t, 0.70, traces[3].AutoSummarizeThresholdRatio)
	require.Equal(t, int64(700_000), traces[3].AutoSummarizeThresholdTokens)
	require.Equal(t, int64(18), traces[3].AutoSummarizeUsedTokens)
	require.Equal(t, 5, traces[3].ToolCount)
	require.Equal(t, int64(1000), traces[3].MaxOutputTokens)
	require.Equal(t, "stop", traces[3].FinishReason)
	require.Equal(t, int64(18), traces[3].TotalTokens)
	require.True(t, traces[3].Success)
}
