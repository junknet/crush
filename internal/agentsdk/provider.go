package agentsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"

	"charm.land/fantasy"
	agentsdkauth "github.com/agent-sdk/auth"
	agentsdkcodex "github.com/agent-sdk/provider/codex"
	agentsdkschema "github.com/agent-sdk/schema"
)

const codexProviderName = "openai"

type CodexProvider struct {
	baseURL string
	cred    agentsdkauth.Credential
}

func NewCodexProvider(baseURL, apiKey string) (*CodexProvider, error) {
	cred := codexCredential(apiKey)
	if cred == nil {
		return nil, fmt.Errorf("openai provider requires OPENAI_API_KEY/CODEX_API_KEY or ~/.codex/auth.json")
	}

	return &CodexProvider{
		baseURL: baseURL,
		cred:    cred,
	}, nil
}

func (p *CodexProvider) Name() string {
	return codexProviderName
}

func (p *CodexProvider) LanguageModel(_ context.Context, modelID string) (fantasy.LanguageModel, error) {
	return &CodexLanguageModel{
		modelID: modelID,
		baseURL: p.baseURL,
		cred:    p.cred,
	}, nil
}

type CodexLanguageModel struct {
	modelID string
	baseURL string
	cred    agentsdkauth.Credential
}

func (m *CodexLanguageModel) Provider() string {
	return codexProviderName
}

func (m *CodexLanguageModel) Model() string {
	return m.modelID
}

func (m *CodexLanguageModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	resp, err := agentsdkcodex.New(m.cred, m.baseURL).CreateMessage(ctx, m.toChatRequest(call))
	if err != nil {
		return nil, err
	}

	return toFantasyResponse(resp), nil
}

func (m *CodexLanguageModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	resp, err := agentsdkcodex.New(m.cred, m.baseURL).CreateMessage(ctx, m.toChatRequest(call))
	if err != nil {
		return nil, err
	}

	return func(yield func(fantasy.StreamPart) bool) {
		text := textFromSDK(resp.Message.Content)
		if text != "" {
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: resp.ID}) {
				return
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: resp.ID, Delta: text}) {
				return
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: resp.ID}) {
				return
			}
		}

		for _, call := range resp.Message.ToolCalls {
			if !yield(fantasy.StreamPart{
				Type:          fantasy.StreamPartTypeToolCall,
				ID:            call.ID,
				ToolCallName:  call.Name,
				ToolCallInput: call.Arguments,
			}) {
				return
			}
		}

		yield(fantasy.StreamPart{
			Type:         fantasy.StreamPartTypeFinish,
			ID:           resp.ID,
			Usage:        toFantasyUsage(resp.Usage),
			FinishReason: fantasy.FinishReasonStop,
		})
	}, nil
}

func (m *CodexLanguageModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("agent-all-sdk codex adapter does not support object generation yet")
}

func (m *CodexLanguageModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return iter.Seq[fantasy.ObjectStreamPart](func(func(fantasy.ObjectStreamPart) bool) {}), fmt.Errorf("agent-all-sdk codex adapter does not support object streaming yet")
}

func (m *CodexLanguageModel) toChatRequest(call fantasy.Call) *agentsdkschema.ChatRequest {
	req := &agentsdkschema.ChatRequest{
		Model:       m.modelID,
		Messages:    toSDKMessages(call.Prompt),
		Tools:       toSDKTools(call.Tools),
		Temperature: call.Temperature,
	}
	if call.MaxOutputTokens != nil {
		req.MaxTokens = int(*call.MaxOutputTokens)
	}
	return req
}

func codexCredential(apiKey string) agentsdkauth.Credential {
	if apiKey != "" {
		return &agentsdkauth.APIKeyCredential{Key: apiKey}
	}

	for _, credit := range agentsdkauth.DetectLocalCredits(context.Background()) {
		if credit.Provider != "codex" {
			continue
		}
		if credit.Type == "oauth" {
			return &agentsdkauth.OAuthCredential{
				AccessToken: credit.Value,
				Provider:    credit.Provider,
				AccountID:   credit.AccountID,
			}
		}
		return &agentsdkauth.APIKeyCredential{Key: credit.Value}
	}

	return nil
}

func toSDKMessages(prompt fantasy.Prompt) []agentsdkschema.Message {
	messages := make([]agentsdkschema.Message, 0, len(prompt))
	for _, msg := range prompt {
		messages = append(messages, agentsdkschema.Message{
			Role:      toSDKRole(msg.Role),
			Content:   toSDKContent(msg.Content),
			ToolCalls: toSDKToolCalls(msg.Content),
		})
	}
	return messages
}

