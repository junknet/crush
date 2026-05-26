package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderHealth_SerializationAndLocking(t *testing.T) {
	// Set test environment variable to route config to a temp dir
	tmpDir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(tmpDir, "crush.json"))

	path := GetProviderHealthPath()
	assert.Contains(t, path, "provider-health.yaml")

	// Initially, file doesn't exist, Read should return empty state
	state, err := ReadProviderHealth()
	require.NoError(t, err)
	assert.Empty(t, state.ActiveProvider)
	assert.Empty(t, state.UnhealthyUntil)

	// Write health state
	state.ActiveProvider = "openai"
	state.UnhealthyUntil["anthropic"] = time.Now().Add(5 * time.Minute)
	err = WriteProviderHealth(state)
	require.NoError(t, err)

	// Read again and verify
	readState, err := ReadProviderHealth()
	require.NoError(t, err)
	assert.Equal(t, "openai", readState.ActiveProvider)
	assert.Contains(t, readState.UnhealthyUntil, "anthropic")

	// Test locking helper
	lock, err := lockProviderHealth()
	require.NoError(t, err)
	assert.NotNil(t, lock)
	unlockProviderHealth(lock)
}

func TestProviderHealth_PrioritizeModels(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(tmpDir, "crush.json"))

	primary := Model{
		ModelCfg: config.SelectedModel{Provider: "anthropic", Model: "claude-3"},
	}
	fb1 := Model{
		ModelCfg: config.SelectedModel{Provider: "openai", Model: "gpt-4"},
	}
	fb2 := Model{
		ModelCfg: config.SelectedModel{Provider: "gemini", Model: "gemini-1.5"},
	}
	fallbacks := []Model{fb1, fb2}

	// 1. Initially, everything is healthy -> primary first, then fallbacks
	p1 := PrioritizeModels(primary, fallbacks)
	require.Len(t, p1, 3)
	assert.Equal(t, "anthropic", p1[0].ModelCfg.Provider)
	assert.Equal(t, "openai", p1[1].ModelCfg.Provider)
	assert.Equal(t, "gemini", p1[2].ModelCfg.Provider)

	// 2. Mark primary (anthropic) unhealthy -> fallbacks move to front, primary to end
	err := MarkProviderUnhealthy("anthropic", 5*time.Minute)
	require.NoError(t, err)

	p2 := PrioritizeModels(primary, fallbacks)
	require.Len(t, p2, 3)
	assert.Equal(t, "openai", p2[0].ModelCfg.Provider)
	assert.Equal(t, "gemini", p2[1].ModelCfg.Provider)
	assert.Equal(t, "anthropic", p2[2].ModelCfg.Provider)

	// 3. Mark primary unhealthy, but also set gemini active -> active fallback is prioritized
	err = MarkProviderActive("gemini")
	require.NoError(t, err)
	// re-mark anthropic unhealthy since MarkProviderActive above cleared everything else, but wait,
	// MarkProviderActive("gemini") sets active_provider to gemini and deletes unhealthy_until for gemini.
	// But it does NOT touch anthropic's unhealthy status unless we clear it. Let's verify:
	// MarkProviderActive only deletes "gemini" from UnhealthyUntil, so "anthropic" remains unhealthy!
	// Let's re-read to be sure.
	state, err := ReadProviderHealth()
	require.NoError(t, err)
	state.ActiveProvider = "gemini"
	state.UnhealthyUntil["anthropic"] = time.Now().Add(5 * time.Minute)
	err = WriteProviderHealth(state)
	require.NoError(t, err)

	p3 := PrioritizeModels(primary, fallbacks)
	require.Len(t, p3, 3)
	assert.Equal(t, "gemini", p3[0].ModelCfg.Provider)
	assert.Equal(t, "openai", p3[1].ModelCfg.Provider)
	assert.Equal(t, "anthropic", p3[2].ModelCfg.Provider)
}

func TestProviderHealth_WatchAndCancel(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(tmpDir, "crush.json"))

	primary := Model{
		ModelCfg: config.SelectedModel{Provider: "anthropic", Model: "claude-3"},
	}
	fb1 := Model{
		ModelCfg: config.SelectedModel{Provider: "openai", Model: "gpt-4"},
	}
	candidates := []Model{primary, fb1}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	failoverTriggered := make(chan struct{}, 1)
	go WatchProviderHealth(ctx, "anthropic", candidates, func() {
		failoverTriggered <- struct{}{}
	})

	// Wait briefly for watch to start
	time.Sleep(100 * time.Millisecond)

	// Write active provider as openai, and mark anthropic unhealthy
	state, err := ReadProviderHealth()
	require.NoError(t, err)
	state.ActiveProvider = "openai"
	state.UnhealthyUntil["anthropic"] = time.Now().Add(5 * time.Minute)
	err = WriteProviderHealth(state)
	require.NoError(t, err)

	select {
	case <-failoverTriggered:
		// Success! Failover was triggered by file update
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for failover trigger")
	}
}

func TestProviderHealth_ProbingLockAndCoordinatedWaiting(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(tmpDir, "crush.json"))

	// 1. Acquire probing lock
	acquired, err := AcquireProbingLock("anthropic")
	require.NoError(t, err)
	assert.True(t, acquired)

	// 2. Second attempt should fail to acquire (already probing)
	acquired2, err := AcquireProbingLock("anthropic")
	require.NoError(t, err)
	assert.False(t, acquired2)

	// 3. Test WaitForProbing in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	waitDone := make(chan string, 1)
	go func() {
		active, _ := WaitForProbing(ctx, "anthropic")
		waitDone <- active
	}()

	// Wait briefly
	time.Sleep(100 * time.Millisecond)

	// Release probing lock and set active provider
	err = MarkProviderActive("openai")
	require.NoError(t, err)
	err = ReleaseProbingLock("anthropic")
	require.NoError(t, err)

	select {
	case active := <-waitDone:
		assert.Equal(t, "openai", active)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for probe to finish")
	}
}
