package agent

import (
	"context"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
)

type tracedTool struct {
	inner fantasy.AgentTool
}

func newTracedTool(inner fantasy.AgentTool) *tracedTool {
	return &tracedTool{inner: inner}
}

func wrapToolsWithTrace(agentTools []fantasy.AgentTool) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(agentTools))
	for i, tool := range agentTools {
		out[i] = newTracedTool(tool)
	}
	return out
}

func (t *tracedTool) Info() fantasy.ToolInfo {
	return t.inner.Info()
}

func (t *tracedTool) ProviderOptions() fantasy.ProviderOptions {
	return t.inner.ProviderOptions()
}

func (t *tracedTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.inner.SetProviderOptions(opts)
}

func (t *tracedTool) SetParallel(parallel bool) {
	if setter, ok := t.inner.(interface{ SetParallel(bool) }); ok {
		setter.SetParallel(parallel)
	}
}

func (t *tracedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	startedAt := time.Now()
	info := t.inner.Info()
	toolName := call.Name
	if toolName == "" {
		toolName = info.Name
	}
	tools.AppendTraceFromContext(ctx, agentruntime.TaskTrace{
		StartedAt:      startedAt,
		Kind:           agentruntime.TraceKindToolStarted,
		Status:         "running",
		Success:        false,
		ToolName:       toolName,
		ToolCallID:     call.ID,
		ToolInput:      call.Input,
		ToolInputBytes: len(call.Input),
		ToolCount:      1,
	})

	resp, err := t.inner.Run(ctx, call)
	finishedAt := time.Now()
	success := err == nil && !resp.IsError
	kind := agentruntime.TraceKindToolFinished
	status := "completed"
	errorText := ""
	if !success {
		kind = agentruntime.TraceKindToolFailed
		status = "failed"
		if err != nil {
			errorText = err.Error()
		} else {
			errorText = resp.Content
		}
	}
	outputBytes := len(resp.Content) + len(resp.Data)
	tools.AppendTraceFromContext(ctx, agentruntime.TaskTrace{
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		DurationMs:      finishedAt.Sub(startedAt).Milliseconds(),
		Kind:            kind,
		Status:          status,
		Success:         success,
		Error:           errorText,
		Output:          resp.Content,
		OutputBytes:     outputBytes,
		ToolName:        toolName,
		ToolCallID:      call.ID,
		ToolInput:       call.Input,
		ToolOutput:      resp.Content,
		ToolInputBytes:  len(call.Input),
		ToolOutputBytes: outputBytes,
		ToolIsError:     resp.IsError,
		ToolStopTurn:    resp.StopTurn,
	})
	return resp, err
}