func toSDKRole(role fantasy.MessageRole) agentsdkschema.Role {
	switch role {
	case fantasy.MessageRoleSystem:
		return agentsdkschema.RoleSystem
	case fantasy.MessageRoleAssistant:
		return agentsdkschema.RoleAssistant
	case fantasy.MessageRoleTool:
		return agentsdkschema.RoleTool
	default:
		return agentsdkschema.RoleUser
	}
}

func toSDKContent(parts []fantasy.MessagePart) []agentsdkschema.ContentPart {
	content := make([]agentsdkschema.ContentPart, 0, len(parts))
	for _, part := range parts {
		switch p := part.(type) {
		case fantasy.TextPart:
			content = append(content, agentsdkschema.ContentPart{Kind: agentsdkschema.ContentKindText, Text: p.Text})
		case fantasy.FilePart:
			content = append(content, agentsdkschema.ContentPart{
				Kind: agentsdkschema.ContentKindImage,
				Image: &agentsdkschema.Image{
					Data:     string(p.Data),
					MimeType: p.MediaType,
				},
			})
		case fantasy.ToolResultPart:
			content = append(content, agentsdkschema.ContentPart{Kind: agentsdkschema.ContentKindText, Text: toolResultText(p.Output)})
		}
	}
	return content
}

func toSDKToolCalls(parts []fantasy.MessagePart) []agentsdkschema.ToolCall {
	calls := make([]agentsdkschema.ToolCall, 0)
	for _, part := range parts {
		switch p := part.(type) {
		case fantasy.ToolCallPart:
			calls = append(calls, agentsdkschema.ToolCall{ID: p.ToolCallID, Name: p.ToolName, Arguments: p.Input})
		case fantasy.ToolResultPart:
			calls = append(calls, agentsdkschema.ToolCall{ID: p.ToolCallID, Arguments: toolResultText(p.Output)})
		}
	}
	return calls
}

func toSDKTools(tools []fantasy.Tool) []agentsdkschema.ToolDefinition {
	out := make([]agentsdkschema.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		functionTool, ok := tool.(fantasy.FunctionTool)
		if !ok {
			continue
		}
		out = append(out, agentsdkschema.ToolDefinition{
			Name:        functionTool.Name,
			Description: functionTool.Description,
			Parameters:  functionTool.InputSchema,
		})
	}
	return out
}

func toFantasyResponse(resp *agentsdkschema.ChatResponse) *fantasy.Response {
	content := make(fantasy.ResponseContent, 0, len(resp.Message.Content)+len(resp.Message.ToolCalls))
	for _, part := range resp.Message.Content {
		switch part.Kind {
		case agentsdkschema.ContentKindThinking:
			if part.Thinking != nil {
				content = append(content, fantasy.ReasoningContent{Text: part.Thinking.Content})
			}
		case agentsdkschema.ContentKindText:
			content = append(content, fantasy.TextContent{Text: part.Text})
		}
	}
	for _, call := range resp.Message.ToolCalls {
		content = append(content, fantasy.ToolCallContent{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Input:      call.Arguments,
		})
	}

	return &fantasy.Response{
		Content:      content,
		FinishReason: fantasy.FinishReasonStop,
		Usage:        toFantasyUsage(resp.Usage),
	}
}

func toFantasyUsage(usage agentsdkschema.Usage) fantasy.Usage {
	input := int64(usage.InputTokens)
	output := int64(usage.OutputTokens)
	return fantasy.Usage{
		InputTokens:     input,
		OutputTokens:    output,
		TotalTokens:     input + output,
		ReasoningTokens: int64(usage.ReasoningTokens),
		CacheReadTokens: int64(usage.CachedTokens),
	}
}

func textFromSDK(parts []agentsdkschema.ContentPart) string {
	var text string
	for _, part := range parts {
		if part.Kind == agentsdkschema.ContentKindText {
			text += part.Text
		}
	}
	return text
}

func toolResultText(output fantasy.ToolResultOutputContent) string {
	switch o := output.(type) {
	case fantasy.ToolResultOutputContentText:
		return o.Text
	case fantasy.ToolResultOutputContentError:
		return o.Error.Error()
	case fantasy.ToolResultOutputContentMedia:
		if o.Text != "" {
			return o.Text
		}
		b, _ := json.Marshal(o)
		return string(b)
	default:
		b, _ := json.Marshal(o)
		return string(b)
	}
}
