package config

import "strings"

const (
	geminiFamilyContextWindowTokens = 1_048_576
	claudeFamilyContextWindowTokens = 200_000
)

// ResolveModelContextWindow returns explicit provider metadata first, then a
// conservative model-family fallback for providers that omit context-window
// metadata from their local model list.
func ResolveModelContextWindow(explicit int64, providerID, providerType, modelID string) int64 {
	if explicit > 0 {
		return explicit
	}

	providerID = strings.ToLower(strings.TrimSpace(providerID))
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	modelID = strings.ToLower(strings.TrimSpace(modelID))

	if strings.Contains(providerID, "antigravity") ||
		strings.Contains(providerType, "antigravity") ||
		strings.Contains(providerType, "gemini") ||
		strings.Contains(providerType, "vertex") ||
		strings.Contains(providerID, "gemini") ||
		strings.HasPrefix(modelID, "gemini-") ||
		strings.Contains(modelID, "/gemini-") {
		return geminiFamilyContextWindowTokens
	}

	if strings.Contains(providerType, "anthropic") ||
		strings.Contains(providerID, "anthropic") ||
		strings.Contains(modelID, "claude") {
		return claudeFamilyContextWindowTokens
	}

	return 0
}
