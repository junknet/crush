package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/iodriver"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	JobOutputToolName = "JobOutput"
	// jobOutputWaitBudget bounds wait=true below the 60s tool-execution timeout
	// so the call returns the current output + running status gracefully instead
	// of blocking until the timeout wrapper kills it and discards the output —
	// the single most frequent job_output failure in real traces (140 hard
	// timeouts). For longer waits the model polls again or uses monitor.
	jobOutputWaitBudget = 5 * time.Second
)

//go:embed job_output.md
var jobOutputDescription string

type JobOutputParams struct {
	ShellID string `json:"shell_id" description:"The ID of the background shell to retrieve output from"`
	Wait    bool   `json:"wait" description:"If true, block until the background shell completes before returning output"`
}

type JobOutputResponseMetadata struct {
	ShellID          string `json:"shell_id"`
	Command          string `json:"command"`
	Description      string `json:"description"`
	Done             bool   `json:"done"`
	WorkingDirectory string `json:"working_directory"`
}

func NewJobOutputTool(bgManager *shell.BackgroundShellManager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		JobOutputToolName,
		jobOutputDescription,
		func(ctx context.Context, params JobOutputParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			params.ShellID = sanitizeShellID(params.ShellID)
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
			}
			if iodriver.IsRemoteJobID(params.ShellID) {
				return remoteJobOutput(ctx, params)
			}

			bgShell, ok := bgManager.Get(params.ShellID)
			if !ok {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("background shell not found: %s", params.ShellID)), nil
			}

			if params.Wait {
				// Bounded wait: return gracefully with partial output if the job
				// outlives the budget, rather than blocking until the tool
				// timeout fires and the output is lost.
				waitCtx, cancel := context.WithTimeout(ctx, jobOutputWaitBudget)
				bgShell.WaitContext(waitCtx)
				cancel()
			}

			stdout, stderr, done, err := bgShell.GetOutput()

			var outputParts []string
			if stdout != "" {
				outputParts = append(outputParts, stdout)
			}
			if stderr != "" {
				outputParts = append(outputParts, stderr)
			}

			status := "running"
			if done {
				status = "completed"
				if err != nil {
					exitCode := shell.ExitCode(err)
					if exitCode != 0 {
						outputParts = append(outputParts, fmt.Sprintf("Exit code %d", exitCode))
					}
				}
			}

			output := strings.Join(outputParts, "\n")
			// Use head-only preview (BashPreviewBytes = 8 KB) rather than the
			// full 30 KB TruncateOutput cap. Background job logs are typically
			// repetitive INFO/HTTP lines; the head captures startup state and
			// the first error; the tail is usually noise. Keeps each job_output
			// call lean in the LLM context window.
			output = headPreview(output)

			metadata := JobOutputResponseMetadata{
				ShellID:          params.ShellID,
				Command:          bgShell.Command,
				Description:      bgShell.Description,
				Done:             done,
				WorkingDirectory: bgShell.WorkingDir,
			}

			if output == "" {
				output = BashNoOutput
			}

			result := fmt.Sprintf("Status: %s\n\n%s", status, output)
			if !done && params.Wait {
				// The job outlived the wait budget. Tell the model how to proceed
				// instead of leaving it to re-block (and time out) on another wait.
				result += fmt.Sprintf("\n\n[job %s still running after %s — call job_output again to poll, or use the monitor tool to wake on a completion/error pattern instead of blocking.]", params.ShellID, jobOutputWaitBudget)
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		},
	)
}

// sanitizeShellID strips trailing prose a model sometimes pastes after a
// background shell id (observed in traces: "045Sync wait is capped at 50s...").
// A shell id is a single whitespace-free token, so keep only the leading run up
// to the first separator and drop any <shell_id> tag wrapping. Applied at every
// tool that takes a model-supplied shell_id so contamination never reaches lookup.
func sanitizeShellID(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "<shell_id>")
	if i := strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '<'
	}); i >= 0 {
		s = s[:i]
	}
	return s
}

func remoteJobOutput(ctx context.Context, params JobOutputParams) (fantasy.ToolResponse, error) {
	backend := GetBackendFromContext(ctx)
	jobber, ok := backend.(iodriver.Jobber)
	if !ok || jobber == nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("remote background shell not available: %s", params.ShellID)), nil
	}

	if params.Wait {
		waitCtx, cancel := context.WithTimeout(ctx, jobOutputWaitBudget)
		defer cancel()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			snapshot, err := jobber.JobOutput(waitCtx, params.ShellID)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if snapshot.Done {
				return remoteJobOutputResponse(params.ShellID, snapshot, false), nil
			}
			select {
			case <-ticker.C:
			case <-waitCtx.Done():
				snapshot, _ = jobber.JobOutput(ctx, params.ShellID)
				return remoteJobOutputResponse(params.ShellID, snapshot, true), nil
			}
		}
	}

	snapshot, err := jobber.JobOutput(ctx, params.ShellID)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	return remoteJobOutputResponse(params.ShellID, snapshot, false), nil
}

func remoteJobOutputResponse(shellID string, snapshot iodriver.JobSnapshot, waitExpired bool) fantasy.ToolResponse {
	outputParts := make([]string, 0, 2)
	if len(snapshot.Stdout) > 0 {
		outputParts = append(outputParts, string(snapshot.Stdout))
	}
	if len(snapshot.Stderr) > 0 {
		outputParts = append(outputParts, string(snapshot.Stderr))
	}
	status := "running"
	if snapshot.Done {
		status = "completed"
		if snapshot.ExitCode != 0 {
			outputParts = append(outputParts, fmt.Sprintf("Exit code %d", snapshot.ExitCode))
		}
	}
	output := headPreview(strings.Join(outputParts, "\n"))
	if output == "" {
		output = BashNoOutput
	}
	result := fmt.Sprintf("Status: %s\n\n%s", status, output)
	if waitExpired {
		result += fmt.Sprintf("\n\n[job %s still running after %s — call job_output again to poll, or use the monitor tool to wake on a completion/error pattern instead of blocking.]", shellID, jobOutputWaitBudget)
	}
	metadata := JobOutputResponseMetadata{
		ShellID:          shellID,
		Command:          snapshot.Command,
		Description:      snapshot.Description,
		Done:             snapshot.Done,
		WorkingDirectory: snapshot.Cwd,
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata)
}
