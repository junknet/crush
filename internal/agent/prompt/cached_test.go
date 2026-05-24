package prompt

import (
	"testing"

	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/vercel"
	"github.com/charmbracelet/crush/internal/agent/hyper"
)

func TestResolveCacheProvider(t *testing.T) {
	cases := map[string]CacheProvider{
		anthropic.Name:    CacheProviderAnthropic,
		bedrock.Name:      CacheProviderAnthropic,
		vercel.Name:       CacheProviderAnthropic,
		openai.Name:       CacheProviderOpenAI,
		azure.Name:        CacheProviderOpenAI,
		google.Name:       CacheProviderGoogle,
		"antigravity":     CacheProviderGoogle,
		openaicompat.Name: CacheProviderCompat,
		hyper.Name:        CacheProviderCompat,
		"unknown":         CacheProviderNone,
		"":                CacheProviderNone,
	}
	for typ, want := range cases {
		if got := ResolveCacheProvider(typ); got != want {
			t.Errorf("ResolveCacheProvider(%q) = %s, want %s", typ, got, want)
		}
	}
}

func TestMaybeInjectPromptCacheKey_OpenAI(t *testing.T) {
	opts := map[string]any{}
	MaybeInjectPromptCacheKey(opts, "sess-123", openai.Name)
	if got, ok := opts["prompt_cache_key"]; !ok || got != "sess-123" {
		t.Errorf("openai: prompt_cache_key not injected: %#v", opts)
	}

	opts2 := map[string]any{}
	MaybeInjectPromptCacheKey(opts2, "sess-456", azure.Name)
	if got, ok := opts2["prompt_cache_key"]; !ok || got != "sess-456" {
		t.Errorf("azure: prompt_cache_key not injected: %#v", opts2)
	}
}

func TestMaybeInjectPromptCacheKey_OtherProvidersUntouched(t *testing.T) {
	for _, typ := range []string{anthropic.Name, google.Name, openaicompat.Name, hyper.Name, ""} {
		opts := map[string]any{}
		MaybeInjectPromptCacheKey(opts, "sess-x", typ)
		if _, ok := opts["prompt_cache_key"]; ok {
			t.Errorf("provider %q: prompt_cache_key should NOT be injected: %#v", typ, opts)
		}
	}
}

func TestMaybeInjectPromptCacheKey_RespectsKillSwitch(t *testing.T) {
	t.Setenv("CRUSH_DISABLE_OPENAI_CACHE_KEY", "1")
	opts := map[string]any{}
	MaybeInjectPromptCacheKey(opts, "sess-x", openai.Name)
	if _, ok := opts["prompt_cache_key"]; ok {
		t.Errorf("kill switch ignored: %#v", opts)
	}
}

func TestMaybeInjectPromptCacheKey_NilOrEmptySessionNoop(t *testing.T) {
	MaybeInjectPromptCacheKey(nil, "sess-x", openai.Name)
	opts := map[string]any{}
	MaybeInjectPromptCacheKey(opts, "", openai.Name)
	if _, ok := opts["prompt_cache_key"]; ok {
		t.Errorf("empty session should not inject")
	}
}

func TestMaybeInjectPromptCacheKey_RespectsExistingKey(t *testing.T) {
	opts := map[string]any{"prompt_cache_key": "caller-supplied"}
	MaybeInjectPromptCacheKey(opts, "sess-x", openai.Name)
	if opts["prompt_cache_key"] != "caller-supplied" {
		t.Errorf("existing key clobbered: %v", opts["prompt_cache_key"])
	}
}
