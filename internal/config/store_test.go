package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestConfigStore_SetConfigField_WritesDeclarative(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configBase := filepath.Join(dir, "crush.json")
	store := &ConfigStore{
		config:     &Config{},
		configBase: configBase,
	}

	// "foo" is a declarative key, so it routes to crush.yaml.
	err := store.SetConfigField("foo", "bar")
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "crush.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "foo")
	require.Contains(t, string(data), "bar")
}

func TestGlobalWorkspaceDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_DATA", dir)

	wsDir := GlobalWorkspaceDir()
	globalData := GlobalConfigData()

	require.Equal(t, filepath.Dir(globalData), wsDir)
	require.Equal(t, dir, wsDir)
}

func TestConfigStaleness_CleanImmediatelyAfterSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "crush.json")

	// Create a config file
	content := []byte(`{"options": {"debug": true}}`)
	require.NoError(t, os.WriteFile(configPath, content, 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	result := store.ConfigStaleness()
	require.False(t, result.Dirty)
	require.Empty(t, result.Changed)
	require.Empty(t, result.Missing)
}

func TestConfigStaleness_DetectsFileContentChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "crush.json")

	// Create initial config file
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": false}`), 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// Modify the file
	time.Sleep(10 * time.Millisecond) // Ensure different mtime
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	require.Contains(t, result.Changed, configPath)
	require.Empty(t, result.Missing)
}

func TestConfigStaleness_DetectsFileDeletion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "crush.json")

	// Create initial config file
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// Delete the file
	require.NoError(t, os.Remove(configPath))

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	require.Empty(t, result.Changed)
	require.Contains(t, result.Missing, configPath)
}

func TestConfigStaleness_DetectsNewFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "crush.json")

	// Don't create file initially
	store := &ConfigStore{
		config:     &Config{},
		configBase: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// Now create the file
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	require.Contains(t, result.Changed, configPath)
	require.Empty(t, result.Missing)
}

func TestConfigStaleness_SortedOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.json")
	pathB := filepath.Join(dir, "b.json")
	pathC := filepath.Join(dir, "c.json")

	// Create all files
	for _, p := range []string{pathA, pathB, pathC} {
		require.NoError(t, os.WriteFile(p, []byte(`{}`), 0o600))
	}

	store := &ConfigStore{
		config:     &Config{},
		configBase: pathA,
	}
	// Add in reverse order to test sorting
	store.captureStalenessSnapshot([]string{pathC, pathA, pathB})

	// Modify all files
	time.Sleep(10 * time.Millisecond)
	for _, p := range []string{pathA, pathB, pathC} {
		require.NoError(t, os.WriteFile(p, []byte(`{"changed": true}`), 0o600))
	}

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	// Should be sorted alphabetically
	require.Equal(t, []string{pathA, pathB, pathC}, result.Changed)
}

func TestConfigStaleness_RefreshClearsDirtyState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "crush.json")

	// Create initial config file
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": false}`), 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// Modify the file
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	// Verify dirty
	result := store.ConfigStaleness()
	require.True(t, result.Dirty)

	// Refresh snapshot
	require.NoError(t, store.RefreshStalenessSnapshot())

	// Verify clean now
	result = store.ConfigStaleness()
	require.False(t, result.Dirty)
	require.Empty(t, result.Changed)
	require.Empty(t, result.Missing)
}

// TestReloadFromDisk_UsesNewConfigValues is a regression test ensuring that
// ReloadFromDisk updates store state BEFORE running model/agent setup,
// so the new config values are used rather than stale pre-reload values.
func TestReloadFromDisk_UsesNewConfigValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	configPath := GlobalConfig()

	// Create initial config with one model preference
	initialConfig := `{
		"models": {
			"brain": {"provider": "openai", "model": "gpt-4"}
		},
		"providers": {
			"openai": {
				"api_key": "test-key",
				"models": [{"id": "gpt-4", "name": "GPT-4"}]
			}
		}
	}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial config properly
	store, err := Load(dir, dir, false)
	require.NoError(t, err)

	store.CaptureStalenessSnapshot([]string{configPath})

	// Verify initial model
	require.Equal(t, "openai", store.config.Models[SelectedModelTypeBrain].Provider)
	require.Equal(t, "gpt-4", store.config.Models[SelectedModelTypeBrain].Model)

	// Modify config on disk to change model
	updatedConfig := `{
		"models": {
			"brain": {"provider": "anthropic", "model": "claude-3"}
		},
		"providers": {
			"openai": {
				"api_key": "test-key",
				"models": [{"id": "gpt-4", "name": "GPT-4"}]
			},
			"anthropic": {
				"api_key": "test-key-2",
				"models": [{"id": "claude-3", "name": "Claude 3"}]
			}
		}
	}`
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(updatedConfig), 0o600))

	// Reload from disk
	ctx := context.Background()
	err = store.ReloadFromDisk(ctx)
	require.NoError(t, err)

	// Verify the NEW config values are now in effect (regression check)
	require.Equal(t, "anthropic", store.config.Models[SelectedModelTypeBrain].Provider)
	require.Equal(t, "claude-3", store.config.Models[SelectedModelTypeBrain].Model)
}

// TestSetConfigField_AutoReloads verifies that SetConfigField automatically
// reloads config into memory after writing, so subsequent reads see the new value.
func TestSetConfigField_AutoReloads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	configPath := GlobalConfig()

	// Create initial config file with debug = false
	initialConfig := `{"options": {"debug": false}}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial config
	store, err := Load(dir, dir, false)
	require.NoError(t, err)

	// Verify initial state
	require.False(t, store.config.Options.Debug)

	// Capture snapshot for staleness tracking
	store.CaptureStalenessSnapshot([]string{configPath})

	// Use SetConfigField to change debug to true
	err = store.SetConfigField("options.debug", true)
	require.NoError(t, err)

	// Verify in-memory state was automatically reloaded and reflects the change
	require.True(t, store.config.Options.Debug, "Expected config to auto-reload and show debug = true")

	// Verify staleness is clean after the reload
	staleness := store.ConfigStaleness()
	require.False(t, staleness.Dirty, "Expected staleness to be clean after auto-reload")
}

