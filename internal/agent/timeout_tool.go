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
			errMsg := toolTimeoutMessage(call.Name, t.timeout)
			resp := fantasy.NewTextErrorResponse(errMsg)
			if call.Name == AgentToolName {
				resp.StopTurn = true
			}
			return resp, nil
		}
		return fantasy.ToolResponse{}, timeoutCtx.Err()
	}
}

func toolTimeoutMessage(name string, timeout time.Duration) string {
	if isBackgroundCapableTool(name) {
		return fmt.Sprintf("Tool %s execution timed out after %v. Long-running commands must run in the background with the tool's background option.", name, timeout)
	}
	return fmt.Sprintf("Tool %s execution timed out after %v. This tool is expected to finish quickly; the operation was canceled instead of being converted to a background job.", name, timeout)
}

func isBackgroundCapableTool(name string) bool {
	switch name {
	case AgentToolName, "Bash":
		return true
	default:
		return false
	}
}

// wrapToolsWithTimeout wraps allowed tools with a default timeout.
func wrapToolsWithTimeout(tools []fantasy.AgentTool, timeout time.Duration) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		name := tool.Info().Name
		// Unwrapped tools. Agent (and its websearch sibling) is a full
		// event-driven agent loop, NOT a quick tool: brain dispatches it and
		// either awaits natural completion (foreground) or detaches and drives
		// it via its handle — agent_job_id + Monitor/JobOutput/JobKill
		// (wait/read/cancel). It must never be killed by a wall-clock tool
		// timeout; its own per-turn model-stream timeouts and session-cancel
		// provide liveness. Bash is unwrapped because it self-backgrounds a hung
		// command after a few seconds rather than being hard-killed here.
		// RemoteAttach deploys the daemon (an ~80MB scp on first use); that
		// legitimately exceeds the quick-tool timeout. The literals are used
		// because the `tools` param shadows the package here.
		if name == AgentToolName || name == "websearch-agent" ||
			name == "RemoteAttach" || name == "RemoteDetach" ||
			name == "Bash" ||
			name == "JobOutput" || name == "JobKill" || name == "Monitor" ||
			name == "Search" || name == "Grep" || name == "Find" || name == "Batch" {
			out[i] = tool
		} else {
			out[i] = newTimeoutTool(tool, timeout)
		}
	}
	return out
}
