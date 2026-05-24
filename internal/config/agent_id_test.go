package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_AgentIDs(t *testing.T) {
	cfg := &Config{
		Options: &Options{
			DisabledTools: []string{},
		},
	}
	cfg.SetupAgents()

	t.Run("Brain agent should have correct ID", func(t *testing.T) {
		brainAgent, ok := cfg.Agents[AgentBrain]
		require.True(t, ok)
		assert.Equal(t, AgentBrain, brainAgent.ID, "Brain agent ID should be '%s'", AgentBrain)
	})

	t.Run("Plan agent should have correct ID", func(t *testing.T) {
		planAgent, ok := cfg.Agents[AgentPlan]
		require.True(t, ok)
		assert.Equal(t, AgentPlan, planAgent.ID, "Plan agent ID should be '%s'", AgentPlan)
	})

	t.Run("Worker agent should have correct ID", func(t *testing.T) {
		workerAgent, ok := cfg.Agents[AgentWorker]
		require.True(t, ok)
		assert.Equal(t, AgentWorker, workerAgent.ID, "Worker agent ID should be '%s'", AgentWorker)
	})

	t.Run("Explore agent should have correct ID", func(t *testing.T) {
		exploreAgent, ok := cfg.Agents[AgentExplore]
		require.True(t, ok)
		assert.Equal(t, AgentExplore, exploreAgent.ID, "Explore agent ID should be '%s'", AgentExplore)
	})
}
