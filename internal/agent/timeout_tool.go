package agent

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"charm.land/fantasy"
)

const defaultForegroundAgentToolTimeout = 2 * time.Minute

func foregroundAgentToolTimeout() time.Duration {
	value := os.Getenv("CRUSH_AGENT_FOREGROUND_TIMEOUT_SECONDS")
	if value == "" {
		return defaultForegroundAgentToolTimeout
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return defaultForegroundAgentToolTimeout
	}
	return time.Duration(seconds) * time.Second
}

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
		// RemoteAttach deploys the daemon (an ~80MB scp on first use) and
		// opens an SSH channel; that legitimately exceeds the quick-tool
		// timeout, so it (and its cheap sibling detach) run unwrapped, like the
		// delegation tools. The ctx still cancels on session cancel.
		// "RemoteAttach"/"RemoteDetach" are tools.RemoteAttach/DetachToolName;
		// the literal is used because the `tools` param shadows the package here.
		if name == AgentToolName {
			out[i] = newTimeoutTool(tool, foregroundAgentToolTimeout())
		} else if name == "websearch-agent" ||
			name == "RemoteAttach" || name == "RemoteDetach" ||
			name == "Bash" ||
			name == "JobOutput" || name == "JobKill" || name == "Monitor" ||
			name == "Search" || name == "Batch" {
			out[i] = tool
		} else {
			out[i] = newTimeoutTool(tool, timeout)
		}
	}
	return out
}
