package agent

import (
	"context"
	"errors"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	toolsPkg "github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSessionAgent is a minimal mock for the SessionAgent interface.
type mockSessionAgent struct {
	model     Model
	runFunc   func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error)
	cancelled []string
}

func (m *mockSessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	return m.runFunc(ctx, call)
}

func (m *mockSessionAgent) Model() Model                                           { return m.model }
func (m *mockSessionAgent) SetModels(primary, title Model, fallbackModels []Model) {}
func (m *mockSessionAgent) SetTools(tools []fantasy.AgentTool)                     {}
func (m *mockSessionAgent) SetDeferredRegistry(*toolsPkg.DeferredRegistry)         {}
func (m *mockSessionAgent) SetSystemPrompt(systemPrompt string)                    {}
func (m *mockSessionAgent) Cancel(sessionID string) {
	m.cancelled = append(m.cancelled, sessionID)
}
func (m *mockSessionAgent) CancelAll() {}
func (m *mockSessionAgent) CancelAndFlush(sessionID string) ([]string, bool) {
	m.cancelled = append(m.cancelled, sessionID)
	return nil, false
}
func (m *mockSessionAgent) DrainQueue(sessionID string) []string        { return nil }
func (m *mockSessionAgent) IsSessionBusy(sessionID string) bool         { return false }
func (m *mockSessionAgent) IsBusy() bool                                { return false }
func (m *mockSessionAgent) QueuedPrompts(sessionID string) int          { return 0 }
func (m *mockSessionAgent) QueuedPromptsList(sessionID string) []string { return nil }
func (m *mockSessionAgent) ClearQueue(sessionID string)                 {}
func (m *mockSessionAgent) Summarize(context.Context, string, fantasy.ProviderOptions) error {
	return nil
}

// newTestCoordinator creates a minimal coordinator for unit testing runSubAgent.
func newTestCoordinator(t *testing.T, env fakeEnv, providerID string, providerCfg config.ProviderConfig) *coordinator {
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.Config().Providers.Set(providerID, providerCfg)
	return &coordinator{
		cfg:      cfg,
		sessions: env.sessions,
	}
}

// newMockAgent creates a mockSessionAgent with the given provider and run function.
func newMockAgent(providerID string, maxTokens int64, runFunc func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)) *mockSessionAgent {
	return &mockSessionAgent{
		model: Model{
			CatwalkCfg: catwalk.Model{
				DefaultMaxTokens: maxTokens,
			},
			ModelCfg: config.SelectedModel{
				Provider: providerID,
			},
		},
		runFunc: runFunc,
	}
}

// agentResultWithText creates a minimal AgentResult with the given text response.
func agentResultWithText(text string) *fantasy.AgentResult {
	return &fantasy.AgentResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.TextContent{Text: text},
			},
		},
	}
}

