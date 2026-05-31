package relay

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/session"
)

// WrapEvent is the exported entry point to [wrapEvent] so other transports
// (e.g. the NATS relay publisher) can produce the same on-the-wire envelope
// the SSE handler emits, keeping a single serialization source of truth.
func WrapEvent(ev any) *pubsub.Payload { return wrapEvent(ev) }

// eventSessionID extracts the owning session ID from a raw event payload
// so the per-session relay loop can drop foreign-session events (the app
// event bus is global). Returns "" for events that aren't tied to a
// specific session (mcp/lsp/permission notification) — those are global
// and must be forwarded by every relay.
func eventSessionID(ev any) string {
	switch e := ev.(type) {
	case pubsub.Event[message.Message]:
		return e.Payload.SessionID
	case pubsub.Event[session.Session]:
		// Prefer ParentSessionID so the root session's relay claims
		// ownership of its sub-agent session lifecycle events; the sub-
		// agent's own relay also gets it via session.ID below if it
		// happens to match.
		if e.Payload.ParentSessionID != "" {
			return e.Payload.ParentSessionID
		}
		return e.Payload.ID
	case pubsub.Event[history.File]:
		return e.Payload.SessionID
	case pubsub.Event[notify.Notification]:
		return e.Payload.SessionID
	case pubsub.Event[permission.PermissionRequest]:
		return e.Payload.SessionID
	}
	return ""
}

// shouldPersistMessage decides whether a message event is a terminal snapshot
// worth storing in JetStream for cold-open replay. Assistant messages stream
// token-by-token (one full-snapshot UpdatedEvent per debounce tick), so only
// their finished snapshot is durable; user and tool messages arrive complete;
// deletions must propagate so a cold-open does not resurrect removed messages.
func shouldPersistMessage(ev pubsub.Event[message.Message]) bool {
	if ev.Type == pubsub.DeletedEvent {
		return true
	}
	if ev.Payload.Role == message.Assistant {
		return ev.Payload.IsFinished()
	}
	return true
}

// wrapEvent converts a raw tea.Msg (a pubsub.Event[T] from the app
// event fan-in) into a pubsub.Payload envelope with the correct
// PayloadType discriminator and a proto-typed inner payload that has
// proper JSON tags. Returns nil if the event type is unrecognized.
func wrapEvent(ev any) *pubsub.Payload {
	switch e := ev.(type) {
	case pubsub.Event[app.LSPEvent]:
		return envelope(pubsub.PayloadTypeLSPEvent, pubsub.Event[proto.LSPEvent]{
			Type: e.Type,
			Payload: proto.LSPEvent{
				Type:            proto.LSPEventType(e.Payload.Type),
				Name:            e.Payload.Name,
				State:           e.Payload.State,
				Error:           e.Payload.Error,
				DiagnosticCount: e.Payload.DiagnosticCount,
			},
		})
	case pubsub.Event[mcp.Event]:
		return envelope(pubsub.PayloadTypeMCPEvent, pubsub.Event[proto.MCPEvent]{
			Type: e.Type,
			Payload: proto.MCPEvent{
				Type:      mcpEventTypeToProto(e.Payload.Type),
				Name:      e.Payload.Name,
				State:     proto.MCPState(e.Payload.State),
				Error:     e.Payload.Error,
				ToolCount: e.Payload.Counts.Tools,
			},
		})
	case pubsub.Event[permission.PermissionRequest]:
		return envelope(pubsub.PayloadTypePermissionRequest, pubsub.Event[proto.PermissionRequest]{
			Type: e.Type,
			Payload: proto.PermissionRequest{
				ID:          e.Payload.ID,
				SessionID:   e.Payload.SessionID,
				ToolCallID:  e.Payload.ToolCallID,
				ToolName:    e.Payload.ToolName,
				Description: e.Payload.Description,
				Action:      e.Payload.Action,
				Path:        e.Payload.Path,
				Params:      e.Payload.Params,
			},
		})
	case pubsub.Event[permission.PermissionNotification]:
		return envelope(pubsub.PayloadTypePermissionNotification, pubsub.Event[proto.PermissionNotification]{
			Type: e.Type,
			Payload: proto.PermissionNotification{
				ToolCallID: e.Payload.ToolCallID,
				Granted:    e.Payload.Granted,
				Denied:     e.Payload.Denied,
			},
		})
	case pubsub.Event[message.Message]:
		return envelope(pubsub.PayloadTypeMessage, pubsub.Event[proto.Message]{
			Type:    e.Type,
			Payload: messageToProto(e.Payload),
		})
	case pubsub.Event[session.Session]:
		return envelope(pubsub.PayloadTypeSession, pubsub.Event[proto.Session]{
			Type:    e.Type,
			Payload: sessionToProto(e.Payload),
		})
	case pubsub.Event[history.File]:
		return envelope(pubsub.PayloadTypeFile, pubsub.Event[proto.File]{
			Type:    e.Type,
			Payload: fileToProto(e.Payload),
		})
	case pubsub.Event[notify.Notification]:
		return envelope(pubsub.PayloadTypeAgentEvent, pubsub.Event[proto.AgentEvent]{
			Type: e.Type,
			Payload: proto.AgentEvent{
				SessionID:          e.Payload.SessionID,
				SessionTitle:       e.Payload.SessionTitle,
				Type:               proto.AgentEventType(e.Payload.Type),
				ProviderID:         e.Payload.ProviderID,
				SubAgentToolCallID: e.Payload.SubAgentToolCallID,
				SubAgentPrompt:     e.Payload.SubAgentPrompt,
				SubAgentProfile:    e.Payload.SubAgentProfile,
				SubAgentError:      e.Payload.SubAgentError,
			},
		})
	case pubsub.Event[agentruntime.TaskTrace]:
		// Task trace events are high-frequency, host-local diagnostics (the
		// trace JSONL file + the TUI DAG panel). They are intentionally not
		// mirrored to the phone — the phone already gets task/sub-agent
		// lifecycle via AgentEvent (notify.Notification) above. Skip silently
		// instead of falling to the default WARN, which otherwise spams the
		// log hundreds of times per session.
		return nil
	default:
		slog.Warn("Unrecognized event type for SSE wrapping", "type", fmt.Sprintf("%T", ev))
		return nil
	}
}

