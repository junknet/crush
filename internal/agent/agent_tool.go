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
)

//go:embed templates/agent_tool.md
var agentToolDescription string

type AgentParams struct {
	Prompt string `json:"prompt" description:"The task for the agent to perform"`
	Role   string `json:"role,omitempty" description:"Agent role to run: explore, plan, or worker. Defaults to explore. Use explore for read-only repository inspection, plan for read-only implementation design, and worker for implementation, refactors, fixes, docs, or verification."`
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

func resolveAgentToolRole(role string) (scheduler.WorkerProfile, string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", config.AgentExplore:
		return scheduler.ProfileExploreAgent, config.AgentExplore, nil
	case config.AgentPlan:
		return scheduler.ProfilePlanAgent, config.AgentPlan, nil
	case config.AgentWorker:
		return scheduler.ProfileWorkerAgent, config.AgentWorker, nil
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
	default:
		return "Explore Agent Session"
	}
}