func TestRunSubAgent(t *testing.T) {
	const providerID = "test-provider"
	providerCfg := config.ProviderConfig{ID: providerID}

	t.Run("happy path", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		calls := make([]SessionAgentCall, 0, 3)
		agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			calls = append(calls, call)
			return agentResultWithText("done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			Profile:        scheduler.ProfileWorkerAgent,
			SessionTitle:   "Test Session",
		})
		require.NoError(t, err)
		assert.Equal(t, "done", resp.Content)
		assert.False(t, resp.IsError)
		require.Len(t, calls, 1)
		assert.Equal(t, "do something", calls[0].Prompt)
		assert.Equal(t, int64(4096), calls[0].MaxOutputTokens)
		assert.Equal(t, string(scheduler.ProfileWorkerAgent), calls[0].TaskProfile)
	})

	t.Run("explore profile propagates to agent call", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		calls := make([]SessionAgentCall, 0, 1)
		agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			calls = append(calls, call)
			return agentResultWithText("done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "inspect",
			Profile:        scheduler.ProfileExploreAgent,
			SessionTitle:   "Explore Session",
		})
		require.NoError(t, err)
		assert.Equal(t, "done", resp.Content)
		require.Len(t, calls, 1)
		assert.Equal(t, string(scheduler.ProfileExploreAgent), calls[0].TaskProfile)
	})

	t.Run("ModelCfg.MaxTokens overrides default", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		calls := make([]SessionAgentCall, 0, 3)
		agent := &mockSessionAgent{
			model: Model{
				CatwalkCfg: catwalk.Model{
					DefaultMaxTokens: 4096,
				},
				ModelCfg: config.SelectedModel{
					Provider:  providerID,
					MaxTokens: 8192,
				},
			},
			runFunc: func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				calls = append(calls, call)
				return agentResultWithText("ok"), nil
			},
		}

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Content)
		require.Len(t, calls, 1)
		assert.Equal(t, int64(8192), calls[0].MaxOutputTokens)
	})

	t.Run("session creation failure with canceled context", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, nil)

		// Use a canceled context to trigger CreateTaskSession failure.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = coord.runSubAgent(ctx, subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
	})

	t.Run("provider not configured", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		// Agent references a provider that doesn't exist in config.
		agent := newMockAgent("unknown-provider", 4096, nil)

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model provider not configured")
	})

	t.Run("agent run error returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, errors.New("provider request failed")
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		// runSubAgent returns (errorResponse, nil) when agent.Run fails — not a Go error.
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Failed to generate response: provider request failed", resp.Content)
	})

	t.Run("session setup callback is invoked", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		var setupCalledWith string
		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
			SessionSetup: func(sessionID string) {
				setupCalledWith = sessionID
			},
		})
		require.NoError(t, err)
		assert.NotEmpty(t, setupCalledWith, "SessionSetup should have been called")
	})

	t.Run("cost propagation to parent session", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		callCount := 0
		agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			callCount++
			// Simulate the agent incurring cost by updating the child session.
			if callCount == 1 {
				childSession, err := env.sessions.Get(ctx, call.SessionID)
				if err != nil {
					return nil, err
				}
				childSession.Cost = 0.05
				_, err = env.sessions.Save(ctx, childSession)
				if err != nil {
					return nil, err
				}
			}
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parentSession.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.05, updated.Cost, 1e-9)
	})
}

func TestEnsureChildTaskUsesProfile(t *testing.T) {
	env := testEnv(t)
	coord := newTestCoordinator(t, env, "test-provider", config.ProviderConfig{ID: "test-provider"})
	taskRuntime := runtime.NewSession(env.workingDir, nil)
	taskScheduler := scheduler.NewAgentScheduler(taskRuntime)
	parent := taskScheduler.EnsureRoot("parent-session", "root goal", nil, scheduler.ProfileBrainAgent)
	require.NotNil(t, parent)

	exploreNode := coord.ensureChildTask(taskScheduler, parent.SessionID, "child-explore", "inspect repo", scheduler.ProfileExploreAgent, 2048)
	require.NotNil(t, exploreNode)
	assert.Equal(t, scheduler.ProfileExploreAgent, exploreNode.Profile)
	assert.Equal(t, scheduler.TaskExplore, exploreNode.Kind)
	assert.Equal(t, scheduler.TaskReadOnly, exploreNode.Mode)
	assert.Equal(t, 2048, exploreNode.Intent.BudgetTokens)

	workerNode := coord.ensureChildTask(taskScheduler, parent.SessionID, "child-worker", "edit file", scheduler.ProfileWorkerAgent, 4096)
	require.NotNil(t, workerNode)
	assert.Equal(t, scheduler.ProfileWorkerAgent, workerNode.Profile)
	assert.Equal(t, scheduler.TaskEdit, workerNode.Kind)
	assert.Equal(t, scheduler.TaskWrite, workerNode.Mode)
	assert.Equal(t, 4096, workerNode.Intent.BudgetTokens)
}

func TestEnsureRootTaskUsesPlanProfile(t *testing.T) {
	env := testEnv(t)
	coord := newTestCoordinator(t, env, "test-provider", config.ProviderConfig{ID: "test-provider"})
	taskRuntime := runtime.NewSession(env.workingDir, nil)
	taskScheduler := scheduler.NewAgentScheduler(taskRuntime)

	planNode := coord.ensureRootTask(taskScheduler, "session-plan", "draft plan", 2048, scheduler.ProfilePlanAgent)
	require.NotNil(t, planNode)
	assert.Equal(t, scheduler.ProfilePlanAgent, planNode.Profile)
	assert.Equal(t, scheduler.TaskPlan, planNode.Kind)
	assert.Equal(t, scheduler.TaskReadOnly, planNode.Mode)
	assert.Equal(t, 2048, planNode.Intent.BudgetTokens)

	brainNode := coord.ensureRootTask(taskScheduler, "session-brain", "implement", 4096, scheduler.ProfileBrainAgent)
	require.NotNil(t, brainNode)
	assert.Equal(t, scheduler.ProfileBrainAgent, brainNode.Profile)
	assert.Equal(t, scheduler.TaskEdit, brainNode.Kind)
	assert.Equal(t, scheduler.TaskWrite, brainNode.Mode)
	assert.Equal(t, 4096, brainNode.Intent.BudgetTokens)
}

