package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	JobOutputToolName = "job_output"
	// jobOutputWaitBudget bounds wait=true below the 60s tool-execution timeout
	// so the call returns the current output + running status gracefully instead
	// of blocking until the timeout wrapper kills it and discards the output —
	// the single most frequent job_output failure in real traces (140 hard
	// timeouts). For longer waits the model polls again or uses monitor.
	jobOutputWaitBudget = 50 * time.Second
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
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
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
