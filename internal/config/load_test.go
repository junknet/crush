package config

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	exitVal := m.Run()
	os.Exit(exitVal)
}

func TestConfig_LoadFromBytes(t *testing.T) {
	data1 := []byte(`{"providers": {"openai": {"api_key": "key1", "base_url": "https://api.openai.com/v1"}}}`)
	data2 := []byte(`{"providers": {"openai": {"api_key": "key2", "base_url": "https://api.openai.com/v2"}}}`)
	data3 := []byte(`{"providers": {"openai": {}}}`)

	loadedConfig, err := loadFromBytes([][]byte{data1, data2, data3})

	require.NoError(t, err)
	require.NotNil(t, loadedConfig)
	require.Equal(t, 1, loadedConfig.Providers.Len())
	pc, _ := loadedConfig.Providers.Get("openai")
	require.Equal(t, "key2", pc.APIKey)
	require.Equal(t, "https://api.openai.com/v2", pc.BaseURL)
}

func TestConfig_LoadFromBytes_DeduplicatesProviderModels(t *testing.T) {
	data1 := []byte(`{"providers": {"waitai-openai": {"models": [{"id": "gpt-5.5", "name": "GPT 5.5 v1"}]}}}`)
	data2 := []byte(`{"providers": {"waitai-openai": {"models": [{"id": "gpt-5.5", "name": "GPT 5.5 v2"}, {"id": "gpt-4o", "name": "GPT 4o"}]}}}`)

	loadedConfig, err := loadFromBytes([][]byte{data1, data2})

	require.NoError(t, err)
	require.NotNil(t, loadedConfig)
	require.Equal(t, 1, loadedConfig.Providers.Len())
	prov, ok := loadedConfig.Providers.Get("waitai-openai")
	require.True(t, ok)
	require.Len(t, prov.Models, 2)
	require.Equal(t, "gpt-5.5", prov.Models[0].ID)
	require.Equal(t, "GPT 5.5 v2", prov.Models[0].Name)
	require.Equal(t, "gpt-4o", prov.Models[1].ID)
}

func TestConfig_ConfigureProvidersEnrichesCustomModelMetadata(t *testing.T) {
	t.Parallel()

	knownProviders := []catwalk.Provider{{
		ID:   catwalk.InferenceProvider("opencode-zen"),
		Name: "OpenCode Zen",
		Models: []catwalk.Model{{
			ID:               "claude-opus-4-7",
			Name:             "Claude Opus 4.7",
			ContextWindow:    1_000_000,
			DefaultMaxTokens: 128_000,
			CanReason:        true,
		}},
	}}
	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"waitai-anthropic": {
				ID:      "waitai-anthropic",
				BaseURL: "http://127.0.0.1:43917/v1",
				APIKey:  "test-key",
				Models: []catwalk.Model{{
					ID: "claude-opus-4-7",
				}},
			},
		}),
	}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{})
	resolver := NewShellVariableResolver(env)

	require.NoError(t, cfg.configureProviders(testStore(cfg), env, resolver, knownProviders))

	provider, ok := cfg.Providers.Get("waitai-anthropic")
	require.True(t, ok)
	require.Len(t, provider.Models, 1)
	require.Equal(t, int64(1_000_000), provider.Models[0].ContextWindow)
	require.Equal(t, int64(128_000), provider.Models[0].DefaultMaxTokens)
	require.True(t, provider.Models[0].CanReason)
}

// TestLookupConfigs_SingleLocation verifies the single-location model:
// lookupConfigs resolves only the global config base candidates and the
// runtime state candidates, never a project- or parent-local crush.json.
func TestLookupConfigs_SingleLocation(t *testing.T) {
	// Force GlobalConfig and GlobalConfigData to point at locations we
	// control so they can be asserted without polluting the developer's
	// real config.
	globalDir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", globalDir)
	t.Setenv("CRUSH_GLOBAL_DATA", globalDir)

	t.Run("does not pick up project- or parent-local crush.json", func(t *testing.T) {
		parent := t.TempDir()

		// A crush.json next to / above the project must never be adopted.
		require.NoError(t, os.WriteFile(
			filepath.Join(parent, "crush.json"),
			[]byte(`{}`),
			0o644,
		))

		project := filepath.Join(parent, "project")
		require.NoError(t, os.Mkdir(project, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(project, "crush.json"),
			[]byte(`{}`),
			0o644,
		))

		got := lookupConfigs(project)
		for _, p := range got {
			require.NotEqual(t, filepath.Join(parent, "crush.json"), p)
			require.NotEqual(t, filepath.Join(project, "crush.json"), p)
		}
	})

	t.Run("resolves only global config and state candidates", func(t *testing.T) {
		project := t.TempDir()

		got := lookupConfigs(project)
		// Exactly the global config base candidates plus the runtime state
		// candidates, regardless of working directory.
		want := append(append([]string{}, configCandidates(GlobalConfig())...), stateConfigCandidates(GlobalConfig())...)
		require.Equal(t, want, got)
		require.Contains(t, got, GlobalConfig())
	})
}

func TestLoadFromConfigPaths_InvalidJSON(t *testing.T) {
	t.Parallel()

	t.Run("identifies the offending file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		good := filepath.Join(tmpDir, "good.json")
		bad := filepath.Join(tmpDir, "bad.json")
		require.NoError(t, os.WriteFile(good, []byte(`{"providers":{}}`), 0o644))
		require.NoError(t, os.WriteFile(bad, []byte(`{not valid json}`), 0o644))

		_, _, err := loadFromConfigPaths([]string{good, bad})
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid JSON in config file")
		require.Contains(t, err.Error(), "bad.json")
	})

	t.Run("skips missing and empty files", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		empty := filepath.Join(tmpDir, "empty.json")
		require.NoError(t, os.WriteFile(empty, []byte(""), 0o644))

		cfg, _, err := loadFromConfigPaths([]string{
			filepath.Join(tmpDir, "nonexistent.json"),
			empty,
		})
		require.NoError(t, err)
		require.NotNil(t, cfg)
	})
}

