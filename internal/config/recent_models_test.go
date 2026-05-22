package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// readConfigJSON reads the config file at path and unmarshals it (YAML or
// JSON, normalized via readConfigFile) into a map.
func readConfigJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := readConfigFile(path)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// readRecentModels reads the recent_models section from the state file.
func readRecentModels(t *testing.T, store *ConfigStore) map[string]any {
	t.Helper()
	out := readConfigJSON(t, store.statePath())
	rm, ok := out["recent_models"].(map[string]any)
	require.True(t, ok)
	return rm
}

// testStoreWithPath creates a ConfigStore backed by a Config for recent model
// tests. Recent models are runtime state, so they persist to state.yaml next
// to the declarative crush.json base.
func testStoreWithPath(cfg *Config, dir string) *ConfigStore {
	return &ConfigStore{
		config:     cfg,
		configBase: filepath.Join(dir, "crush.json"),
	}
}

func TestRecordRecentModel_AddsAndPersists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	err := store.recordRecentModel(SelectedModelTypeBrain, SelectedModel{Provider: "openai", Model: "gpt-4o"})
	require.NoError(t, err)

	// in-memory state
	require.Len(t, cfg.RecentModels[SelectedModelTypeBrain], 1)
	require.Equal(t, "openai", cfg.RecentModels[SelectedModelTypeBrain][0].Provider)
	require.Equal(t, "gpt-4o", cfg.RecentModels[SelectedModelTypeBrain][0].Model)

	// persisted state
	rm := readRecentModels(t, store)
	brain, ok := rm[string(SelectedModelTypeBrain)].([]any)
	require.True(t, ok)
	require.Len(t, brain, 1)
	item, ok := brain[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "openai", item["provider"])
	require.Equal(t, "gpt-4o", item["model"])
}

func TestRecordRecentModel_DedupeAndMoveToFront(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	// Add two entries
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, SelectedModel{Provider: "openai", Model: "gpt-4o"}))
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, SelectedModel{Provider: "anthropic", Model: "claude"}))
	// Re-add first; should move to front and not duplicate
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, SelectedModel{Provider: "openai", Model: "gpt-4o"}))

	got := cfg.RecentModels[SelectedModelTypeBrain]
	require.Len(t, got, 2)
	require.Equal(t, SelectedModel{Provider: "openai", Model: "gpt-4o"}, got[0])
	require.Equal(t, SelectedModel{Provider: "anthropic", Model: "claude"}, got[1])
}

func TestRecordRecentModel_TrimsToMax(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	// Insert 6 unique models; max is 5
	entries := []SelectedModel{
		{Provider: "p1", Model: "m1"},
		{Provider: "p2", Model: "m2"},
		{Provider: "p3", Model: "m3"},
		{Provider: "p4", Model: "m4"},
		{Provider: "p5", Model: "m5"},
		{Provider: "p6", Model: "m6"},
	}
	for _, e := range entries {
		require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, e))
	}

	// in-memory state
	got := cfg.RecentModels[SelectedModelTypeBrain]
	require.Len(t, got, 5)
	// Newest first, capped at 5: p6..p2
	require.Equal(t, SelectedModel{Provider: "p6", Model: "m6"}, got[0])
	require.Equal(t, SelectedModel{Provider: "p5", Model: "m5"}, got[1])
	require.Equal(t, SelectedModel{Provider: "p4", Model: "m4"}, got[2])
	require.Equal(t, SelectedModel{Provider: "p3", Model: "m3"}, got[3])
	require.Equal(t, SelectedModel{Provider: "p2", Model: "m2"}, got[4])

	// persisted state: verify trimmed to 5 and newest-first order
	rm := readRecentModels(t, store)
	brain, ok := rm[string(SelectedModelTypeBrain)].([]any)
	require.True(t, ok)
	require.Len(t, brain, 5)
	// Build provider:model IDs and verify order
	var ids []string
	for _, v := range brain {
		m := v.(map[string]any)
		ids = append(ids, m["provider"].(string)+":"+m["model"].(string))
	}
	require.Equal(t, []string{"p6:m6", "p5:m5", "p4:m4", "p3:m3", "p2:m2"}, ids)
}

