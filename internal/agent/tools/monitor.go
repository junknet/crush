package tools

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	MonitorToolName = "monitor"
	// DefaultMonitorTimeoutSeconds bounds an unmatched watch so it eventually
	// wakes the agent to decide rather than watching forever.
	DefaultMonitorTimeoutSeconds = 300
)

//go:embed monitor.md
var monitorDescription string

type MonitorParams struct {
	ShellID        string `json:"shell_id" description:"The ID of the background shell (job) to watch"`
	Pattern        string `json:"pattern" description:"Go regular expression matched per output line; matching a new line wakes the agent"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"Max seconds to watch before waking the agent unmatched (default 300)"`
}

type MonitorResponseMetadata struct {
	ShellID        string `json:"shell_id"`
	Pattern        string `json:"pattern"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func NewMonitorTool(bgManager *shell.BackgroundShellManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MonitorToolName,
		monitorDescription,
		func(ctx context.Context, params MonitorParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
			}
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("missing pattern"), nil
			}

			timeoutSeconds := params.TimeoutSeconds
			if timeoutSeconds <= 0 {
				timeoutSeconds = DefaultMonitorTimeoutSeconds
			}

			sessionID := GetSessionFromContext(ctx)
			if err := bgManager.StartMonitor(params.ShellID, params.Pattern,
				time.Duration(timeoutSeconds)*time.Second, sessionID); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			metadata := MonitorResponseMetadata{
				ShellID:        params.ShellID,
				Pattern:        params.Pattern,
				TimeoutSeconds: timeoutSeconds,
			}
			response := fmt.Sprintf(
				"Monitoring job %s for pattern %q. This turn will end now; you'll be automatically woken when the pattern matches, the job ends, or %ds elapse. Do not poll.",
				params.ShellID, params.Pattern, timeoutSeconds)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		},
	)
}
