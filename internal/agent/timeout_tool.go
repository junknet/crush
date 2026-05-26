package agent

import (
	"context"
	"fmt"
	"time"

	"charm.land/fantasy"
)

// timeoutTool wraps a fantasy.AgentTool to enforce a maximum execution duration.
type timeoutTool struct {
	inner   fantasy.AgentTool
	timeout time.Duration
}

func newTimeoutTool(inner fantasy.AgentTool, timeout time.Duration) *timeoutTool {
	return &timeoutTool{inner: inner, timeout: timeout}
}

func (t *timeoutTool) Info() fantasy.ToolInfo {
	return t.inner.Info()
}

func (t *timeoutTool) ProviderOptions() fantasy.ProviderOptions {
	return t.inner.ProviderOptions()
}

func (t *timeoutTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.inner.SetProviderOptions(opts)
}

func (t *timeoutTool) SetParallel(parallel bool) {
	if setter, ok := t.inner.(interface{ SetParallel(bool) }); ok {
		setter.SetParallel(parallel)
	}
}

func (t *timeoutTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	type result struct {
		resp fantasy.ToolResponse
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		resp, err := t.inner.Run(timeoutCtx, call)
		ch <- result{resp: resp, err: err}
	}()

	select {
	case res := <-ch:
		return res.resp, res.err
	case <-timeoutCtx.Done():
		if timeoutCtx.Err() == context.DeadlineExceeded {
			errMsg := fmt.Sprintf("Tool %s execution timed out after %v. Long-running tasks must run in the background (e.g. using the background execution flag or background commands).", call.Name, t.timeout)
			return fantasy.NewTextErrorResponse(errMsg), nil
		}
		return fantasy.ToolResponse{}, timeoutCtx.Err()
	}
}

// wrapToolsWithTimeout wraps allowed tools with a default timeout, excluding delegation tools.
func wrapToolsWithTimeout(tools []fantasy.AgentTool, timeout time.Duration) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		name := tool.Info().Name
		if name == AgentToolName || name == "agentic_fetch" || name == "agent_tool" || name == "agentic_fetch_tool" {
			out[i] = tool
		} else {
			out[i] = newTimeoutTool(tool, timeout)
		}
	}
	return out
}