func TestLoadFromConfigPaths_YAML(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "crush.llm.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(`
providers:
  waitai-openai:
    id: waitai-openai
    name: WaitAI OpenAI
    base_url: http://127.0.0.1:43917/v1
    api_key: test-key
    models:
      - id: gpt-5.5
        name: GPT 5.5
models:
  brain:
    model: claude-opus-4-7
    provider: waitai-openai
`), 0o644))

	cfg, loaded, err := loadFromConfigPaths([]string{yamlPath})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, []string{yamlPath}, loaded)

	prov, ok := cfg.Providers.Get("waitai-openai")
	require.True(t, ok)
	require.Equal(t, "test-key", prov.APIKey)
	require.Equal(t, "http://127.0.0.1:43917/v1", prov.BaseURL)
	require.Equal(t, "claude-opus-4-7", cfg.Models[SelectedModelTypeBrain].Model)
}

// testStore wraps a Config in a minimal ConfigStore for testing.
func testStore(cfg *Config) *ConfigStore {
	return &ConfigStore{config: cfg}
}

func TestConfig_setDefaults(t *testing.T) {
	t.Run("sets default data directory", func(t *testing.T) {
		cfg := &Config{}
		workingDir := t.TempDir()

		cfg.setDefaults(workingDir, "")

		require.NotNil(t, cfg.Options)
		require.NotNil(t, cfg.Options.TUI)
		require.NotNil(t, cfg.Options.ContextPaths)
		require.NotNil(t, cfg.Providers)
		require.NotNil(t, cfg.Models)
		require.NotNil(t, cfg.LSP)
		require.NotNil(t, cfg.MCP)
		require.Equal(t, filepath.Join(workingDir, ".crush"), cfg.Options.DataDirectory)
		require.Equal(t, "CLAUDE.md", cfg.Options.InitializeAs)
		for _, path := range defaultContextPaths {
			require.Contains(t, cfg.Options.ContextPaths, path)
		}
	})

	t.Run("resolves relative configured data directory from working directory", func(t *testing.T) {
		cfg := &Config{Options: &Options{DataDirectory: "."}}
		workingDir := filepath.Join(t.TempDir(), "worktree")

		cfg.setDefaults(workingDir, "")

		require.Equal(t, workingDir, cfg.Options.DataDirectory)
	})

	t.Run("resolves relative flag data directory from working directory", func(t *testing.T) {
		cfg := &Config{}
		workingDir := filepath.Join(t.TempDir(), "worktree")

		cfg.setDefaults(workingDir, "./state")

		require.Equal(t, filepath.Join(workingDir, "state"), cfg.Options.DataDirectory)
	})

	t.Run("preserves absolute configured data directory", func(t *testing.T) {
		// Use a platform-appropriate absolute path so the test runs
		// the same way on POSIX and Windows.
		absDir := filepath.Join(t.TempDir(), "data")
		cfg := &Config{Options: &Options{DataDirectory: absDir}}

		cfg.setDefaults(filepath.Join(t.TempDir(), "worktree"), "")

		require.Equal(t, absDir, cfg.Options.DataDirectory)
	})

	t.Run("workspace merge re-entry keeps an absolute data directory", func(t *testing.T) {
		// Simulate the load and reload paths: defaults are applied
		// twice with the data directory potentially carried through
		// from an earlier merge as a relative string.
		workingDir := filepath.Join(t.TempDir(), "worktree")
		cfg := &Config{}
		cfg.setDefaults(workingDir, "")

		// Workspace JSON sets data_directory to a relative value; the
		// merge replaces the struct, then setDefaults runs again.
		cfg.Options.DataDirectory = "./state"
		cfg.setDefaults(workingDir, "")

		require.True(t, filepath.IsAbs(cfg.Options.DataDirectory),
			"data directory must remain absolute after re-merge, got %q",
			cfg.Options.DataDirectory)
		require.Equal(t, filepath.Join(workingDir, "state"), cfg.Options.DataDirectory)
	})

	t.Run("does not adopt .crush from a parent project", func(t *testing.T) {
		parent := t.TempDir()

		// .crush in the parent: it should not be reused by the child
		// because there is no git context joining them.
		require.NoError(t, os.Mkdir(filepath.Join(parent, defaultDataDirectory), 0o755))

		child := filepath.Join(parent, "child")
		require.NoError(t, os.Mkdir(child, 0o755))

		cfg := &Config{}
		cfg.setDefaults(child, "")

		require.Equal(
			t,
			filepath.Clean(filepath.Join(child, defaultDataDirectory)),
			filepath.Clean(cfg.Options.DataDirectory),
		)
	})

	t.Run("does not climb out of git worktree to find .crush", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}

		parent := t.TempDir()

		// Stray .crush above the worktree root.
		require.NoError(t, os.Mkdir(filepath.Join(parent, defaultDataDirectory), 0o755))

		worktree := filepath.Join(parent, "worktree")
		require.NoError(t, os.Mkdir(worktree, 0o755))

		sub := filepath.Join(worktree, "pkg")
		require.NoError(t, os.Mkdir(sub, 0o755))

		// Make worktree a real git repo so the boundary detection
		// resolves to it, mirroring what happens with linked worktrees
		// in real usage.
		gitInit := exec.CommandContext(t.Context(), "git", "init", "-q")
		gitInit.Dir = worktree
		require.NoError(t, gitInit.Run())

		cfg := &Config{}
		cfg.setDefaults(sub, "")

		// Resolve symlinks because TempDir on macOS sits under /var
		// which is a symlink to /private/var. The data directory has
		// not been created yet, so resolve its parent and join.
		gotDir, gotName := filepath.Split(cfg.Options.DataDirectory)
		gotEvalDir, err := filepath.EvalSymlinks(filepath.Clean(gotDir))
		require.NoError(t, err)
		gotEval := filepath.Join(gotEvalDir, gotName)

		strayEval, err := filepath.EvalSymlinks(filepath.Join(parent, defaultDataDirectory))
		require.NoError(t, err)
		require.NotEqual(t, strayEval, gotEval, "must not adopt parent .crush")

		subEval, err := filepath.EvalSymlinks(sub)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(subEval, defaultDataDirectory), gotEval)
	})
}

func TestConfig_configureProviders(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models: []catwalk.Model{{
				ID: "test-model",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	require.Equal(t, 1, cfg.Providers.Len())

	// We want to make sure that we keep the configured API key as a placeholder
	pc, _ := cfg.Providers.Get("openai")
	require.Equal(t, "$OPENAI_API_KEY", pc.APIKey)
}

func TestConfig_configureProvidersWithOverride(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models: []catwalk.Model{{
				ID: "test-model",
			}},
		},
	}

	cfg := &Config{
		Providers: csync.NewMap[string, ProviderConfig](),
	}
	cfg.Providers.Set("openai", ProviderConfig{
		APIKey:  "xyz",
		BaseURL: "https://api.openai.com/v2",
		Models: []catwalk.Model{
			{
				ID:   "test-model",
				Name: "Updated",
			},
			{
				ID: "another-model",
			},
		},
	})
	cfg.setDefaults("/tmp", "")

	env := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	require.Equal(t, 1, cfg.Providers.Len())

	// We want to make sure that we keep the configured API key as a placeholder
	pc, _ := cfg.Providers.Get("openai")
	require.Equal(t, "xyz", pc.APIKey)
	require.Equal(t, "https://api.openai.com/v2", pc.BaseURL)
	require.Len(t, pc.Models, 2)
	require.Equal(t, "Updated", pc.Models[0].Name)
}

func TestConfig_configureProvidersWithNewProvider(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models: []catwalk.Model{{
				ID: "test-model",
			}},
		},
	}

	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"custom": {
				APIKey:  "xyz",
				BaseURL: "https://api.someendpoint.com/v2",
				Models: []catwalk.Model{
					{
						ID: "test-model",
					},
				},
			},
		}),
	}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	// Should be to because of the env variable
	require.Equal(t, cfg.Providers.Len(), 2)

	// We want to make sure that we keep the configured API key as a placeholder
	pc, _ := cfg.Providers.Get("custom")
	require.Equal(t, "xyz", pc.APIKey)
	// Make sure we set the ID correctly
	require.Equal(t, "custom", pc.ID)
	require.Equal(t, "https://api.someendpoint.com/v2", pc.BaseURL)
	require.Len(t, pc.Models, 1)

	_, ok := cfg.Providers.Get("openai")
	require.True(t, ok, "OpenAI provider should still be present")
}

func TestConfig_configureProvidersBedrockWithCredentials(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          catwalk.InferenceProviderBedrock,
			APIKey:      "",
			APIEndpoint: "",
			Models: []catwalk.Model{{
				ID: "anthropic.claude-sonnet-4-20250514-v1:0",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"AWS_ACCESS_KEY_ID":     "test-key-id",
		"AWS_SECRET_ACCESS_KEY": "test-secret-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	require.Equal(t, cfg.Providers.Len(), 1)

	bedrockProvider, ok := cfg.Providers.Get("bedrock")
	require.True(t, ok, "Bedrock provider should be present")
	require.Len(t, bedrockProvider.Models, 1)
	require.Equal(t, "anthropic.claude-sonnet-4-20250514-v1:0", bedrockProvider.Models[0].ID)
}

func TestConfig_configureProvidersBedrockWithoutCredentials(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          catwalk.InferenceProviderBedrock,
			APIKey:      "",
			APIEndpoint: "",
			Models: []catwalk.Model{{
				ID: "anthropic.claude-sonnet-4-20250514-v1:0",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	// Provider should not be configured without credentials
	require.Equal(t, cfg.Providers.Len(), 0)
}

func TestConfig_configureProvidersBedrockWithoutUnsupportedModel(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          catwalk.InferenceProviderBedrock,
			APIKey:      "",
			APIEndpoint: "",
			Models: []catwalk.Model{{
				ID: "some-random-model",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"AWS_ACCESS_KEY_ID":     "test-key-id",
		"AWS_SECRET_ACCESS_KEY": "test-secret-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.Error(t, err)
}

func TestConfig_configureProvidersVertexAIWithCredentials(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          catwalk.InferenceProviderVertexAI,
			APIKey:      "",
			APIEndpoint: "",
			Models: []catwalk.Model{{
				ID: "gemini-pro",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"VERTEXAI_PROJECT":  "test-project",
		"VERTEXAI_LOCATION": "us-central1",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	require.Equal(t, cfg.Providers.Len(), 1)

	vertexProvider, ok := cfg.Providers.Get("vertexai")
	require.True(t, ok, "VertexAI provider should be present")
	require.Len(t, vertexProvider.Models, 1)
	require.Equal(t, "gemini-pro", vertexProvider.Models[0].ID)
	require.Equal(t, "test-project", vertexProvider.ExtraParams["project"])
	require.Equal(t, "us-central1", vertexProvider.ExtraParams["location"])
}

func TestConfig_configureProvidersVertexAIWithoutCredentials(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          catwalk.InferenceProviderVertexAI,
			APIKey:      "",
			APIEndpoint: "",
			Models: []catwalk.Model{{
				ID: "gemini-pro",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"GOOGLE_GENAI_USE_VERTEXAI": "false",
		"GOOGLE_CLOUD_PROJECT":      "test-project",
		"GOOGLE_CLOUD_LOCATION":     "us-central1",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	// Provider should not be configured without proper credentials
	require.Equal(t, cfg.Providers.Len(), 0)
}

func TestConfig_configureProvidersVertexAIMissingProject(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          catwalk.InferenceProviderVertexAI,
			APIKey:      "",
			APIEndpoint: "",
			Models: []catwalk.Model{{
				ID: "gemini-pro",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"GOOGLE_GENAI_USE_VERTEXAI": "true",
		"GOOGLE_CLOUD_LOCATION":     "us-central1",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	// Provider should not be configured without project
	require.Equal(t, cfg.Providers.Len(), 0)
}

func TestConfig_configureProvidersSetProviderID(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models: []catwalk.Model{{
				ID: "test-model",
			}},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	require.Equal(t, cfg.Providers.Len(), 1)

	// Provider ID should be set
	pc, _ := cfg.Providers.Get("openai")
	require.Equal(t, "openai", pc.ID)
}

func TestConfig_EnabledProviders(t *testing.T) {
	t.Run("all providers enabled", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					ID:      "openai",
					APIKey:  "key1",
					Disable: false,
				},
				"anthropic": {
					ID:      "anthropic",
					APIKey:  "key2",
					Disable: false,
				},
			}),
		}

		enabled := cfg.EnabledProviders()
		require.Len(t, enabled, 2)
	})

	t.Run("some providers disabled", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					ID:      "openai",
					APIKey:  "key1",
					Disable: false,
				},
				"anthropic": {
					ID:      "anthropic",
					APIKey:  "key2",
					Disable: true,
				},
			}),
		}

		enabled := cfg.EnabledProviders()
		require.Len(t, enabled, 1)
		require.Equal(t, "openai", enabled[0].ID)
	})

	t.Run("empty providers map", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMap[string, ProviderConfig](),
		}

		enabled := cfg.EnabledProviders()
		require.Len(t, enabled, 0)
	})
}

func TestConfig_IsConfigured(t *testing.T) {
	t.Run("returns true when at least one provider is enabled", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					ID:      "openai",
					APIKey:  "key1",
					Disable: false,
				},
			}),
		}

		require.True(t, cfg.IsConfigured())
	})

	t.Run("returns false when no providers are configured", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMap[string, ProviderConfig](),
		}

		require.False(t, cfg.IsConfigured())
	})

	t.Run("returns false when all providers are disabled", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					ID:      "openai",
					APIKey:  "key1",
					Disable: true,
				},
				"anthropic": {
					ID:      "anthropic",
					APIKey:  "key2",
					Disable: true,
				},
			}),
		}

		require.False(t, cfg.IsConfigured())
	})
}

func TestConfig_setupAgentsWithNoDisabledTools(t *testing.T) {
	cfg := &Config{
		Options: &Options{
			DisabledTools: []string{},
		},
	}

	cfg.SetupAgents()
	allowedTools := resolveAllowedTools(allToolNames(), cfg.Options.DisabledTools)
	brainAgent, ok := cfg.Agents[AgentBrain]
	require.True(t, ok)
	assert.Equal(t, allowedTools, brainAgent.AllowedTools)

	workerAgent, ok := cfg.Agents[AgentWorker]
	require.True(t, ok)
	assert.Equal(t, allowedTools, workerAgent.AllowedTools)

	exploreAgent, ok := cfg.Agents[AgentExplore]
	require.True(t, ok)
	assert.Equal(t, resolveExploreTools(allowedTools), exploreAgent.AllowedTools)
}

func TestConfig_setupAgentsWithDisabledTools(t *testing.T) {
	cfg := &Config{
		Options: &Options{
			DisabledTools: []string{
				"edit",
				"download",
				"rg",
			},
		},
	}

	cfg.SetupAgents()
	allowedTools := resolveAllowedTools(allToolNames(), cfg.Options.DisabledTools)
	brainAgent, ok := cfg.Agents[AgentBrain]
	require.True(t, ok)
	assert.Equal(t, allowedTools, brainAgent.AllowedTools)

	workerAgent, ok := cfg.Agents[AgentWorker]
	require.True(t, ok)

	assert.Equal(t, allowedTools, workerAgent.AllowedTools)

	exploreAgent, ok := cfg.Agents[AgentExplore]
	require.True(t, ok)
	assert.Equal(t, resolveExploreTools(allowedTools), exploreAgent.AllowedTools)
}

func TestConfig_setupAgentsWithEveryExploreToolDisabled(t *testing.T) {
	disabledTools := resolveExploreTools(allToolNames())
	cfg := &Config{
		Options: &Options{
			DisabledTools: disabledTools,
		},
	}

	cfg.SetupAgents()
	allowedTools := resolveAllowedTools(allToolNames(), disabledTools)
	brainAgent, ok := cfg.Agents[AgentBrain]
	require.True(t, ok)
	assert.Equal(t, allowedTools, brainAgent.AllowedTools)

	workerAgent, ok := cfg.Agents[AgentWorker]
	require.True(t, ok)
	assert.Equal(t, allowedTools, workerAgent.AllowedTools)

	exploreAgent, ok := cfg.Agents[AgentExplore]
	require.True(t, ok)
	assert.Len(t, exploreAgent.AllowedTools, 0)
}

func TestConfig_setupAgentsOverlaysCustomAgents(t *testing.T) {
	cfg := &Config{
		Options: &Options{
			DisabledTools: []string{},
		},
		Agents: map[string]Agent{
			"review": {
				Name:         "Review",
				Description:  "Custom review agent",
				Model:        SelectedModelTypeExplore,
				AllowedTools: []string{"fd", "rg"},
				ContextPaths: []string{"docs"},
			},
		},
	}

	cfg.SetupAgents()
	review, ok := cfg.Agents["review"]
	require.True(t, ok)
	require.Equal(t, "Review", review.Name)
	require.Equal(t, SelectedModelTypeExplore, review.Model)
	require.Equal(t, []string{"fd", "rg"}, review.AllowedTools)
	require.Contains(t, review.ContextPaths, "docs")
	require.Equal(t, AgentBrain, cfg.DefaultAgent)
}

func TestConfig_configureProvidersWithDisabledProvider(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models: []catwalk.Model{{
				ID: "test-model",
			}},
		},
	}

	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"openai": {
				Disable: true,
			},
		}),
	}
	cfg.setDefaults("/tmp", "")

	env := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)

	require.Equal(t, cfg.Providers.Len(), 1)
	prov, exists := cfg.Providers.Get("openai")
	require.True(t, exists)
	require.True(t, prov.Disable)
}

func TestConfig_configureProvidersCustomProviderValidation(t *testing.T) {
	t.Run("custom provider with missing API key is allowed, but not known providers", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					BaseURL: "https://api.custom.com/v1",
					Models: []catwalk.Model{{
						ID: "test-model",
					}},
				},
				"openai": {
					APIKey: "$MISSING",
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 1)
		_, exists := cfg.Providers.Get("custom")
		require.True(t, exists)
	})

	t.Run("custom provider with missing BaseURL is removed", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey: "test-key",
					Models: []catwalk.Model{{
						ID: "test-model",
					}},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 0)
		_, exists := cfg.Providers.Get("custom")
		require.False(t, exists)
	})

	t.Run("custom provider with no models is removed", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey:  "test-key",
					BaseURL: "https://api.custom.com/v1",
					Models:  []catwalk.Model{},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 0)
		_, exists := cfg.Providers.Get("custom")
		require.False(t, exists)
	})

	t.Run("custom provider with unsupported type is removed", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey:  "test-key",
					BaseURL: "https://api.custom.com/v1",
					Type:    "unsupported",
					Models: []catwalk.Model{{
						ID: "test-model",
					}},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 0)
		_, exists := cfg.Providers.Get("custom")
		require.False(t, exists)
	})

	t.Run("valid custom provider is kept and ID is set", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey:  "test-key",
					BaseURL: "https://api.custom.com/v1",
					Type:    catwalk.TypeOpenAI,
					Models: []catwalk.Model{{
						ID: "test-model",
					}},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 1)
		customProvider, exists := cfg.Providers.Get("custom")
		require.True(t, exists)
		require.Equal(t, "custom", customProvider.ID)
		require.Equal(t, "test-key", customProvider.APIKey)
		require.Equal(t, "https://api.custom.com/v1", customProvider.BaseURL)
	})

	t.Run("custom anthropic provider is supported", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom-anthropic": {
					APIKey:  "test-key",
					BaseURL: "https://api.anthropic.com/v1",
					Type:    catwalk.TypeAnthropic,
					Models: []catwalk.Model{{
						ID: "claude-3-sonnet",
					}},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 1)
		customProvider, exists := cfg.Providers.Get("custom-anthropic")
		require.True(t, exists)
		require.Equal(t, "custom-anthropic", customProvider.ID)
		require.Equal(t, "test-key", customProvider.APIKey)
		require.Equal(t, "https://api.anthropic.com/v1", customProvider.BaseURL)
		require.Equal(t, catwalk.TypeAnthropic, customProvider.Type)
	})

	t.Run("disabled custom provider is removed", func(t *testing.T) {
		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey:  "test-key",
					BaseURL: "https://api.custom.com/v1",
					Type:    catwalk.TypeOpenAI,
					Disable: true,
					Models: []catwalk.Model{{
						ID: "test-model",
					}},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 0)
		_, exists := cfg.Providers.Get("custom")
		require.False(t, exists)
	})
}

func TestConfig_configureProvidersEnhancedCredentialValidation(t *testing.T) {
	t.Run("VertexAI provider removed when credentials missing with existing config", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:          catwalk.InferenceProviderVertexAI,
				APIKey:      "",
				APIEndpoint: "",
				Models: []catwalk.Model{{
					ID: "gemini-pro",
				}},
			},
		}

		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"vertexai": {
					BaseURL: "custom-url",
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{
			"GOOGLE_GENAI_USE_VERTEXAI": "false",
		})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 0)
		_, exists := cfg.Providers.Get("vertexai")
		require.False(t, exists)
	})

	t.Run("Bedrock provider removed when AWS credentials missing with existing config", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:          catwalk.InferenceProviderBedrock,
				APIKey:      "",
				APIEndpoint: "",
				Models: []catwalk.Model{{
					ID: "anthropic.claude-sonnet-4-20250514-v1:0",
				}},
			},
		}

		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"bedrock": {
					BaseURL: "custom-url",
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 0)
		_, exists := cfg.Providers.Get("bedrock")
		require.False(t, exists)
	})

	t.Run("provider removed when API key missing with existing config", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:          "openai",
				APIKey:      "$MISSING_API_KEY",
				APIEndpoint: "https://api.openai.com/v1",
				Models: []catwalk.Model{{
					ID: "test-model",
				}},
			},
		}

		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					BaseURL: "custom-url",
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 0)
		_, exists := cfg.Providers.Get("openai")
		require.False(t, exists)
	})

	t.Run("known provider should still be added if the endpoint is missing the client will use default endpoints", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:          "openai",
				APIKey:      "$OPENAI_API_KEY",
				APIEndpoint: "$MISSING_ENDPOINT",
				Models: []catwalk.Model{{
					ID: "test-model",
				}},
			},
		}

		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					APIKey: "test-key",
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{
			"OPENAI_API_KEY": "test-key",
		})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		require.Equal(t, cfg.Providers.Len(), 1)
		_, exists := cfg.Providers.Get("openai")
		require.True(t, exists)
	})
}

func TestConfig_defaultModelSelection(t *testing.T) {
	t.Run("default behavior uses the default models for given provider", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "abc",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		brain, explore, err := cfg.defaultModelSelection(knownProviders)
		require.NoError(t, err)
		require.Equal(t, "brain-model", brain.Model)
		require.Equal(t, "openai", brain.Provider)
		require.Equal(t, int64(1000), brain.MaxTokens)
		require.Equal(t, "explore-model", explore.Model)
		require.Equal(t, "openai", explore.Provider)
		require.Equal(t, int64(500), explore.MaxTokens)
	})
	t.Run("should error if no providers configured", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "$MISSING_KEY",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		_, _, err = cfg.defaultModelSelection(knownProviders)
		require.Error(t, err)
	})
	t.Run("should error if model is missing", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "abc",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "not-brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)
		_, _, err = cfg.defaultModelSelection(knownProviders)
		require.Error(t, err)
	})

	t.Run("should configure the default models with a custom provider", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "$MISSING", // will not be included in the config
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "not-brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey:  "test-key",
					BaseURL: "https://api.custom.com/v1",
					Models: []catwalk.Model{
						{
							ID:               "model",
							DefaultMaxTokens: 600,
						},
					},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)
		brain, explore, err := cfg.defaultModelSelection(knownProviders)
		require.NoError(t, err)
		require.Equal(t, "model", brain.Model)
		require.Equal(t, "custom", brain.Provider)
		require.Equal(t, int64(600), brain.MaxTokens)
		require.Equal(t, "model", explore.Model)
		require.Equal(t, "custom", explore.Provider)
		require.Equal(t, int64(600), explore.MaxTokens)
	})

	t.Run("should fail if no model configured", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "$MISSING", // will not be included in the config
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "not-brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey:  "test-key",
					BaseURL: "https://api.custom.com/v1",
					Models:  []catwalk.Model{},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)
		_, _, err = cfg.defaultModelSelection(knownProviders)
		require.Error(t, err)
	})
	t.Run("should use the default provider first", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "set",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"custom": {
					APIKey:  "test-key",
					BaseURL: "https://api.custom.com/v1",
					Models: []catwalk.Model{
						{
							ID:               "brain-model",
							DefaultMaxTokens: 1000,
						},
					},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)
		brain, explore, err := cfg.defaultModelSelection(knownProviders)
		require.NoError(t, err)
		require.Equal(t, "brain-model", brain.Model)
		require.Equal(t, "openai", brain.Provider)
		require.Equal(t, int64(1000), brain.MaxTokens)
		require.Equal(t, "explore-model", explore.Model)
		require.Equal(t, "openai", explore.Provider)
		require.Equal(t, int64(500), explore.MaxTokens)
	})
}

func TestConfig_configureProvidersDisableDefaultProviders(t *testing.T) {
	t.Run("when enabled, ignores all default providers and requires full specification", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:          "openai",
				APIKey:      "$OPENAI_API_KEY",
				APIEndpoint: "https://api.openai.com/v1",
				Models: []catwalk.Model{{
					ID: "gpt-4",
				}},
			},
		}

		// User references openai but doesn't fully specify it (no base_url, no
		// models). This should be rejected because disable_default_providers
		// treats all providers as custom.
		cfg := &Config{
			Options: &Options{
				DisableDefaultProviders: true,
			},
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					APIKey: "$OPENAI_API_KEY",
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{
			"OPENAI_API_KEY": "test-key",
		})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.ErrorContains(t, err, "no custom providers")

		// openai should NOT be present because it lacks base_url and models.
		require.Equal(t, 0, cfg.Providers.Len())
		_, exists := cfg.Providers.Get("openai")
		require.False(t, exists, "openai should not be present without full specification")
	})

	t.Run("when enabled, fully specified providers work", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:          "openai",
				APIKey:      "$OPENAI_API_KEY",
				APIEndpoint: "https://api.openai.com/v1",
				Models: []catwalk.Model{{
					ID: "gpt-4",
				}},
			},
		}

		// User fully specifies their provider.
		cfg := &Config{
			Options: &Options{
				DisableDefaultProviders: true,
			},
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"my-llm": {
					APIKey:  "$MY_API_KEY",
					BaseURL: "https://my-llm.example.com/v1",
					Models: []catwalk.Model{{
						ID: "my-model",
					}},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{
			"MY_API_KEY":     "test-key",
			"OPENAI_API_KEY": "test-key",
		})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		// Only fully specified provider should be present.
		require.Equal(t, 1, cfg.Providers.Len())
		provider, exists := cfg.Providers.Get("my-llm")
		require.True(t, exists, "my-llm should be present")
		require.Equal(t, "https://my-llm.example.com/v1", provider.BaseURL)
		require.Len(t, provider.Models, 1)

		// Default openai should NOT be present.
		_, exists = cfg.Providers.Get("openai")
		require.False(t, exists, "openai should not be present")
	})

	t.Run("when disabled, includes all known providers with valid credentials", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:          "openai",
				APIKey:      "$OPENAI_API_KEY",
				APIEndpoint: "https://api.openai.com/v1",
				Models: []catwalk.Model{{
					ID: "gpt-4",
				}},
			},
			{
				ID:          "anthropic",
				APIKey:      "$ANTHROPIC_API_KEY",
				APIEndpoint: "https://api.anthropic.com/v1",
				Models: []catwalk.Model{{
					ID: "claude-3",
				}},
			},
		}

		// User only configures openai, both API keys are available, but option
		// is disabled.
		cfg := &Config{
			Options: &Options{
				DisableDefaultProviders: false,
			},
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					APIKey: "$OPENAI_API_KEY",
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{
			"OPENAI_API_KEY":    "test-key",
			"ANTHROPIC_API_KEY": "test-key",
		})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		// Both providers should be present.
		require.Equal(t, 2, cfg.Providers.Len())
		_, exists := cfg.Providers.Get("openai")
		require.True(t, exists, "openai should be present")
		_, exists = cfg.Providers.Get("anthropic")
		require.True(t, exists, "anthropic should be present")
	})

	t.Run("when enabled, provider missing models is rejected", func(t *testing.T) {
		cfg := &Config{
			Options: &Options{
				DisableDefaultProviders: true,
			},
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"my-llm": {
					APIKey:  "test-key",
					BaseURL: "https://my-llm.example.com/v1",
					Models:  []catwalk.Model{}, // No models.
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.ErrorContains(t, err, "no custom providers")

		// Provider should be rejected for missing models.
		require.Equal(t, 0, cfg.Providers.Len())
	})

	t.Run("when enabled, provider missing base_url is rejected", func(t *testing.T) {
		cfg := &Config{
			Options: &Options{
				DisableDefaultProviders: true,
			},
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"my-llm": {
					APIKey: "test-key",
					Models: []catwalk.Model{{ID: "model"}},
					// No BaseURL.
				},
			}),
		}
		cfg.setDefaults("/tmp", "")

		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, []catwalk.Provider{})
		require.ErrorContains(t, err, "no custom providers")

		// Provider should be rejected for missing base_url.
		require.Equal(t, 0, cfg.Providers.Len())
	})
}

func TestConfig_setDefaultsDisableDefaultProvidersEnvVar(t *testing.T) {
	t.Run("sets option from environment variable", func(t *testing.T) {
		t.Setenv("CRUSH_DISABLE_DEFAULT_PROVIDERS", "true")

		cfg := &Config{}
		cfg.setDefaults("/tmp", "")

		require.True(t, cfg.Options.DisableDefaultProviders)
	})

	t.Run("does not override when env var is not set", func(t *testing.T) {
		cfg := &Config{
			Options: &Options{
				DisableDefaultProviders: true,
			},
		}
		cfg.setDefaults("/tmp", "")

		require.True(t, cfg.Options.DisableDefaultProviders)
	})
}

func TestConfig_configureSelectedModels(t *testing.T) {
	t.Run("preserves role model profiles from llm config", func(t *testing.T) {
		cfg := &Config{
			Options: &Options{DisableDefaultProviders: true},
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"waitai-anthropic": {
					ID:      "waitai-anthropic",
					Name:    "WaitAI Anthropic",
					Type:    catwalk.TypeAnthropic,
					BaseURL: "http://127.0.0.1:43917",
					APIKey:  "test-key",
					Models: []catwalk.Model{
						{ID: "claude-opus-4-7", DefaultMaxTokens: 16000, CanReason: true},
						{ID: "claude-sonnet-4-6", DefaultMaxTokens: 12000, CanReason: true},
						{ID: "claude-haiku-4-5-20251001", DefaultMaxTokens: 8000},
					},
				},
			}),
			Models: map[SelectedModelType]SelectedModel{
				SelectedModelTypeBrain: {
					Provider:  "waitai-anthropic",
					Model:     "claude-opus-4-7",
					Think:     true,
					MaxTokens: 16000,
				},
				SelectedModelTypePlan: {
					Provider:  "waitai-anthropic",
					Model:     "claude-opus-4-7",
					Think:     true,
					MaxTokens: 16000,
				},
				SelectedModelTypeWorker: {
					Provider:  "waitai-anthropic",
					Model:     "claude-sonnet-4-6",
					Think:     true,
					MaxTokens: 12000,
				},
				SelectedModelTypeExplore: {
					Provider:  "waitai-anthropic",
					Model:     "claude-haiku-4-5-20251001",
					MaxTokens: 8000,
				},
			},
		}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, nil)
		require.NoError(t, err)

		err = configureSelectedModels(testStore(cfg), nil, false)
		require.NoError(t, err)

		require.Equal(t, "claude-opus-4-7", cfg.Models[SelectedModelTypeBrain].Model)
		require.Equal(t, "claude-opus-4-7", cfg.Models[SelectedModelTypePlan].Model)
		require.Equal(t, "claude-sonnet-4-6", cfg.Models[SelectedModelTypeWorker].Model)
		require.Equal(t, "claude-haiku-4-5-20251001", cfg.Models[SelectedModelTypeExplore].Model)
		require.True(t, cfg.Models[SelectedModelTypeBrain].Think)
		require.True(t, cfg.Models[SelectedModelTypePlan].Think)
		require.True(t, cfg.Models[SelectedModelTypeWorker].Think)
		require.False(t, cfg.Models[SelectedModelTypeExplore].Think)
	})

	t.Run("reload mode should not persist fallback defaults", func(t *testing.T) {
		dir := t.TempDir()
		globalPath := filepath.Join(dir, "crush.json")
		require.NoError(t, os.WriteFile(globalPath, []byte(`{"models":{"brain":{"provider":"ghost","model":"missing"}}}`), 0o600))

		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "abc",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{ID: "brain-model", DefaultMaxTokens: 1000},
					{ID: "explore-model", DefaultMaxTokens: 500},
				},
			},
		}

		cfg := &Config{
			Models: map[SelectedModelType]SelectedModel{
				SelectedModelTypeBrain: {Provider: "ghost", Model: "missing"},
			},
		}
		cfg.setDefaults(dir, "")
		store := &ConfigStore{config: cfg, configBase: globalPath}
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(store, env, resolver, knownProviders)
		require.NoError(t, err)

		err = configureSelectedModels(store, knownProviders, false)
		require.NoError(t, err)

		// In-memory falls back to default.
		require.Equal(t, "openai", cfg.Models[SelectedModelTypeBrain].Provider)
		require.Equal(t, "brain-model", cfg.Models[SelectedModelTypeBrain].Model)

		// Disk remains unchanged in reload mode.
		data, readErr := os.ReadFile(globalPath)
		require.NoError(t, readErr)
		require.Contains(t, string(data), `"provider":"ghost"`)
		require.Contains(t, string(data), `"model":"missing"`)
	})
	t.Run("rejects legacy model type keys", func(t *testing.T) {
		cfg := &Config{
			Models: map[SelectedModelType]SelectedModel{
				SelectedModelType("legacy"): {
					Provider: "openai",
					Model:    "gpt-4",
				},
			},
			Providers: csync.NewMapFrom(map[string]ProviderConfig{
				"openai": {
					ID:     "openai",
					APIKey: "abc",
					Models: []catwalk.Model{{ID: "gpt-4"}},
				},
			}),
		}
		cfg.setDefaults("/tmp", "")
		store := &ConfigStore{config: cfg}
		err := configureSelectedModels(store, nil, false)
		require.ErrorContains(t, err, "unsupported model type")
	})
	t.Run("should override defaults", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "abc",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "custom-brain-model",
						DefaultMaxTokens: 2000,
					},
					{
						ID:               "brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{
			Models: map[SelectedModelType]SelectedModel{
				"brain": {
					Model: "custom-brain-model",
				},
			},
		}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		err = configureSelectedModels(testStore(cfg), knownProviders, true)
		require.NoError(t, err)
		brain := cfg.Models[SelectedModelTypeBrain]
		plan := cfg.Models[SelectedModelTypePlan]
		explore := cfg.Models[SelectedModelTypeExplore]
		require.Equal(t, "custom-brain-model", brain.Model)
		require.Equal(t, "openai", brain.Provider)
		require.Equal(t, int64(2000), brain.MaxTokens)
		require.Equal(t, "brain-model", plan.Model)
		require.Equal(t, "openai", plan.Provider)
		require.Equal(t, int64(1000), plan.MaxTokens)
		require.Equal(t, "explore-model", explore.Model)
		require.Equal(t, "openai", explore.Provider)
		require.Equal(t, int64(500), explore.MaxTokens)
	})
	t.Run("should be possible to use multiple providers", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "abc",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
			{
				ID:                  "anthropic",
				APIKey:              "abc",
				DefaultLargeModelID: "a-brain-model",
				DefaultSmallModelID: "a-explore-model",
				Models: []catwalk.Model{
					{
						ID:               "a-brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "a-explore-model",
						DefaultMaxTokens: 200,
					},
				},
			},
		}

		cfg := &Config{
			Models: map[SelectedModelType]SelectedModel{
				"explore": {
					Model:     "a-explore-model",
					Provider:  "anthropic",
					MaxTokens: 300,
				},
			},
		}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		err = configureSelectedModels(testStore(cfg), knownProviders, true)
		require.NoError(t, err)
		brain := cfg.Models[SelectedModelTypeBrain]
		explore := cfg.Models[SelectedModelTypeExplore]
		require.Equal(t, "brain-model", brain.Model)
		require.Equal(t, "openai", brain.Provider)
		require.Equal(t, int64(1000), brain.MaxTokens)
		require.Equal(t, "a-explore-model", explore.Model)
		require.Equal(t, "anthropic", explore.Provider)
		require.Equal(t, int64(300), explore.MaxTokens)
	})

	t.Run("should override the max tokens only", func(t *testing.T) {
		knownProviders := []catwalk.Provider{
			{
				ID:                  "openai",
				APIKey:              "abc",
				DefaultLargeModelID: "brain-model",
				DefaultSmallModelID: "explore-model",
				Models: []catwalk.Model{
					{
						ID:               "brain-model",
						DefaultMaxTokens: 1000,
					},
					{
						ID:               "explore-model",
						DefaultMaxTokens: 500,
					},
				},
			},
		}

		cfg := &Config{
			Models: map[SelectedModelType]SelectedModel{
				"brain": {
					MaxTokens: 100,
				},
			},
		}
		cfg.setDefaults("/tmp", "")
		env := env.NewFromMap(map[string]string{})
		resolver := NewShellVariableResolver(env)
		err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
		require.NoError(t, err)

		err = configureSelectedModels(testStore(cfg), knownProviders, true)
		require.NoError(t, err)
		brain := cfg.Models[SelectedModelTypeBrain]
		require.Equal(t, "brain-model", brain.Model)
		require.Equal(t, "openai", brain.Provider)
		require.Equal(t, int64(100), brain.MaxTokens)
	})
}

func TestConfig_configureProviders_HyperAPIKeyFromEnv(t *testing.T) {
	// Test that HYPER_API_KEY environment variable works without config
	knownProviders := []catwalk.Provider{
		{
			ID:                  "hyper",
			APIKey:              "", // No API key in provider definition
			DefaultLargeModelID: "brain-model",
			DefaultSmallModelID: "explore-model",
			Models: []catwalk.Model{
				{
					ID:               "brain-model",
					DefaultMaxTokens: 1000,
				},
				{
					ID:               "explore-model",
					DefaultMaxTokens: 500,
				},
			},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	env := env.NewFromMap(map[string]string{
		"HYPER_API_KEY": "env-api-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	require.Equal(t, 1, cfg.Providers.Len())

	// Verify Hyper provider is configured with the env var API key
	pc, ok := cfg.Providers.Get("hyper")
	require.True(t, ok, "Hyper provider should be configured")
	require.Equal(t, "env-api-key", pc.APIKey)
	require.Equal(t, "env-api-key", pc.APIKeyTemplate)
}

func TestConfig_configureProviders_HyperAPIKeyFromConfigOverrides(t *testing.T) {
	// Test that config API key takes precedence when HYPER_API_KEY is also set
	knownProviders := []catwalk.Provider{
		{
			ID:                  "hyper",
			APIKey:              "provider-api-key",
			DefaultLargeModelID: "brain-model",
			DefaultSmallModelID: "explore-model",
			Models: []catwalk.Model{
				{
					ID:               "brain-model",
					DefaultMaxTokens: 1000,
				},
				{
					ID:               "explore-model",
					DefaultMaxTokens: 500,
				},
			},
		},
	}

	// User has Hyper configured with an API key
	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"hyper": {
				APIKey: "config-api-key",
			},
		}),
	}
	cfg.setDefaults("/tmp", "")

	// But they also have HYPER_API_KEY set - env var should take precedence
	env := env.NewFromMap(map[string]string{
		"HYPER_API_KEY": "env-api-key",
	})
	resolver := NewShellVariableResolver(env)
	err := cfg.configureProviders(testStore(cfg), env, resolver, knownProviders)
	require.NoError(t, err)
	require.Equal(t, 1, cfg.Providers.Len())

	// Verify env var takes precedence (as per requirements)
	pc, ok := cfg.Providers.Get("hyper")
	require.True(t, ok, "Hyper provider should be configured")
	require.Equal(t, "env-api-key", pc.APIKey)
}

// TestConfig_configureProviders_ProviderHeaderResolveError verifies
// that a failing $(cmd) in a provider header fails the provider load
// with a clear message that names the offending header. Provider
// headers share the MCP error contract.
func TestConfig_configureProviders_ProviderHeaderResolveError(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models:      []catwalk.Model{{ID: "test-model"}},
		},
	}

	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"openai": {
				ExtraHeaders: map[string]string{
					// Failing $(...) — inner command exits 1. Must
					// propagate as an error, not a silent truncation.
					"X-Broken": "$(false)",
				},
			},
		}),
	}
	cfg.setDefaults("/tmp", "")

	testEnv := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
		"PATH":           os.Getenv("PATH"),
	})
	resolver := NewShellVariableResolver(testEnv)

	err := cfg.configureProviders(testStore(cfg), testEnv, resolver, knownProviders)
	require.Error(t, err, "failing $(cmd) in a header must fail the provider load")
	require.Contains(t, err.Error(), "X-Broken", "error must name the offending header")
}

// TestConfig_configureProviders_CatwalkDefaultWithUnsetVarLoads
// verifies that a Catwalk-style default header like
// "OpenAI-Organization": "$OPENAI_ORG_ID" loads cleanly under lenient
// nounset (unset → "" → header dropped), and does not fail the load
// or leave the literal template on the wire.
func TestConfig_configureProviders_CatwalkDefaultWithUnsetVarLoads(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models:      []catwalk.Model{{ID: "test-model"}},
			DefaultHeaders: map[string]string{
				"OpenAI-Organization": "$OPENAI_ORG_ID",
			},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")

	testEnv := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
		"PATH":           os.Getenv("PATH"),
	})
	resolver := NewShellVariableResolver(testEnv)

	err := cfg.configureProviders(testStore(cfg), testEnv, resolver, knownProviders)
	require.NoError(t, err, "optional env-gated header must not fail the load")

	pc, ok := cfg.Providers.Get("openai")
	require.True(t, ok, "openai provider must still be configured")
	_, present := pc.ExtraHeaders["OpenAI-Organization"]
	require.False(t, present, "header whose value resolves to empty must be absent")
}

// TestConfig_configureProviders_LiteralEmptyHeaderDropped pins design
// decision #18 for the literal case: a user-authored
// "X-Custom": "" in extra_headers is absent from the resolved map.
// Applies to both known- and custom-provider paths; this test
// exercises the custom-provider loop.
func TestConfig_configureProviders_LiteralEmptyHeaderDropped(t *testing.T) {
	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"my-llm": {
				APIKey:  "test-key",
				BaseURL: "https://my-llm.example.com/v1",
				Type:    catwalk.TypeOpenAI,
				Models:  []catwalk.Model{{ID: "m"}},
				ExtraHeaders: map[string]string{
					"X-Custom": "",
					"X-Kept":   "present",
				},
			},
		}),
	}
	cfg.setDefaults("/tmp", "")

	testEnv := env.NewFromMap(map[string]string{
		"PATH": os.Getenv("PATH"),
	})
	resolver := NewShellVariableResolver(testEnv)

	err := cfg.configureProviders(testStore(cfg), testEnv, resolver, []catwalk.Provider{})
	require.NoError(t, err)

	pc, ok := cfg.Providers.Get("my-llm")
	require.True(t, ok)
	_, present := pc.ExtraHeaders["X-Custom"]
	require.False(t, present, "literal empty-string header must be dropped")
	require.Equal(t, "present", pc.ExtraHeaders["X-Kept"])
}

// TestConfig_configureProviders_EchoEmptyHeaderDropped pins design
// decision #18 for the non-failing empty case: $(echo) exits 0 with
// empty output, resolves cleanly to "", and must be dropped the same
// way an unset bare $VAR is. Exercises the known-provider loop.
func TestConfig_configureProviders_EchoEmptyHeaderDropped(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			Models:      []catwalk.Model{{ID: "test-model"}},
			DefaultHeaders: map[string]string{
				"X-Empty": "$(echo)",
				"X-Kept":  "present",
			},
		},
	}

	cfg := &Config{}
	cfg.setDefaults("/tmp", "")

	testEnv := env.NewFromMap(map[string]string{
		"OPENAI_API_KEY": "test-key",
		"PATH":           os.Getenv("PATH"),
	})
	resolver := NewShellVariableResolver(testEnv)

	err := cfg.configureProviders(testStore(cfg), testEnv, resolver, knownProviders)
	require.NoError(t, err)

	pc, ok := cfg.Providers.Get("openai")
	require.True(t, ok)
	_, present := pc.ExtraHeaders["X-Empty"]
	require.False(t, present, "$(echo) → empty → header must be dropped")
	require.Equal(t, "present", pc.ExtraHeaders["X-Kept"])
}

// TestConfig_configureProviders_UnsetAPIKeySkipsProvider verifies that
// under the lenient-nounset shell resolver, $UNSET_API_KEY expands to
// ("", nil) rather than ("", err), and the existing
// `v == "" || err != nil` skip path at load.go:331 still drops the
// provider. The slog.Warn line is emitted on the same
// path but is not asserted here — internal/config/load_test.go's
// TestMain replaces the default slog handler with an io.Discard
// writer, so capturing that log line would require mid-test handler
// swapping and a sync.Mutex dance that adds more flake surface than
// signal. The observable outcome (provider absent from the map) is
// what downstream code — model picker, agent wiring — actually reads,
// so that's what we pin.
func TestConfig_configureProviders_UnsetAPIKeySkipsProvider(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$SOMETHING_UNSET",
			APIEndpoint: "https://api.openai.com/v1",
			Models:      []catwalk.Model{{ID: "test-model"}},
		},
	}

	// Existing user config for this known provider so the load.go:332
	// `if configExists` branch fires and actually calls Providers.Del.
	// Without it the provider was never in the map to begin with and
	// the test would pass trivially.
	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"openai": {BaseURL: "custom-url"},
		}),
	}
	cfg.setDefaults("/tmp", "")

	testEnv := env.NewFromMap(map[string]string{
		"PATH": os.Getenv("PATH"),
	})
	resolver := NewShellVariableResolver(testEnv)

	err := cfg.configureProviders(testStore(cfg), testEnv, resolver, knownProviders)
	require.NoError(t, err, "skip path must not surface as a load error")

	require.Equal(t, 0, cfg.Providers.Len(), "provider with unset API key must be skipped")
	_, exists := cfg.Providers.Get("openai")
	require.False(t, exists)
}

// TestConfig_configureProviders_FailingAPIKeyCmdSkipsProvider pins
// that the two failure modes for APIKey — ("", nil) from an unset var
// under lenient nounset and ("", err) from a failing $(cmd) — are
// equivalent for the skip outcome at load.go:331. The `v == "" ||
// err != nil` check fires on either branch; this test locks in that
// equivalence so a future refactor that splits the check into two
// paths doesn't accidentally start propagating $(false) as a load
// error while keeping unset-var as a silent skip (or vice versa).
func TestConfig_configureProviders_FailingAPIKeyCmdSkipsProvider(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          "openai",
			APIKey:      "$(false)",
			APIEndpoint: "https://api.openai.com/v1",
			Models:      []catwalk.Model{{ID: "test-model"}},
		},
	}

	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"openai": {BaseURL: "custom-url"},
		}),
	}
	cfg.setDefaults("/tmp", "")

	testEnv := env.NewFromMap(map[string]string{
		"PATH": os.Getenv("PATH"),
	})
	resolver := NewShellVariableResolver(testEnv)

	err := cfg.configureProviders(testStore(cfg), testEnv, resolver, knownProviders)
	require.NoError(t, err, "failing $(cmd) in API key must skip provider, not fail load")

	require.Equal(t, 0, cfg.Providers.Len(), "provider with failing $(cmd) API key must be skipped")
	_, exists := cfg.Providers.Get("openai")
	require.False(t, exists)
}

