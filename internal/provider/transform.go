package provider

import (
	"fmt"
	"strings"
)

// Protocol identifies the wire protocol that should be emitted.
type Protocol string

const (
	ProtocolAnthropic       Protocol = "anthropic"
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
)

// Adapter describes the model endpoint and its protocol capabilities.
type Adapter struct {
	ID                  string
	Protocol            Protocol
	OutputLimit         int
	AllowedEfforts      []ThinkingBudget
	CacheControlEnabled bool
}

// BuildRequest converts the provider-agnostic intent into a wire-level payload.
func BuildRequest(adapter Adapter, intent RequestIntent, systemPrompt string, messages []map[string]any, tools []map[string]any) (map[string]any, error) {
	if adapter.ID == "" {
		return nil, fmt.Errorf("adapter id is required")
	}
	switch adapter.Protocol {
	case ProtocolAnthropic:
		return buildAnthropicRequest(adapter, intent, systemPrompt, messages, tools), nil
	case ProtocolOpenAIChat:
		return buildOpenAIChatRequest(adapter, intent, systemPrompt, messages, tools), nil
	case ProtocolOpenAIResponses:
		return buildOpenAIResponsesRequest(adapter, intent, systemPrompt, messages, tools), nil
	default:
		return nil, fmt.Errorf("unsupported adapter protocol %q", adapter.Protocol)
	}
}

func buildAnthropicRequest(adapter Adapter, intent RequestIntent, systemPrompt string, messages []map[string]any, tools []map[string]any) map[string]any {
	request := map[string]any{
		"model":    adapter.ID,
		"messages": filterEmptyUserMessages(messages),
	}
	if intent.MaxOutputTokens > 0 {
		request["max_tokens"] = intent.MaxOutputTokens
	} else if adapter.OutputLimit > 0 {
		request["max_tokens"] = adapter.OutputLimit
	} else {
		request["max_tokens"] = max(intent.ThinkingBudget.Tokens()+1024, 1024)
	}
	if intent.ThinkingBudget.Tokens() > 0 {
		request["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": intent.ThinkingBudget.Tokens(),
		}
	}
	if systemPrompt != "" {
		request["system"] = []map[string]any{{"type": "text", "text": systemPrompt}}
	}
	if len(tools) > 0 {
		request["tools"] = tools
	}
	switch intent.ToolMode {
	case ToolModeAny:
		request["tool_choice"] = map[string]any{"type": "any"}
	case ToolModeAuto:
		request["tool_choice"] = map[string]any{"type": "auto"}
	}
	if intent.ThinkingBudget.Tokens() >= 21000 {
		request["stream"] = true
	}
	return request
}

func buildOpenAIChatRequest(adapter Adapter, intent RequestIntent, systemPrompt string, messages []map[string]any, tools []map[string]any) map[string]any {
	request := map[string]any{
		"model":          adapter.ID,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if intent.MaxOutputTokens > 0 {
		request["max_tokens"] = intent.MaxOutputTokens
	} else if adapter.OutputLimit > 0 {
		request["max_tokens"] = adapter.OutputLimit
	}
	if effort := intent.ThinkingBudget.ReasoningEffort(); effort != "" && effort != "minimal" {
		request["reasoning_effort"] = effort
	}
	request["messages"] = filterEmptyUserMessages(prependSystemMessage(systemPrompt, messages))
	if len(tools) > 0 {
		request["tools"] = tools
	}
	switch intent.ToolMode {
	case ToolModeAny:
		request["tool_choice"] = "required"
	case ToolModeAuto:
		request["tool_choice"] = "auto"
	}
	return request
}

func buildOpenAIResponsesRequest(adapter Adapter, intent RequestIntent, systemPrompt string, messages []map[string]any, tools []map[string]any) map[string]any {
	request := map[string]any{
		"model":  adapter.ID,
		"input":  responsesInput(messages),
		"stream": true,
	}
	if intent.MaxOutputTokens > 0 {
		request["max_output_tokens"] = intent.MaxOutputTokens
	} else if adapter.OutputLimit > 0 {
		request["max_output_tokens"] = adapter.OutputLimit
	}
	if effort := intent.ThinkingBudget.ReasoningEffort(); effort != "" && effort != "minimal" {
		request["reasoning"] = map[string]any{"effort": effort}
	}
	if systemPrompt != "" {
		request["instructions"] = systemPrompt
	}
	if len(tools) > 0 {
		request["tools"] = tools
	}
	switch intent.ToolMode {
	case ToolModeAny:
		request["tool_choice"] = map[string]any{"type": "required"}
	case ToolModeAuto:
		request["tool_choice"] = map[string]any{"type": "auto"}
	}
	return request
}

func prependSystemMessage(systemPrompt string, messages []map[string]any) []map[string]any {
	if systemPrompt == "" {
		return append([]map[string]any(nil), messages...)
	}
	result := make([]map[string]any, 0, len(messages)+1)
	result = append(result, map[string]any{
		"role":    "system",
		"content": systemPrompt,
	})
	result = append(result, messages...)
	return result
}

func responsesInput(messages []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		content := msg["content"]
		switch v := content.(type) {
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			result = append(result, map[string]any{
				"role":    role,
				"content": []map[string]any{{"type": "input_text", "text": v}},
			})
		case []map[string]any:
			if len(v) == 0 {
				continue
			}
			result = append(result, map[string]any{"role": role, "content": v})
		default:
			if content != nil {
				result = append(result, msg)
			}
		}
	}
	return result
}

func filterEmptyUserMessages(messages []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role != "user" {
			result = append(result, msg)
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			if strings.TrimSpace(content) == "" {
				continue
			}
		case []map[string]any:
			if len(content) == 0 {
				continue
			}
		}
		result = append(result, msg)
	}
	return result
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