func TestUpdateParentSessionCost(t *testing.T) {
	t.Run("accumulates cost correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		// Set child cost.
		child.Cost = 0.10
		_, err = env.sessions.Save(t.Context(), child)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.10, updated.Cost, 1e-9)
	})

	t.Run("accumulates multiple child costs", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		child1, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child1")
		require.NoError(t, err)
		child1.Cost = 0.05
		_, err = env.sessions.Save(t.Context(), child1)
		require.NoError(t, err)

		child2, err := env.sessions.CreateTaskSession(t.Context(), "tool-2", parent.ID, "Child2")
		require.NoError(t, err)
		child2.Cost = 0.03
		_, err = env.sessions.Save(t.Context(), child2)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child1.ID, parent.ID)
		require.NoError(t, err)
		err = coord.updateParentSessionCost(t.Context(), child2.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.08, updated.Cost, 1e-9)
	})

	t.Run("child session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), "non-existent", parent.ID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get child session")
	})

	t.Run("parent session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, "non-existent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get parent session")
	})

	t.Run("zero cost handled correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", session.ModeExecute)
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.0, updated.Cost, 1e-9)
	})
}

func TestGetProviderOptionsReasoningEffort(t *testing.T) {
	// Bedrock is Fantasy's Anthropic under a different provider name; options
	// must land under anthropic.Name so the Anthropic language model picks them up.
	tests := []struct {
		name         string
		providerType catwalk.Type
	}{
		{"anthropic honors reasoning_effort", catwalk.Type(anthropic.Name)},
		{"bedrock honors reasoning_effort", catwalk.Type(bedrock.Name)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{
				CatwalkCfg: catwalk.Model{ID: "claude-opus-4-7", CanReason: true},
				ModelCfg: config.SelectedModel{
					Provider:        "test",
					Think:           true,
					ReasoningEffort: "high",
				},
			}
			providerCfg := config.ProviderConfig{ID: "test", Type: tc.providerType}

			opts := getProviderOptions("test-session", model, providerCfg, false)

			raw, ok := opts[anthropic.Name]
			require.True(t, ok, "options should be keyed under anthropic.Name for type %q", tc.providerType)
			parsed, ok := raw.(*anthropic.ProviderOptions)
			require.True(t, ok)
			require.NotNil(t, parsed.Thinking)
			assert.Equal(t, int64(16000), parsed.Thinking.BudgetTokens)
		})
	}
}

func TestNewCoordinatorWithAuditor(t *testing.T) {
	env := testEnv(t)
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)

	// Configure auditor agent in config
	cfg.Config().Agents[config.AgentAuditor] = config.Agent{
		ID:           config.AgentAuditor,
		Model:        config.SelectedModelTypeAuditor,
		AllowedTools: []string{"view", "rg"},
	}

	// We also need provider config for it to succeed resolving models
	providerID := "test-provider"
	providerCfg := config.ProviderConfig{
		ID:   providerID,
		Type: "anthropic",
		Models: []catwalk.Model{
			{ID: "claude-opus-4-7", CanReason: true},
		},
	}
	cfg.Config().Providers.Set(providerID, providerCfg)
	cfg.Config().Models[config.SelectedModelTypeAuditor] = config.SelectedModel{
		Provider: providerID,
		Model:    "claude-opus-4-7",
	}
	cfg.Config().Models[config.SelectedModelTypeBrain] = config.SelectedModel{
		Provider: providerID,
		Model:    "claude-opus-4-7",
	}

	// We can use NewCoordinator directly
	coord, err := NewCoordinator(
		t.Context(),
		cfg,
		env.sessions,
		nil, // messages
		nil, // permissions
		nil, // history
		nil, // filetracker
		nil, // lspManager
		nil, // notify pubsub
		nil, // bgManager
	)
	require.NoError(t, err)

	c, ok := coord.(*coordinator)
	require.True(t, ok)

	auditor, ok := c.agents[config.AgentAuditor]
	require.True(t, ok, "auditor agent should be initialized")
	require.NotNil(t, auditor)
}