// TestConfig_configureProviders_UnsetAzureEndpointSkipsProvider pins
// the same contract on the Azure path at load.go:287 — APIEndpoint is
// the field that gates Azure and goes through the same
// `v == "" || err != nil` skip check. Covered here so both branches
// of the shared skip pattern (APIKey default path and APIEndpoint
// Azure path) are tested; a future refactor that unifies them can
// rely on these two tests to catch drift.
func TestConfig_configureProviders_UnsetAzureEndpointSkipsProvider(t *testing.T) {
	knownProviders := []catwalk.Provider{
		{
			ID:          catwalk.InferenceProviderAzure,
			APIKey:      "test-key",
			APIEndpoint: "$UNSET_AZURE_ENDPOINT",
			Models:      []catwalk.Model{{ID: "test-model"}},
		},
	}

	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"azure": {BaseURL: ""},
		}),
	}
	cfg.setDefaults("/tmp", "")

	testEnv := env.NewFromMap(map[string]string{
		"PATH": os.Getenv("PATH"),
	})
	resolver := NewShellVariableResolver(testEnv)

	err := cfg.configureProviders(testStore(cfg), testEnv, resolver, knownProviders)
	require.NoError(t, err)

	require.Equal(t, 0, cfg.Providers.Len(), "azure provider with unset endpoint must be skipped")
	_, exists := cfg.Providers.Get("azure")
	require.False(t, exists)
}

func TestContextPathsExist_OnlyClaudeMd(t *testing.T) {
	t.Run("ignores legacy agent files", func(t *testing.T) {
		dir := t.TempDir()

		require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(""), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte(""), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.local.md"), []byte(""), 0o644))

		exists, err := contextPathsExist(dir)
		require.NoError(t, err)
		require.False(t, exists)
	})

	t.Run("accepts claude md", func(t *testing.T) {
		dir := t.TempDir()

		require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(""), 0o644))

		exists, err := contextPathsExist(dir)
		require.NoError(t, err)
		require.True(t, exists)
	})
}
