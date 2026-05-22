package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type configFileFormat int

const (
	configFileFormatJSON configFileFormat = iota
	configFileFormatYAML
)

func configFileFormatForPath(path string) configFileFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return configFileFormatYAML
	default:
		return configFileFormatJSON
	}
}

func readConfigFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []byte("{}"), nil
	}

	switch configFileFormatForPath(path) {
	case configFileFormatJSON:
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid JSON in config file %s", path)
		}
		return data, nil
	case configFileFormatYAML:
		var parsed any
		if err := yaml.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("invalid YAML in config file %s: %w", path, err)
		}
		if parsed == nil {
			return []byte("{}"), nil
		}
		jsonBytes, err := json.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("failed to convert YAML config file %s: %w", path, err)
		}
		return jsonBytes, nil
	default:
		return nil, fmt.Errorf("unsupported config file format for %s", path)
	}
}

func writeConfigFile(path string, data []byte) error {
	switch configFileFormatForPath(path) {
	case configFileFormatJSON:
		return atomicWriteFile(path, data, 0o600)
	case configFileFormatYAML:
		var parsed any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return fmt.Errorf("failed to convert config file %s to YAML: %w", path, err)
		}
		yamlBytes, err := yaml.Marshal(parsed)
		if err != nil {
			return fmt.Errorf("failed to marshal YAML config file %s: %w", path, err)
		}
		return atomicWriteFile(path, yamlBytes, 0o600)
	default:
		return fmt.Errorf("unsupported config file format for %s", path)
	}
}

// isStateKey reports whether a config key is runtime *state* the app writes on
// its own — current model selection, recent models, and oauth/api tokens it
// refreshes. These persist to state.yaml so they never collide with the
// hand-authored declarative config in crush.yaml (providers, agents, mcp,
// options). Everything else is declarative and lives in crush.yaml.
func isStateKey(key string) bool {
	switch {
	case strings.HasPrefix(key, "models."):
		return true
	case strings.HasPrefix(key, "recent_models."):
		return true
	case strings.HasPrefix(key, "providers.") &&
		(strings.HasSuffix(key, ".oauth") || strings.HasSuffix(key, ".api_key")):
		return true
	default:
		return false
	}
}

func configCandidates(basePath string) []string {
	dir := filepath.Dir(basePath)
	stem := strings.TrimSuffix(filepath.Base(basePath), filepath.Ext(basePath))
	return []string{
		filepath.Join(dir, stem+".json"),
		filepath.Join(dir, stem+".yaml"),
		filepath.Join(dir, stem+".yml"),
	}
}

// stateConfigCandidates returns the runtime-state file candidates that sit
// next to the declarative config: always state.{yaml,yml} in the same dir.
func stateConfigCandidates(basePath string) []string {
	dir := filepath.Dir(basePath)
	return []string{
		filepath.Join(dir, "state.yaml"),
		filepath.Join(dir, "state.yml"),
	}
}

func resolveFirstExistingPath(candidates []string) string {
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}
