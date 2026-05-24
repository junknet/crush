package backend

import (
	"context"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
)

// GetAgentInfo returns the agent's model and busy status.
func (b *Backend) GetAgentInfo(workspaceID string) (proto.AgentInfo, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.AgentInfo{}, err
	}

	var agentInfo proto.AgentInfo
	if ws.AgentCoordinator != nil {
		m := ws.AgentCoordinator.Model()
		agentInfo = proto.AgentInfo{
			Model:    m.CatwalkCfg,
			ModelCfg: m.ModelCfg,
			IsBusy:   ws.AgentCoordinator.IsBusy(),
			IsReady:  true,
		}
	}
	return agentInfo, nil
}

// SummarizeSession triggers a session summarization.
func (b *Backend) SummarizeSession(ctx context.Context, workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if ws.AgentCoordinator == nil {
		return ErrAgentNotInitialized
	}

	return ws.AgentCoordinator.Summarize(ctx, sessionID)
}

// QueuedPrompts returns the number of queued prompts for the session.
func (b *Backend) QueuedPrompts(workspaceID, sessionID string) (int, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}

	if ws.AgentCoordinator == nil {
		return 0, nil
	}

	return ws.AgentCoordinator.QueuedPrompts(sessionID), nil
}

// ClearQueue clears the prompt queue for the session.
func (b *Backend) ClearQueue(workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if ws.AgentCoordinator != nil {
		ws.AgentCoordinator.ClearQueue(sessionID)
	}
	return nil
}

// QueuedPromptsList returns the list of queued prompt strings for a
// session.
func (b *Backend) QueuedPromptsList(workspaceID, sessionID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	if ws.AgentCoordinator == nil {
		return nil, nil
	}

	return ws.AgentCoordinator.QueuedPromptsList(sessionID), nil
}

// GetDefaultExploreModel returns the default explore model for a provider.
func (b *Backend) GetDefaultExploreModel(workspaceID, providerID string) (config.SelectedModel, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return config.SelectedModel{}, err
	}

	return ws.GetDefaultExploreModel(providerID), nil
}
