package tools

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/iodriver"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	MonitorToolName = "Monitor"
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
			if iodriver.IsRemoteJobID(params.ShellID) {
				backend := GetBackendFromContext(ctx)
				jobber, ok := backend.(iodriver.Jobber)
				if !ok || jobber == nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("remote background shell not available: %s", params.ShellID)), nil
				}
				if _, err := regexp.Compile(params.Pattern); err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid pattern: %v", err)), nil
				}
				go monitorRemoteJob(context.Background(), jobber, params.ShellID, sessionID, params.Pattern, time.Duration(timeoutSeconds)*time.Second)
				metadata := MonitorResponseMetadata{
					ShellID:        params.ShellID,
					Pattern:        params.Pattern,
					TimeoutSeconds: timeoutSeconds,
				}
				response := fmt.Sprintf(
					"Monitoring job %s for pattern %q. This turn will end now; you'll be automatically woken when the pattern matches, the job ends, or %ds elapse. Do not poll.",
					params.ShellID, params.Pattern, timeoutSeconds)
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
			}

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

func monitorRemoteJob(ctx context.Context, jobber iodriver.Jobber, shellID, sessionID, pattern string, timeout time.Duration) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return
	}
	markRemoteMonitor(shellID)
	defer unmarkRemoteMonitor(shellID)
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(time.Second)
	defer deadline.Stop()
	defer ticker.Stop()
	seenLines := 0
	for {
		select {
		case <-ticker.C:
			snapshot, err := jobber.JobOutput(ctx, shellID)
			if err != nil {
				return
			}
			combined := strings.TrimSpace(string(snapshot.Stdout) + "\n" + string(snapshot.Stderr))
			lines := monitorSplitNonEmptyLines(combined)
			for _, line := range lines[seenLines:] {
				if re.MatchString(line) {
					shell.PublishBackgroundDone(shell.BackgroundJobEvent{
						Kind:        shell.BackgroundKindMonitorHit,
						ID:          shellID,
						SessionID:   sessionID,
						Command:     snapshot.Command,
						Description: snapshot.Description,
						Pattern:     pattern,
						MatchLine:   line,
					})
					return
				}
			}
			seenLines = len(lines)
			if snapshot.Done {
				shell.PublishBackgroundDone(shell.BackgroundJobEvent{
					Kind:        shell.BackgroundKindMonitorEOF,
					ID:          shellID,
					SessionID:   sessionID,
					Command:     snapshot.Command,
					Description: snapshot.Description,
					ExitCode:    snapshot.ExitCode,
					OutputTail:  backgroundTail(string(snapshot.Stdout), string(snapshot.Stderr)),
					Pattern:     pattern,
				})
				return
			}
		case <-deadline.C:
			shell.PublishBackgroundDone(shell.BackgroundJobEvent{
				Kind:      shell.BackgroundKindMonitorTimeout,
				ID:        shellID,
				SessionID: sessionID,
				Pattern:   pattern,
			})
			return
		case <-ctx.Done():
			return
		}
	}
}

func monitorSplitNonEmptyLines(s string) []string {
	raw := strings.Split(s, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