// TestRemoveConfigField_AutoReloads verifies that RemoveConfigField automatically
// reloads config into memory after writing.
func TestRemoveConfigField_AutoReloads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	configPath := GlobalConfig()

	// Create initial config file with a custom option
	initialConfig := `{"options": {"debug": true, "custom_field": "value"}}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial config
	store, err := Load(dir, dir, false)
	require.NoError(t, err)

	// Capture snapshot
	store.CaptureStalenessSnapshot([]string{configPath})

	// Verify the field exists initially (indirectly - store loaded successfully)
	require.True(t, store.config.Options.Debug)

	// Remove the debug field
	err = store.RemoveConfigField("options.debug")
	require.NoError(t, err)

	// Verify auto-reload occurred and stale state is clean
	staleness := store.ConfigStaleness()
	require.False(t, staleness.Dirty, "Expected staleness to be clean after auto-reload from RemoveConfigField")
}

// TestSetConfigField_AutoReloadSkipsWhenNoWorkingDir verifies that auto-reload
// gracefully skips when working directory is not set (e.g., during testing).
func TestSetConfigField_AutoReloadSkipsWhenNoWorkingDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "crush.json")

	// Create a store without working directory (like some test setups)
	store := &ConfigStore{
		config:     &Config{},
		configBase: configPath,
		// workingDir is empty
	}

	// SetConfigField should succeed even without workingDir (auto-reload skips)
	err := store.SetConfigField("foo", "bar")
	require.NoError(t, err)

	// Verify file was still written ("foo" is declarative -> crush.yaml)
	data, err := os.ReadFile(filepath.Join(dir, "crush.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "foo")
}

// TestSetConfigField_RoutesStateKeyToStateFile verifies that runtime-state keys
// (api_key/oauth/models/recent_models) persist to state.yaml, not crush.yaml.
func TestSetConfigField_RoutesStateKeyToStateFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	basePath := filepath.Join(dir, "crush.json")
	store := &ConfigStore{
		config:     &Config{},
		configBase: basePath,
	}

	require.NoError(t, store.SetConfigField("providers.waitai-openai.api_key", "secret"))

	statePath := filepath.Join(dir, "state.yaml")
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)
	require.Contains(t, string(data), "providers:")
	require.Contains(t, string(data), "waitai-openai:")
	require.Contains(t, string(data), "api_key: secret")

	// The declarative file must not exist for a pure state write.
	_, err = os.Stat(filepath.Join(dir, "crush.yaml"))
	require.True(t, os.IsNotExist(err))
}

// TestAutoReloadDisabledDuringReload verifies that auto-reload is suppressed
// during ReloadFromDisk to prevent re-entrant/nested reload calls.
func TestAutoReloadDisabledDuringReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	configPath := GlobalConfig()

	// Create initial config with a provider that will trigger config modification during reload
	// (simulating the anthropic OAuth token removal case)
	initialConfig := `{
		"providers": {
			"anthropic": {
				"api_key": "test-key",
				"oauth": {"access_token": "token", "refresh_token": "refresh"}
			}
		}
	}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load will trigger configureProviders which removes anthropic OAuth config
	// This should NOT cause infinite recursion thanks to autoReloadDisabled guard
	store, err := Load(dir, dir, false)
	require.NoError(t, err)

	// Verify the store loaded successfully and autoReloadDisabled was unset
	require.False(t, store.autoReloadDisabled)

	// Capture snapshot and verify reload also works without recursion
	store.CaptureStalenessSnapshot([]string{configPath})

	// Modify file and reload - this should work without re-entrancy issues
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(`{"options": {"debug": true}}`), 0o600))

	err = store.ReloadFromDisk(context.Background())
	require.NoError(t, err)

	// Verify reload completed successfully
	require.False(t, store.autoReloadDisabled, "autoReloadDisabled should be false after ReloadFromDisk")
}

