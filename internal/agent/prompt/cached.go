package prompt

import (
	"os"
	"strconv"

	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/antigravity"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/vercel"
	"github.com/charmbracelet/crush/internal/agent/hyper"
)

// CacheProvider classifies a provider into the cache regime it belongs to.
// It is the user-visible label written to the trace so cache hit-ratio dashboards
// can group requests by regime rather than by SDK-internal provider name.
type CacheProvider string

const (
	CacheProviderAnthropic CacheProvider = "anthropic"     // ephemeral cache_control
	CacheProviderOpenAI    CacheProvider = "openai"        // PromptCacheKey on Chat / Responses
	CacheProviderGoogle    CacheProvider = "google"        // implicit prefix cache (>=1024 tokens)
	CacheProviderCompat    CacheProvider = "openai_compat" // OpenAI-compatible — no controlled cache
	CacheProviderNone      CacheProvider = "none"
)

// ResolveCacheProvider maps a fantasy provider type name to the cache regime.
// Returns CacheProviderNone when the provider has no known cache mechanism.
func ResolveCacheProvider(providerType string) CacheProvider {
	switch providerType {
	case anthropic.Name, bedrock.Name, vercel.Name:
		return CacheProviderAnthropic
	case openai.Name, azure.Name:
		return CacheProviderOpenAI
	case google.Name, antigravity.Name:
		return CacheProviderGoogle
	case openaicompat.Name, hyper.Name:
		return CacheProviderCompat
	default:
		return CacheProviderNone
	}
}

// openaiCacheDisabled reports whether the user has flipped the kill switch
// for OpenAI PromptCacheKey routing.
func openaiCacheDisabled() bool {
	t, _ := strconv.ParseBool(os.Getenv("CRUSH_DISABLE_OPENAI_CACHE_KEY"))
	return t
}

// MaybeInjectPromptCacheKey writes the PromptCacheKey field (as the raw
// "prompt_cache_key" map key) into a getProviderOptions-style merged map when
// the provider routes through OpenAI's Chat Completions or Responses API.
//
// Setting the key keeps the request pinned to the same backend partition for
// the lifetime of a session, which is what makes OpenAI's automatic prefix
// cache actually hit across turns.
//
// Other providers ignore the key. Anthropic-family caching is still driven by
// the message-level cache_control marker in agent.getCacheControlOptions —
// this function deliberately does nothing for them.
func MaybeInjectPromptCacheKey(opts map[string]any, sessionID, providerType string) {
	if sessionID == "" || opts == nil || openaiCacheDisabled() {
		return
	}
	// Don't clobber an explicit caller-supplied key.
	if _, ok := opts["prompt_cache_key"]; ok {
		return
	}
	switch ResolveCacheProvider(providerType) {
	case CacheProviderOpenAI:
		opts["prompt_cache_key"] = sessionID
	}
}
