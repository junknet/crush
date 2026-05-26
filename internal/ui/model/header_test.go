package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBrainHeaderModelLabelUsesConcreteSelectedModel(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeBrain: {
				Provider: "waitai-openai",
				Model:    "gpt-5.5",
			},
		},
		Agents: map[string]config.Agent{
			config.AgentBrain: {Model: config.SelectedModelTypeBrain},
		},
	}

	require.Equal(t, "BRAIN waitai-openai/gpt-5.5", brainHeaderModelLabel(cfg))
}

func TestBrainHeaderModelLabelFallsBackToModelSlot(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Agents: map[string]config.Agent{
			config.AgentBrain: {Model: config.SelectedModelTypeBrain},
		},
	}

	require.Equal(t, "BRAIN", brainHeaderModelLabel(cfg))
}