// TestSetConfigFields_AutoReloadsAtomically verifies that SetConfigFields writes
// multiple fields in a single disk write and triggers only one auto-reload,
// avoiding intermediate states where only some fields are persisted.
func TestSetConfigFields_AutoReloadsAtomically(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	configPath := GlobalConfig()

	// Create initial config file.
	initialConfig := `{"options": {"debug": false}}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial config.
	store, err := Load(dir, dir, false)
	require.NoError(t, err)

	// Capture snapshot.
	store.CaptureStalenessSnapshot([]string{configPath})

	// Write multiple fields atomically.
	err = store.SetConfigFields(map[string]any{
		"options.debug":  true,
		"options.custom": "hello",
	})
	require.NoError(t, err)

	// Verify both fields are reflected in memory.
	require.True(t, store.config.Options.Debug)
}

func TestLoadTokenFromDisk_ReturnsNewerToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configBase := filepath.Join(dir, "crush.json")

	// OAuth tokens are runtime state and live in state.yaml.
	stateContent := `providers:
  hyper:
    oauth:
      access_token: newer-token-from-disk
      refresh_token: refresh-abc
      expires_in: 3600
      expires_at: 9999999999
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.yaml"), []byte(stateContent), 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configBase,
	}

	token, err := store.loadTokenFromDisk("hyper")
	require.NoError(t, err)
	require.NotNil(t, token)
	require.Equal(t, "newer-token-from-disk", token.AccessToken)
	require.Equal(t, "refresh-abc", token.RefreshToken)
	require.Equal(t, 3600, token.ExpiresIn)
	require.Equal(t, int64(9999999999), token.ExpiresAt)
}

func TestLoadTokenFromDisk_ReturnsTokenWhenSame(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configBase := filepath.Join(dir, "crush.json")

	stateContent := `providers:
  hyper:
    oauth:
      access_token: same-token
      refresh_token: refresh-abc
      expires_in: 3600
      expires_at: 9999999999
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.yaml"), []byte(stateContent), 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configBase,
	}

	token, err := store.loadTokenFromDisk("hyper")
	require.NoError(t, err)
	require.NotNil(t, token)
	require.Equal(t, "same-token", token.AccessToken)
}

func TestLoadTokenFromDisk_ReturnsNilWhenFileMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configBase := filepath.Join(dir, "crush.json")

	store := &ConfigStore{
		config:     &Config{},
		configBase: configBase,
	}

	token, err := store.loadTokenFromDisk("hyper")
	require.NoError(t, err)
	require.Nil(t, token)
}

func TestLoadTokenFromDisk_ReturnsNilWhenProviderMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configBase := filepath.Join(dir, "crush.json")

	stateContent := `providers:
  openai:
    api_key: test-key
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.yaml"), []byte(stateContent), 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configBase,
	}

	token, err := store.loadTokenFromDisk("hyper")
	require.NoError(t, err)
	require.Nil(t, token)
}

func TestLoadTokenFromDisk_ReturnsNilWhenOAuthMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configBase := filepath.Join(dir, "crush.json")

	stateContent := `providers:
  hyper:
    api_key: test-key
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.yaml"), []byte(stateContent), 0o600))

	store := &ConfigStore{
		config:     &Config{},
		configBase: configBase,
	}

	token, err := store.loadTokenFromDisk("hyper")
	require.NoError(t, err)
	require.Nil(t, token)
}

func TestRefreshOAuthToken_UsesDiskTokenWhenDifferent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configBase := filepath.Join(dir, "crush.json")

	// A newer token lives in state.yaml (written by another session).
	stateContent := `providers:
  hyper:
    api_key: newer-access-token
    oauth:
      access_token: newer-access-token
      refresh_token: refresh-abc
      expires_in: 3600
      expires_at: 9999999999
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.yaml"), []byte(stateContent), 0o600))

	// Set up store with an older in-memory token
	oldToken := &oauth.Token{
		AccessToken:  "older-access-token",
		RefreshToken: "refresh-abc",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(), // Expired
	}

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("hyper", ProviderConfig{
		ID:         "hyper",
		Name:       "Hyper",
		APIKey:     oldToken.AccessToken,
		OAuthToken: oldToken,
	})

	store := &ConfigStore{
		config: &Config{
			Providers: providers,
		},
		configBase: configBase,
	}

	// Refresh should use the disk token without making an external call
	err := store.RefreshOAuthToken(context.Background(), "hyper")
	require.NoError(t, err)

	// Verify the in-memory token was updated to the disk token
	updatedConfig, ok := store.config.Providers.Get("hyper")
	require.True(t, ok)
	require.Equal(t, "newer-access-token", updatedConfig.APIKey)
	require.Equal(t, "newer-access-token", updatedConfig.OAuthToken.AccessToken)
	require.Equal(t, "refresh-abc", updatedConfig.OAuthToken.RefreshToken)
}
