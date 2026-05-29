package provider

import (
	"errors"
	"strings"

	"charm.land/fantasy"
)

// RequestPurpose describes the high-level goal of a request.
type RequestPurpose string

const (
	PurposePlan      RequestPurpose = "plan"
	PurposeEdit      RequestPurpose = "edit"
	PurposeInspect   RequestPurpose = "inspect"
	PurposeVerify    RequestPurpose = "verify"
	PurposeSummarize RequestPurpose = "summarize"
)

// ThinkingBudget is a provider-agnostic reasoning budget.
type ThinkingBudget int

const (
	BudgetNone ThinkingBudget = iota
	BudgetLow
	BudgetMedium
	BudgetHigh
	BudgetXHigh
)

// BudgetFromTokens converts a token budget into a reasoning budget tier.
func BudgetFromTokens(tokens int) ThinkingBudget {
	switch {
	case tokens <= 0:
		return BudgetNone
	case tokens <= 1024:
		return BudgetLow
	case tokens <= 4096:
		return BudgetMedium
	case tokens <= 12000:
		return BudgetHigh
	default:
		return BudgetXHigh
	}
}

// Tokens returns the approximate token count for the budget tier.
func (b ThinkingBudget) Tokens() int {
	switch b {
	case BudgetNone:
		return 0
	case BudgetLow:
		return 1024
	case BudgetMedium:
		return 4096
	case BudgetHigh:
		return 12000
	case BudgetXHigh:
		return 32000
	default:
		return 0
	}
}

// ReasoningEffort converts the budget tier to a provider effort string.
func (b ThinkingBudget) ReasoningEffort() string {
	switch b {
	case BudgetNone:
		return "minimal"
	case BudgetLow:
		return "low"
	case BudgetMedium:
		return "medium"
	case BudgetHigh:
		return "high"
	case BudgetXHigh:
		return "xhigh"
	default:
		return "minimal"
	}
}

// AttentionPolicy controls whether a request should stay in the main session
// or be compacted out after completion.
type AttentionPolicy string

const (
	AttentionPolicyInherit            AttentionPolicy = "inherit"
	AttentionPolicyEvictAfterTask     AttentionPolicy = "evict_after_task"
	AttentionPolicyKeepSessionCompact AttentionPolicy = "keep_session_compact"
)

// ToolMode controls how freely tools can be used.
type ToolMode string

const (
	ToolModeAuto ToolMode = "auto"
	ToolModeNone ToolMode = "none"
	ToolModeAny  ToolMode = "any"
)

// RequestIntent is the provider-facing request contract.
type RequestIntent struct {
	Purpose         RequestPurpose
	ThinkingBudget  ThinkingBudget
	AttentionPolicy AttentionPolicy
	MaxOutputTokens int
	ToolMode        ToolMode
	Tools           []string
}

// IsDefinitiveFailure returns true when the error is considered non-retryable
// (e.g. invalid arguments, billing issues, access denied). Transient errors
// like rate limits or 5xx server errors return false.
func IsDefinitiveFailure(err error, provider string) bool {
	if err == nil {
		return false
	}
	var providerErr *fantasy.ProviderError
	if errors.As(err, &providerErr) {
		code := providerErr.StatusCode
		// Definitive: auth/billing/permissions/invalid-args.
		// Transient (not in list): 429, 502, 503, 504.
		if code == 401 || code == 402 || code == 403 || code == 400 || code == 422 {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "insufficient balance") ||
		strings.Contains(msg, "credit limit") ||
		strings.Contains(msg, "billing") ||
		strings.Contains(msg, "quota exceeded") {
		return true
	}
	return false
}
