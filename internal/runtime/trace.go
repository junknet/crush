package runtime

import "time"

// TraceKind classifies a runtime trace entry.
type TraceKind string

const (
	TraceKindTaskPlanned  TraceKind = "task_planned"
	TraceKindTaskStarted  TraceKind = "task_started"
	TraceKindTaskProgress TraceKind = "task_progress"
	TraceKindTaskFinished TraceKind = "task_finished"
	TraceKindTaskFailed   TraceKind = "task_failed"
	TraceKindTaskInput    TraceKind = "task_input"
	TraceKindTaskOutput   TraceKind = "task_output"
	TraceKindToolStarted  TraceKind = "tool_started"
	TraceKindToolFinished TraceKind = "tool_finished"
	TraceKindToolFailed   TraceKind = "tool_failed"
	TraceKindCommandStart TraceKind = "command_started"
	TraceKindCommandDone  TraceKind = "command_finished"
	TraceKindCommandFail  TraceKind = "command_failed"
	TraceKindCommandSkip  TraceKind = "command_skipped"
)

// TaskTrace is an append-only record of task planning, execution, and
// communication within a runtime session.
type TaskTrace struct {
	Sequence              int64
	RecordedAt            time.Time
	StartedAt             time.Time
	FinishedAt            time.Time
	DurationMs            int64
	ConversationSessionID string
	SessionID             string
	NodeID                string
	ParentID              string
	Depth                 int
	Profile               string
	ProviderID            string
	ProviderType          string
	ModelID               string
	RequestID             string
	Kind                  TraceKind
	Status                string
	Success               bool
	Goal                  string
	Scope                 []string
	Input                 string
	Output                string
	Error                 string
	InputBytes            int
	OutputBytes           int
	InputTokens           int64
	OutputTokens          int64
	TotalTokens           int64
	ReasoningTokens       int64
	CacheCreationTokens   int64
	CacheReadTokens       int64
	EstimatedCostUSD      float64
	ToolName              string
	ToolCallID            string
	ToolInput             string
	ToolOutput            string
	CommandID             string
	Command               string
	WorkingDir            string
	ExitCode              *int
	Outcome               string
	StdoutBytes           int
	StderrBytes           int
	ShellID               string
}

// TraceKey returns a stable identifier for the trace entry.
func (t TaskTrace) TraceKey() string {
	sessionID := t.ConversationSessionID
	if sessionID == "" {
		sessionID = t.SessionID
	}
	if sessionID == "" {
		return ""
	}
	return sessionID + ":" + t.NodeID + ":" + t.Kind.String()
}

// String returns the string representation of the trace kind.
func (k TraceKind) String() string {
	return string(k)
}
