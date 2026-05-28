package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
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
	Role            string `json:"role,omitempty" description:"agent 角色：explore（只读仓库检查）、plan（只读实现设计）、worker（实现/修复代码）、auditor（安全/数学对抗审查）。默认 explore。"`
	RunInBackground bool   `json:"run_in_background,omitempty" description:"设为 true 时立即返回 agent_job_id，不阻塞 brain agent。用 monitor(shell_id=agent_job_id) 等待完成，用 job_output(shell_id=agent_job_id) 取结果。适合耗时长的 worker 任务。"`
}

const (
	AgentToolName = "agent"
)

func (c *coordinator) agentTool(ctx context.Context) (fantasy.AgentTool, error) {
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

	go func() {
		runCtx := context.Background() // detached from caller ctx — must not cancel on return
		result, err := c.runSubAgent(runCtx, subAgentParams{
			Agent:          agent,
			SessionID:      sessionID,
			AgentMessageID: agentMessageID,
			ToolCallID:     callID,
			Prompt:         prompt,
			Profile:        profile,
			SessionTitle:   agentSessionTitle(role),
		})

		var outputTail string
		var exitCode int
		if err != nil {
			exitCode = 1
			outputTail = fmt.Sprintf("error: %v", err)
		} else {
			text := result.Content
			if len(text) > 4096 {
				text = text[len(text)-4096:]
			}
			outputTail = text
		}

		shell.PublishBackgroundDone(shell.BackgroundJobEvent{
			Kind:        shell.BackgroundKindDone,
			ID:          jobID,
			SessionID:   sessionID,
			Command:     description,
			Description: description,
			ExitCode:    exitCode,
			OutputTail:  outputTail,
		})
	}()

	return fantasy.NewTextResponse(fmt.Sprintf(
		"agent_job_id: %s\n角色: %s\n状态: 后台运行中\n\n任务已在后台启动。使用 monitor(shell_id=%s, regex=\"agent_job_id: %s\") 等待完成，或用 job_output(shell_id=%s) 查看输出。",
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
