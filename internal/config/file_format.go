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

func isLLMConfigKey(key string) bool {
	switch {
	case strings.HasPrefix(key, "providers."):
		return true
	case strings.HasPrefix(key, "models."):
		return true
	case strings.HasPrefix(key, "agents."):
		return true
	case key == "default_agent":
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

func llmConfigCandidates(basePath string) []string {
	dir := filepath.Dir(basePath)
	stem := strings.TrimSuffix(filepath.Base(basePath), filepath.Ext(basePath))
	return []string{
		filepath.Join(dir, stem+".llm.yaml"),
		filepath.Join(dir, stem+".llm.yml"),
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
