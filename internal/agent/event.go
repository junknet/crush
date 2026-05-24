package agent

import (
	"time"

	"charm.land/fantasy"
	agentprompt "github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/event"
)

func (a *sessionAgent) eventPromptSent(sessionID string) {
	event.PromptSent(
		a.eventCommon(sessionID, a.primaryModel.Get())...,
	)
}

func (a *sessionAgent) eventPromptResponded(sessionID string, duration time.Duration) {
	event.PromptResponded(
		append(
			a.eventCommon(sessionID, a.primaryModel.Get()),
			"prompt duration pretty", duration.String(),
			"prompt duration in seconds", int64(duration.Seconds()),
		)...,
	)
}

func (a *sessionAgent) eventTokensUsed(sessionID string, model Model, usage fantasy.Usage, cost float64) {
	cacheProvider := string(agentprompt.ResolveCacheProvider(string(model.ProviderType)))
	total := usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheCreationTokens
	hitRatio := 0.0
	if usage.InputTokens+usage.CacheReadTokens > 0 {
		hitRatio = float64(usage.CacheReadTokens) / float64(usage.InputTokens+usage.CacheReadTokens)
	}
	event.TokensUsed(
		append(
			a.eventCommon(sessionID, model),
			"input tokens", usage.InputTokens,
			"output tokens", usage.OutputTokens,
			"cache read tokens", usage.CacheReadTokens,
			"cache creation tokens", usage.CacheCreationTokens,
			"cache provider", cacheProvider,
			"cache hit ratio", hitRatio,
			"total tokens", total,
			"cost", cost,
		)...,
	)
}

func (a *sessionAgent) eventCommon(sessionID string, model Model) []any {
	m := model.ModelCfg

	return []any{
		"session id", sessionID,
		"provider", m.Provider,
		"model", m.Model,
		"reasoning effort", m.ReasoningEffort,
		"thinking mode", m.Think,
	}
}
