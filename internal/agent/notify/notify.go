// Package notify defines domain notification types for agent events.
// These types are decoupled from UI concerns so the agent can publish
// events without importing UI packages.
package notify

// Type identifies the kind of agent notification.
type Type string

const (
	// TypeAgentFinished indicates the agent has completed its turn.
	TypeAgentFinished Type = "agent_finished"
	// TypeReAuthenticate indicates the agent encountered an
	// authentication error and the user needs to re-authenticate.
	TypeReAuthenticate Type = "re_authenticate"

	// TypeSubAgentStarted indicates a sub-agent has been dispatched.
	// SubAgentToolCallID and SubAgentPrompt are populated; SessionID is
	// the sub-session created for the delegation.
	TypeSubAgentStarted Type = "sub_agent_started"

	// TypeSubAgentFinished indicates a sub-agent completed successfully.
	TypeSubAgentFinished Type = "sub_agent_finished"

	// TypeSubAgentFailed indicates a sub-agent terminated with an error.
	// SubAgentError carries the error text.
	TypeSubAgentFailed Type = "sub_agent_failed"
)

// Notification represents a domain event published by the agent.
type Notification struct {
	SessionID    string
	SessionTitle string
	Type         Type
	ProviderID   string

	// Sub-agent fields. Populated only for TypeSubAgent* events.
	// SubAgentToolCallID is the parent's tool-call id that triggered the
	// delegation — stable across Started → Finished/Failed so the UI can
	// match events to a single row.
	SubAgentToolCallID string
	SubAgentPrompt     string
	SubAgentError      string
}
