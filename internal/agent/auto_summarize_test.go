package agent

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestShouldAutoSummarizeAtSeventyPercent(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	model := Model{CatwalkCfg: catwalk.Model{ContextWindow: 1_000}}

	require.False(t, agent.shouldAutoSummarize(model, session.Session{
		PromptTokens:     699,
		CompletionTokens: 0,
	}))
	require.True(t, agent.shouldAutoSummarize(model, session.Session{
		PromptTokens:     699,
		CompletionTokens: 1,
	}))
}

func TestShouldAutoSummarizeHonorsDisableFlag(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{disableAutoSummarize: true}
	model := Model{CatwalkCfg: catwalk.Model{ContextWindow: 1_000}}

	require.False(t, agent.shouldAutoSummarize(model, session.Session{
		PromptTokens:     700,
		CompletionTokens: 0,
	}))
}