// envelope marshals the inner event and wraps it in a pubsub.Payload.
func envelope(payloadType pubsub.PayloadType, inner any) *pubsub.Payload {
	raw, err := json.Marshal(inner)
	if err != nil {
		slog.Error("Failed to marshal event payload", "error", err)
		return nil
	}
	return &pubsub.Payload{
		Type:    payloadType,
		Payload: raw,
	}
}

func mcpEventTypeToProto(t mcp.EventType) proto.MCPEventType {
	switch t {
	case mcp.EventStateChanged:
		return proto.MCPEventStateChanged
	case mcp.EventToolsListChanged:
		return proto.MCPEventToolsListChanged
	case mcp.EventPromptsListChanged:
		return proto.MCPEventPromptsListChanged
	case mcp.EventResourcesListChanged:
		return proto.MCPEventResourcesListChanged
	default:
		return proto.MCPEventStateChanged
	}
}

func sessionToProto(s session.Session) proto.Session {
	return proto.Session{
		ID:                        s.ID,
		ParentSessionID:           s.ParentSessionID,
		Title:                     s.Title,
		Mode:                      string(s.Mode),
		SummaryMessageID:          s.SummaryMessageID,
		MessageCount:              s.MessageCount,
		PromptTokens:              s.PromptTokens,
		CompletionTokens:          s.CompletionTokens,
		LastPromptTokens:          s.LastPromptTokens,
		LastCompletionTokens:      s.LastCompletionTokens,
		LastCacheCreationTokens:   s.LastCacheCreationTokens,
		LastCacheReadTokens:       s.LastCacheReadTokens,
		LastContextPressureTokens: s.LastContextPressureTokens,
		Cost:                      s.Cost,
		Todos:                     todosToProto(s.Todos),
		CreatedAt:                 s.CreatedAt,
		UpdatedAt:                 s.UpdatedAt,
	}
}

func todosToProto(todos []session.Todo) []proto.Todo {
	if len(todos) == 0 {
		return nil
	}
	out := make([]proto.Todo, len(todos))
	for i, t := range todos {
		out[i] = proto.Todo{
			Content:    t.Content,
			Status:     string(t.Status),
			ActiveForm: t.ActiveForm,
		}
	}
	return out
}

func fileToProto(f history.File) proto.File {
	return proto.File{
		ID:        f.ID,
		SessionID: f.SessionID,
		Path:      f.Path,
		Content:   f.Content,
		Version:   f.Version,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
	}
}

func messageToProto(m message.Message) proto.Message {
	msg := proto.Message{
		ID:        m.ID,
		SessionID: m.SessionID,
		Role:      proto.MessageRole(m.Role),
		Model:     m.Model,
		Provider:  m.Provider,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}

	for _, p := range m.Parts {
		switch v := p.(type) {
		case message.TextContent:
			msg.Parts = append(msg.Parts, proto.TextContent{Text: v.Text})
		case message.ReasoningContent:
			msg.Parts = append(msg.Parts, proto.ReasoningContent{
				Thinking:   v.Thinking,
				Signature:  v.Signature,
				StartedAt:  v.StartedAt,
				FinishedAt: v.FinishedAt,
			})
		case message.ToolCall:
			msg.Parts = append(msg.Parts, proto.ToolCall{
				ID:       v.ID,
				Name:     v.Name,
				Input:    v.Input,
				Finished: v.Finished,
			})
		case message.ToolResult:
			content := v.Content
			const maxContent = 8192 // 8KB snippet is plenty for mobile preview
			if len(content) > maxContent {
				content = content[:maxContent] + "\n\n... (output truncated for mobile) ..."
			}
			msg.Parts = append(msg.Parts, proto.ToolResult{
				ToolCallID: v.ToolCallID,
				Name:       v.Name,
				Content:    content,
				Data:       v.Data, // Data is usually small or nil for text tools
				MIMEType:   v.MIMEType,
				Metadata:   v.Metadata,
				IsError:    v.IsError,
			})
		case message.Finish:
			msg.Parts = append(msg.Parts, proto.Finish{
				Reason:  proto.FinishReason(v.Reason),
				Time:    v.Time,
				Message: v.Message,
				Details: v.Details,
			})
		case message.ImageURLContent:
			msg.Parts = append(msg.Parts, proto.ImageURLContent{URL: v.URL, Detail: v.Detail})
		case message.BinaryContent:
			msg.Parts = append(msg.Parts, proto.BinaryContent{Path: v.Path, MIMEType: v.MIMEType, Data: v.Data})
		}
	}

	return msg
}

func messagesToProto(msgs []message.Message) []proto.Message {
	out := make([]proto.Message, len(msgs))
	for i, m := range msgs {
		out[i] = messageToProto(m)
	}
	return out
}
