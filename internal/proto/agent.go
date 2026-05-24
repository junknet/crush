package proto

import (
	"encoding/json"
	"errors"
)

// AgentEventType represents the type of agent event.
type AgentEventType string

const (
	AgentEventTypeError            AgentEventType = "error"
	AgentEventTypeResponse         AgentEventType = "response"
	AgentEventTypeSummarize        AgentEventType = "summarize"
	AgentEventTypeAgentStarted     AgentEventType = "agent_started"
	AgentEventTypeAgentFinished    AgentEventType = "agent_finished"
	AgentEventTypeSubAgentStarted  AgentEventType = "sub_agent_started"
	AgentEventTypeSubAgentFinished AgentEventType = "sub_agent_finished"
	AgentEventTypeSubAgentFailed   AgentEventType = "sub_agent_failed"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (t AgentEventType) MarshalText() ([]byte, error) {
	return []byte(t), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (t *AgentEventType) UnmarshalText(text []byte) error {
	*t = AgentEventType(text)
	return nil
}

// AgentEvent represents an event emitted by the agent.
type AgentEvent struct {
	Type    AgentEventType `json:"type"`
	Message Message        `json:"message"`
	Error   error          `json:"error,omitempty"`

	// Session and provider fields identify the active run that emitted
	// this event. Sub-agent events use the created child session id.
	SessionID    string `json:"session_id,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
	ProviderID   string `json:"provider_id,omitempty"`
	Progress     string `json:"progress,omitempty"`
	Done         bool   `json:"done,omitempty"`

	// Sub-agent fields are populated for sub_agent_* events so web and TUI
	// clients can render a traceable live activity row.
	SubAgentToolCallID string `json:"sub_agent_tool_call_id,omitempty"`
	SubAgentPrompt     string `json:"sub_agent_prompt,omitempty"`
	SubAgentProfile    string `json:"sub_agent_profile,omitempty"`
	SubAgentError      string `json:"sub_agent_error,omitempty"`
}

// MarshalJSON implements the [json.Marshaler] interface.
func (e AgentEvent) MarshalJSON() ([]byte, error) {
	type Alias AgentEvent
	return json.Marshal(&struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Error: func() string {
			if e.Error != nil {
				return e.Error.Error()
			}
			return ""
		}(),
		Alias: Alias(e),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (e *AgentEvent) UnmarshalJSON(data []byte) error {
	type Alias AgentEvent
	aux := &struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Alias: Alias(*e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*e = AgentEvent(aux.Alias)
	if aux.Error != "" {
		e.Error = errors.New(aux.Error)
	}
	return nil
}
