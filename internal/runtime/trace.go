package runtime

import "time"

// TraceKind classifies a runtime trace entry.
type TraceKind string

const (
	TraceKindTaskPlanned                    TraceKind = "task_planned"
	TraceKindTaskStarted                    TraceKind = "task_started"
	TraceKindTaskProgress                   TraceKind = "task_progress"
	TraceKindTaskFinished                   TraceKind = "task_finished"
	TraceKindTaskFailed                     TraceKind = "task_failed"
	TraceKindTaskInput                      TraceKind = "task_input"
	TraceKindTaskOutput                     TraceKind = "task_output"
	TraceKindLLMStarted                     TraceKind = "llm_request_started"
	TraceKindLLMFirstEvent                  TraceKind = "llm_first_event"
	TraceKindLLMFirstText                   TraceKind = "llm_first_text_delta"
	TraceKindLLMRetry                       TraceKind = "llm_request_retry"
	TraceKindLLMFinished                    TraceKind = "llm_request_finished"
	TraceKindLLMFailed                      TraceKind = "llm_request_failed"
	TraceKindToolStarted                    TraceKind = "tool_started"
	TraceKindToolFinished                   TraceKind = "tool_finished"
	TraceKindToolFailed                     TraceKind = "tool_failed"
	TraceKindCommandStart                   TraceKind = "command_started"
	TraceKindCommandDone                    TraceKind = "command_finished"
	TraceKindCommandFail                    TraceKind = "command_failed"
	TraceKindCommandSkip                    TraceKind = "command_skipped"
	TraceKindConversationCompactionStarted  TraceKind = "conversation_compaction_started"
	TraceKindConversationCompactionProgress TraceKind = "conversation_compaction_progress"
	TraceKindConversationCompactionFinished TraceKind = "conversation_compaction_finished"
	TraceKindConversationCompactionFailed   TraceKind = "conversation_compaction_failed"
)

// TaskTrace is an append-only record of task planning, execution, and
// communication within a runtime session.
type TaskTrace struct {
	Sequence                      int64
	RecordedAt                    time.Time
	StartedAt                     time.Time
	FinishedAt                    time.Time
	DurationMs                    int64
	ConversationSessionID         string
	SessionID                     string
	NodeID                        string
	ParentID                      string
	Depth                         int
	Profile                       string
	ProviderID                    string
	ProviderType                  string
	ModelID                       string
	RequestID                     string
	HTTPTraceID                   string
	Kind                          TraceKind
	Status                        string
	Success                       bool
	Goal                          string
	Scope                         []string
	Attempt                       int
	StepNumber                    int
	Input                         string
	Output                        string
	Error                         string
	InputBytes                    int
	OutputBytes                   int
	InputTokens                   int64
	OutputTokens                  int64
	TotalTokens                   int64
	ReasoningTokens               int64
	CacheCreationTokens           int64
	CacheReadTokens               int64
	EstimatedCostUSD              float64
	ContextMessageCount           int
	ContextBytes                  int
	PreflightEstimatedInputTokens int64
	ContextWindowTokens           int64
	AutoSummarizeThresholdRatio   float64
	AutoSummarizeThresholdTokens  int64
	AutoSummarizeUsedTokens       int64
	AutoSummarizeTriggered        bool
	AttachmentCount               int
	FileCount                     int
	ToolCount                     int
	ToolSchemaBytes               int
	MaxOutputTokens               int64
	FirstEventType                string
	FirstEventLatencyMs           int64
	FirstTextLatencyMs            int64
	RetryDelayMs                  int64
	FinishReason                  string
	ToolName                      string
	ToolCallID                    string
	ToolInput                     string
	ToolOutput                    string
	ToolInputBytes                int
	ToolOutputBytes               int
	ToolIsError                   bool
	ToolStopTurn                  bool
	CommandID                     string
	Command                       string
	WorkingDir                    string
	ExitCode                      *int
	Outcome                       string
	StdoutBytes                   int
	StderrBytes                   int
	ShellID                       string
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
