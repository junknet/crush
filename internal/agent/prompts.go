package agent

import (
	"context"
	_ "embed"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/brain.md.tpl
var brainPromptTmpl []byte

//go:embed templates/worker.md.tpl
var workerPromptTmpl []byte

//go:embed templates/plan.md.tpl
var planPromptTmpl []byte

//go:embed templates/explore.md.tpl
var explorePromptTmpl []byte

//go:embed templates/auditor.md.tpl
var auditorPromptTmpl []byte

//go:embed templates/initialize.md.tpl
var initializePromptTmpl []byte

func brainPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("brain", string(brainPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func workerPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("worker", string(workerPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func planPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("plan", string(planPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func explorePrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("explore", string(explorePromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func auditorPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("auditor", string(auditorPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func promptForAgentRole(role string, opts ...prompt.Option) (*prompt.Prompt, error) {
	switch role {
	case config.AgentBrain:
		return brainPrompt(opts...)
	case config.AgentPlan:
		return planPrompt(opts...)
	case config.AgentWorker:
		return workerPrompt(opts...)
	case config.AgentExplore:
		return explorePrompt(opts...)
	case config.AgentAuditor:
		return auditorPrompt(opts...)
	default:
		return brainPrompt(opts...)
	}
}

func InitializePrompt(cfg *config.ConfigStore) (string, error) {
	systemPrompt, err := prompt.NewPrompt("initialize", string(initializePromptTmpl))
	if err != nil {
		return "", err
	}
	return systemPrompt.Build(context.Background(), "", "", cfg)
}