func TestRecordRecentModel_SkipsEmptyValues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	// Missing provider
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, SelectedModel{Provider: "", Model: "m"}))
	// Missing model
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, SelectedModel{Provider: "p", Model: ""}))

	_, ok := cfg.RecentModels[SelectedModelTypeBrain]
	// Map may be initialized, but should have no entries
	if ok {
		require.Len(t, cfg.RecentModels[SelectedModelTypeBrain], 0)
	}
	// No file should be written (stat via fs.FS)
	baseDir := filepath.Dir(store.statePath())
	fileName := filepath.Base(store.statePath())
	_, err := fs.Stat(os.DirFS(baseDir), fileName)
	require.True(t, os.IsNotExist(err))
}

func TestRecordRecentModel_NoPersistOnNoop(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	entry := SelectedModel{Provider: "openai", Model: "gpt-4o"}
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, entry))

	baseDir := filepath.Dir(store.statePath())
	fileName := filepath.Base(store.statePath())
	before, err := fs.ReadFile(os.DirFS(baseDir), fileName)
	require.NoError(t, err)

	// Get file ModTime to verify no write occurs
	stBefore, err := fs.Stat(os.DirFS(baseDir), fileName)
	require.NoError(t, err)
	beforeMod := stBefore.ModTime()

	// Re-record same entry should be a no-op (no write)
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, entry))

	after, err := fs.ReadFile(os.DirFS(baseDir), fileName)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))

	// Verify ModTime unchanged to ensure truly no write occurred
	stAfter, err := fs.Stat(os.DirFS(baseDir), fileName)
	require.NoError(t, err)
	require.True(t, stAfter.ModTime().Equal(beforeMod), "file ModTime should not change on noop")
}

func TestUpdatePreferredModel_UpdatesRecents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	sel := SelectedModel{Provider: "openai", Model: "gpt-4o"}
	require.NoError(t, store.UpdatePreferredModel(SelectedModelTypeExplore, sel))

	// in-memory
	require.Equal(t, sel, cfg.Models[SelectedModelTypeExplore])
	require.Len(t, cfg.RecentModels[SelectedModelTypeExplore], 1)

	// persisted (read via fs.FS)
	rm := readRecentModels(t, store)
	explore, ok := rm[string(SelectedModelTypeExplore)].([]any)
	require.True(t, ok)
	require.Len(t, explore, 1)
}

func TestRecordRecentModel_TypeIsolation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	// Add models to both brain and explore types.
	brainModel := SelectedModel{Provider: "openai", Model: "gpt-4o"}
	exploreModel := SelectedModel{Provider: "anthropic", Model: "claude"}

	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, brainModel))
	require.NoError(t, store.recordRecentModel(SelectedModelTypeExplore, exploreModel))

	// in-memory: verify types maintain separate histories
	require.Len(t, cfg.RecentModels[SelectedModelTypeBrain], 1)
	require.Len(t, cfg.RecentModels[SelectedModelTypeExplore], 1)
	require.Equal(t, brainModel, cfg.RecentModels[SelectedModelTypeBrain][0])
	require.Equal(t, exploreModel, cfg.RecentModels[SelectedModelTypeExplore][0])

	// Add another brain model and verify explore is unchanged.
	anotherBrain := SelectedModel{Provider: "google", Model: "gemini"}
	require.NoError(t, store.recordRecentModel(SelectedModelTypeBrain, anotherBrain))

	require.Len(t, cfg.RecentModels[SelectedModelTypeBrain], 2)
	require.Len(t, cfg.RecentModels[SelectedModelTypeExplore], 1)
	require.Equal(t, exploreModel, cfg.RecentModels[SelectedModelTypeExplore][0])

	// persisted state: verify both types exist with correct lengths and contents
	rm := readRecentModels(t, store)

	brain, ok := rm[string(SelectedModelTypeBrain)].([]any)
	require.True(t, ok)
	require.Len(t, brain, 2)
	// Verify newest first for brain type
	require.Equal(t, "google", brain[0].(map[string]any)["provider"])
	require.Equal(t, "gemini", brain[0].(map[string]any)["model"])
	require.Equal(t, "openai", brain[1].(map[string]any)["provider"])
	require.Equal(t, "gpt-4o", brain[1].(map[string]any)["model"])

	explore, ok := rm[string(SelectedModelTypeExplore)].([]any)
	require.True(t, ok)
	require.Len(t, explore, 1)
	require.Equal(t, "anthropic", explore[0].(map[string]any)["provider"])
	require.Equal(t, "claude", explore[0].(map[string]any)["model"])
}
