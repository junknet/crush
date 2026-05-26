package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	crushlog "github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/message"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
)

type llmTraceObserver struct {
	mu          sync.Mutex
	ctx         context.Context
	requestID   string
	httpTraceID string
	attempt     int
	startedAt   time.Time
	step        int
	started     bool
	finished    bool
	firstEvent  bool
	firstText   bool
	metrics     llmRequestMetrics
}

type llmRequestMetrics struct {
	contextMessageCount           int
	contextBytes                  int
	preflightEstimatedInputTokens int64
	contextWindowTokens           int64
	autoSummarizeThresholdRatio   float64
	autoSummarizeThresholdTokens  int64
	attachmentCount               int
	fileCount                     int
	toolCount                     int
	toolSchemaBytes               int
	maxOutputTokens               int64
}

type llmUsageAudit struct {
	autoSummarizeUsedTokens int64
	autoSummarizeTriggered  bool
}

func newLLMTraceObserver(ctx context.Context, requestID string, attempt int) *llmTraceObserver {
	return &llmTraceObserver{
		ctx:         ctx,
		requestID:   requestID,
		httpTraceID: crushlog.TraceIDFromContext(ctx),
		attempt:     attempt,
	}
}

func (o *llmTraceObserver) start(step int, metrics llmRequestMetrics) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.step = step
	o.startedAt = time.Now()
	o.started = true
	o.finished = false
	o.firstEvent = false
	o.firstText = false
	o.metrics = metrics
	entry := o.baseEntryLocked(agentruntime.TraceKindLLMStarted, "streaming")
	entry.Success = false
	o.mu.Unlock()
	tools.AppendTraceFromContext(o.ctx, entry)
}

func (o *llmTraceObserver) recordFirstEvent(eventType string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if !o.started || o.firstEvent {
		o.mu.Unlock()
		return
	}
	o.firstEvent = true
	entry := o.baseEntryLocked(agentruntime.TraceKindLLMFirstEvent, "first_event")
	entry.Success = true
	entry.FirstEventType = eventType
	entry.FirstEventLatencyMs = time.Since(o.startedAt).Milliseconds()
	o.mu.Unlock()
	tools.AppendTraceFromContext(o.ctx, entry)
}

func (o *llmTraceObserver) recordFirstText() {
	if o == nil {
		return
	}
	o.mu.Lock()
	if !o.started || o.firstText {
		o.mu.Unlock()
		return
	}
	o.firstText = true
	entry := o.baseEntryLocked(agentruntime.TraceKindLLMFirstText, "first_text_delta")
	entry.Success = true
	entry.FirstTextLatencyMs = time.Since(o.startedAt).Milliseconds()
	o.mu.Unlock()
	tools.AppendTraceFromContext(o.ctx, entry)
}

func (o *llmTraceObserver) retry(err *fantasy.ProviderError, delay time.Duration) {
	if o == nil || err == nil {
		return
	}
	o.mu.Lock()
	if !o.started {
		o.mu.Unlock()
		return
	}
	entry := o.baseEntryLocked(agentruntime.TraceKindLLMRetry, "retrying")
	entry.Success = false
	entry.DurationMs = time.Since(o.startedAt).Milliseconds()
	entry.RetryDelayMs = delay.Milliseconds()
	entry.Error = err.Error()
	o.mu.Unlock()
	tools.AppendTraceFromContext(o.ctx, entry)
}

func (o *llmTraceObserver) finish(finishReason string, usage fantasy.Usage, audit llmUsageAudit) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if !o.started || o.finished {
		o.mu.Unlock()
		return
	}
	o.finished = true
	entry := o.baseEntryLocked(agentruntime.TraceKindLLMFinished, "completed")
	entry.Success = true
	entry.FinishedAt = time.Now()
	entry.DurationMs = entry.FinishedAt.Sub(o.startedAt).Milliseconds()
	entry.FinishReason = finishReason
	entry.InputTokens = usage.InputTokens
	entry.OutputTokens = usage.OutputTokens
	entry.TotalTokens = usage.TotalTokens
	entry.ReasoningTokens = usage.ReasoningTokens
	entry.CacheCreationTokens = usage.CacheCreationTokens
	entry.CacheReadTokens = usage.CacheReadTokens
	entry.AutoSummarizeUsedTokens = audit.autoSummarizeUsedTokens
	entry.AutoSummarizeTriggered = audit.autoSummarizeTriggered
	o.mu.Unlock()
	tools.AppendTraceFromContext(o.ctx, entry)
}

func (o *llmTraceObserver) fail(err error) {
	if o == nil || err == nil {
		return
	}
	o.mu.Lock()
	if !o.started || o.finished {
		o.mu.Unlock()
		return
	}
	o.finished = true
	entry := o.baseEntryLocked(agentruntime.TraceKindLLMFailed, "failed")
	entry.Success = false
	entry.FinishedAt = time.Now()
	entry.DurationMs = entry.FinishedAt.Sub(o.startedAt).Milliseconds()
	entry.Error = err.Error()
	o.mu.Unlock()
	tools.AppendTraceFromContext(o.ctx, entry)
}

func (o *llmTraceObserver) baseEntryLocked(kind agentruntime.TraceKind, status string) agentruntime.TaskTrace {
	return agentruntime.TaskTrace{
		StartedAt:                     o.startedAt,
		Kind:                          kind,
		Status:                        status,
		RequestID:                     o.requestID,
		HTTPTraceID:                   o.httpTraceID,
		Attempt:                       o.attempt,
		StepNumber:                    o.step,
		ContextMessageCount:           o.metrics.contextMessageCount,
		ContextBytes:                  o.metrics.contextBytes,
		PreflightEstimatedInputTokens: o.metrics.preflightEstimatedInputTokens,
		ContextWindowTokens:           o.metrics.contextWindowTokens,
		AutoSummarizeThresholdRatio:   o.metrics.autoSummarizeThresholdRatio,
		AutoSummarizeThresholdTokens:  o.metrics.autoSummarizeThresholdTokens,
		AttachmentCount:               o.metrics.attachmentCount,
		FileCount:                     o.metrics.fileCount,
		ToolCount:                     o.metrics.toolCount,
		ToolSchemaBytes:               o.metrics.toolSchemaBytes,
		MaxOutputTokens:               o.metrics.maxOutputTokens,
	}
}

func buildLLMRequestMetrics(messages []fantasy.Message, agentTools []fantasy.AgentTool, files []fantasy.FilePart, attachments []message.Attachment, contextWindowTokens int64, maxOutputTokens int64) llmRequestMetrics {
	contextBytes := jsonSize(messages)
	toolSchemaBytes := 0
	for _, tool := range agentTools {
		toolSchemaBytes += jsonSize(tool.Info())
	}
	return llmRequestMetrics{
		contextMessageCount:           len(messages),
		contextBytes:                  contextBytes,
		preflightEstimatedInputTokens: estimateTokensFromBytes(contextBytes + toolSchemaBytes),
		contextWindowTokens:           contextWindowTokens,
		autoSummarizeThresholdRatio:   autoSummarizeUsedRatio,
		autoSummarizeThresholdTokens:  autoSummarizeThresholdTokens(contextWindowTokens),
		attachmentCount:               len(attachments),
		fileCount:                     len(files),
		toolCount:                     len(agentTools),
		toolSchemaBytes:               toolSchemaBytes,
		maxOutputTokens:               maxOutputTokens,
	}
}

func jsonSize(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(data)
}

func estimateTokensFromBytes(byteCount int) int64 {
	if byteCount <= 0 {
		return 0
	}
	return int64((byteCount + 3) / 4)
}
