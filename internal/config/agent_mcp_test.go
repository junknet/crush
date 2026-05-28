package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgent_MCPDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	cfg.SetupAgents()

	// Brain should have all MCPs (nil)
	require.Nil(t, cfg.Agents[AgentBrain].AllowedMCP)

	// Explore should have all MCPs (nil now, was empty map)
	require.Nil(t, cfg.Agents[AgentExplore].AllowedMCP)

	// Plan should have all MCPs (nil now, was empty map)
	require.Nil(t, cfg.Agents[AgentPlan].AllowedMCP)

	// Auditor should have all MCPs (nil now, was empty map)
	require.Nil(t, cfg.Agents[AgentAuditor].AllowedMCP)
}

func TestAgent_MCPDisable(t *testing.T) {
	cfg := &Config{
		Agents: map[string]Agent{
			AgentExplore: {
				AllowedMCP: map[string][]string{},
			},
		},
	}
	cfg.setDefaults("/tmp", "")
	cfg.SetupAgents()

	// Explore should have the empty map (disabled)
	require.NotNil(t, cfg.Agents[AgentExplore].AllowedMCP)
	require.Len(t, cfg.Agents[AgentExplore].AllowedMCP, 0)
}
