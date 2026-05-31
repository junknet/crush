package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/shell"
)

//go:embed templates/agent_tool.md
var agentToolDescription string

type AgentParams struct {
	Prompt          string `json:"prompt" description:"要执行的任务描述"`
	Role            string `json:"role,omitempty" jsonschema:"description=agent role,enum=explore,enum=plan,enum=worker,enum=auditor" description:"agent 角色：explore（只读仓库检查）、plan（只读实现设计）、worker（实现/修复代码）、auditor（安全/数学对抗审查）。默认 explore。"`
	RunInBackground bool   `json:"run_in_background,omitempty" description:"设为 true 时立即返回 agent_job_id，不阻塞 brain agent。用 Monitor(shell_id=agent_job_id) 等待完成，用 JobOutput(shell_id=agent_job_id) 取结果。适合耗时长的 worker 任务。"`
}

const (
	AgentToolName = "Agent"
)

// agentTool builds the delegation tool. When allowedRoles is non-empty the tool
// rejects any role outside that set — used to confine the read-only plan agent
// to spawning explore sub-agents only, so it cannot fan out to mutating roles.
func (c *coordinator) agentTool(ctx context.Context, allowedRoles ...string) (fantasy.AgentTool, error) {
	return fantasy.NewParallelAgentTool(
		AgentToolName,
		agentToolDescription,
		func(ctx context.Context, params AgentParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			profile, role, err := resolveAgentToolRole(params.Role)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			if len(allowedRoles) > 0 && !slices.Contains(allowedRoles, role) {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Agent role %q is not permitted here; allowed roles: %s", role, strings.Join(allowedRoles, ", "))), nil
			}

			agent := c.agentForProfile(profile)
			if agent == nil {
				return fantasy.ToolResponse{}, fmt.Errorf("%s agent not configured", role)
			}

			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			if params.RunInBackground {
				return c.runSubAgentBackground(ctx, sessionID, agentMessageID, call.ID, role, profile, agent, params.Prompt)
			}

			return c.runSubAgent(ctx, subAgentParams{
				Agent:          agent,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         params.Prompt,
				Profile:        profile,
				SessionTitle:   agentSessionTitle(role),
			})
		},
	), nil
}

// runSubAgentBackground spawns a sub-agent in a goroutine and returns
// immediately with an agent_job_id. The job completion is published on the
// shared backgroundBroker so the brain's event loop wakes up exactly as it
// does for a finished bash background job. Brain can also attach a monitor.
func (c *coordinator) runSubAgentBackground(
	ctx context.Context,
	sessionID, agentMessageID, callID, role string,
	profile scheduler.WorkerProfile,
	agent SessionAgent,
	prompt string,
) (fantasy.ToolResponse, error) {
	// Reuse the global idCounter so job IDs are globally unique and
	// unambiguous alongside bash job IDs.
	jobID := fmt.Sprintf("%03X", shell.NextJobID())
	description := fmt.Sprintf("agent(%s): %s", role, truncateDesc(prompt, 60))

	// Register the sub-agent as a virtual background job so its agent_job_id is
	// resolvable by Monitor / JobOutput / JobKill exactly like a bash bg shell —
	// the advertised polling path below only works because of this registration
	// (previously the ID was never in the manager, so it failed with
	// "background shell not found"). runCtx is detached from the caller (must
	// survive this tool returning) but cancelable via JobKill / session reaping.
	runCtx, cancel := context.WithCancel(context.Background())
	bgShell := c.bgManager.RegisterVirtualJob(jobID, description, sessionID, cancel)
	dataDir := c.cfg.Config().Options.DataDirectory

	go func() {
		result, err := c.runSubAgent(runCtx, subAgentParams{
			Agent:          agent,
			SessionID:      sessionID,
			AgentMessageID: agentMessageID,
			ToolCallID:     callID,
			Prompt:         prompt,
			Profile:        profile,
			SessionTitle:   agentSessionTitle(role),
		})

		output := ""
		status := "completed"
		exitCode := 0
		if err != nil {
			output = fmt.Sprintf("error: %v", err)
			exitCode = 1
		} else {
			output = result.Content
		}
		// Persist the terminal result BEFORE Complete so JobOutput can recover
		// it even if the process restarts after the worker finishes but before
		// the brain collects it (the resume case where the live manager is
		// empty). Best-effort; failure must not block completion.
		if perr := tools.PersistAgentJobResult(dataDir, sessionID, jobID, status, output, exitCode); perr != nil {
			slog.Warn("Failed to persist agent job result", "job", jobID, "error", perr)
		}
		// Drives maybePublishDone → the same auto-wake the agent loop already
		// listens for, and makes job_output(shell_id) return the result.
		bgShell.Complete(output, err)
	}()

	return fantasy.NewTextResponse(fmt.Sprintf(
		"agent_job_id: %s\n角色: %s\n状态: 后台运行中\n\n任务已在后台启动，完成后会自动通知你继续（并带回输出摘要）。也可用 monitor(shell_id=%s) 等待完成、job_output(shell_id=%s) 查看输出、job_kill(shell_id=%s) 终止。",
		jobID, role, jobID, jobID, jobID,
	)), nil
}

func truncateDesc(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func resolveAgentToolRole(role string) (scheduler.WorkerProfile, string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", config.AgentExplore:
		return scheduler.ProfileExploreAgent, config.AgentExplore, nil
	case config.AgentPlan:
		return scheduler.ProfilePlanAgent, config.AgentPlan, nil
	case config.AgentWorker:
		return scheduler.ProfileWorkerAgent, config.AgentWorker, nil
	case config.AgentAuditor:
		return scheduler.ProfileAuditorAgent, config.AgentAuditor, nil
	default:
		return "", "", fmt.Errorf("unknown agent role %q", role)
	}
}

func agentSessionTitle(role string) string {
	switch role {
	case config.AgentPlan:
		return "Plan Agent Session"
	case config.AgentWorker:
		return "Worker Agent Session"
	case config.AgentAuditor:
		return "Auditor Agent Session"
	default:
		return "Explore Agent Session"
	}
}
