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
			return fantasy.NewTextErrorResponse(errMsg), nil
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
	case "bash", "bash_tool", "nu", "nu_tool", "ssh", "ssh_tool":
		return true
	default:
		return false
	}
}

// wrapToolsWithTimeout wraps allowed tools with a default timeout, excluding delegation tools.
func wrapToolsWithTimeout(tools []fantasy.AgentTool, timeout time.Duration) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		name := tool.Info().Name
		// remote_attach deploys the daemon (an ~80MB scp on first use) and
		// opens an SSH channel; that legitimately exceeds the quick-tool
		// timeout, so it (and its cheap sibling detach) run unwrapped, like the
		// delegation tools. The ctx still cancels on session cancel.
		// "remote_attach"/"remote_detach" are tools.RemoteAttach/DetachToolName;
		// the literal is used because the `tools` param shadows the package here.
		// "run" self-times via its own timeout_seconds param (default 60, max
		// 300) with a graceful "run timed out" message; the outer 60s wrapper
		// would otherwise hard-kill it at 60s and make its advertised 300s max a
		// lie (12 such premature timeouts in real traces).
		if name == AgentToolName || name == "agentic_fetch" || name == "agent_tool" || name == "agentic_fetch_tool" ||
			name == "remote_attach" || name == "remote_detach" ||
			name == "run" || name == "run_tool" {
			out[i] = tool
		} else {
			out[i] = newTimeoutTool(tool, timeout)
		}
	}
	return out
}
