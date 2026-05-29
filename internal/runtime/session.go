package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/pubsub"
)

var traceBroker = pubsub.NewBroker[TaskTrace]()

// SubscribeTraceEvents returns runtime trace entries as they are appended.
func SubscribeTraceEvents(ctx context.Context) <-chan pubsub.Event[TaskTrace] {
	return traceBroker.Subscribe(ctx)
}

// PublishTraceEvent emits a runtime trace entry to UI and app subscribers.
func PublishTraceEvent(entry TaskTrace) {
	traceBroker.Publish(pubsub.UpdatedEvent, entry)
}

// EventBus is the runtime callback used to surface semantic events.
type EventBus func(tea.Msg)

// RuntimeSession holds mutable process state that should outlive a single
// prompt turn. It is intentionally small but typed so the scheduler, tools,
// and UI can share the same session-scoped view of the world.
type RuntimeSession struct {
	mu sync.RWMutex

	sessionID string
	rootPath  string

	compactHistory []string
	facts          map[string]string
	tools          map[string]struct{}
	lspStates      map[string]string
	mcpStates      map[string]string
	traceSeq       int64
	trace          []TaskTrace
	traceKeys      map[string]int

	eventBus EventBus
}

// NewSession creates a runtime session for a single workspace root.
func NewSession(rootPath string, bus EventBus) *RuntimeSession {
	return &RuntimeSession{
		rootPath:  rootPath,
		facts:     make(map[string]string),
		tools:     make(map[string]struct{}),
		lspStates: make(map[string]string),
		mcpStates: make(map[string]string),
		traceKeys: make(map[string]int),
		eventBus:  bus,
	}
}

// BindSession sets the current logical session identifier.
func (s *RuntimeSession) BindSession(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sessionID = sessionID
	s.mu.Unlock()
}

// SessionID returns the current logical session identifier.
func (s *RuntimeSession) SessionID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

// RootPath returns the workspace root associated with the runtime session.
func (s *RuntimeSession) RootPath() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rootPath
}

// Emit forwards a semantic event to the configured bus.
func (s *RuntimeSession) Emit(msg tea.Msg) {
	if s == nil || s.eventBus == nil {
		return
	}
	s.eventBus(msg)
}

// CloneForRun creates a per-turn runtime snapshot that keeps persistent
// integration state but discards transient facts and compact history.
func (s *RuntimeSession) CloneForRun(sessionID string) *RuntimeSession {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if sessionID == "" {
		sessionID = s.sessionID
	}

	clone := &RuntimeSession{
		sessionID: sessionID,
		rootPath:  s.rootPath,
		facts:     make(map[string]string),
		tools:     make(map[string]struct{}, len(s.tools)),
		lspStates: make(map[string]string, len(s.lspStates)),
		mcpStates: make(map[string]string, len(s.mcpStates)),
		trace:     nil,
		traceSeq:  0,
		traceKeys: make(map[string]int),
		eventBus:  s.eventBus,
	}
	for name := range s.tools {
		clone.tools[name] = struct{}{}
	}
	for name, state := range s.lspStates {
		clone.lspStates[name] = state
	}
	for name, state := range s.mcpStates {
		clone.mcpStates[name] = state
	}
	return clone
}

// SetCompactHistory replaces the compact session summary.
func (s *RuntimeSession) SetCompactHistory(entries []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.compactHistory = append([]string(nil), entries...)
	s.mu.Unlock()
}

// AppendCompactHistory appends a single entry to the compact session summary.
func (s *RuntimeSession) AppendCompactHistory(entry string) {
	if s == nil || entry == "" {
		return
	}
	s.mu.Lock()
	s.compactHistory = append(s.compactHistory, entry)
	s.mu.Unlock()
}

// CompactHistory returns a defensive copy of the compact session summary.
func (s *RuntimeSession) CompactHistory() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.compactHistory...)
}

// SetFact stores a typed fact in the runtime session.
func (s *RuntimeSession) SetFact(key, value string) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	s.facts[key] = value
	s.mu.Unlock()
}

// ResetEphemeralState clears transient per-turn state while preserving tools
// and integration state that should survive across prompts.
func (s *RuntimeSession) ResetEphemeralState() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.compactHistory = nil
	s.facts = make(map[string]string)
	s.trace = nil
	s.traceSeq = 0
	s.traceKeys = make(map[string]int)
	s.mu.Unlock()
}

// Fact returns a typed fact from the runtime session.
func (s *RuntimeSession) Fact(key string) (string, bool) {
	if s == nil || key == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.facts[key]
	return value, ok
}

// RegisterTool marks a tool as available in the runtime session.
func (s *RuntimeSession) RegisterTool(name string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	s.tools[name] = struct{}{}
	s.mu.Unlock()
}

// Tools returns the registered tool names in no guaranteed order.
func (s *RuntimeSession) Tools() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.tools))
	for name := range s.tools {
		result = append(result, name)
	}
	return result
}

// SetLSPState stores the current state of a named LSP client.
func (s *RuntimeSession) SetLSPState(name, state string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	s.lspStates[name] = state
	s.mu.Unlock()
}

// LSPStates returns a copy of the current LSP state map.
func (s *RuntimeSession) LSPStates() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.lspStates))
	for name, state := range s.lspStates {
		result[name] = state
	}
	return result
}

// SetMCPState stores the current state of a named MCP client.
func (s *RuntimeSession) SetMCPState(name, state string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	s.mcpStates[name] = state
	s.mu.Unlock()
}

// MCPStates returns a copy of the current MCP state map.
func (s *RuntimeSession) MCPStates() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.mcpStates))
	for name, state := range s.mcpStates {
		result[name] = state
	}
	return result
}

// AppendTrace stores an immutable trace entry and assigns a sequence number
// when the caller leaves it unset.
func (s *RuntimeSession) AppendTrace(entry TaskTrace) TaskTrace {
	if s == nil {
		return entry
	}

	s.mu.Lock()

	if entry.ConversationSessionID == "" {
		entry.ConversationSessionID = s.sessionID
	}
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = timeNow()
	}
	entry.Scope = append([]string(nil), entry.Scope...)

	traceKey := traceDedupKey(entry)
	if s.traceKeys == nil {
		s.traceKeys = make(map[string]int, len(s.trace)+1)
		for i, existing := range s.trace {
			s.traceKeys[traceDedupKey(existing)] = i
		}
	}
	if index, ok := s.traceKeys[traceKey]; ok {
		existing := s.trace[index]
		existing.Scope = append([]string(nil), existing.Scope...)
		s.mu.Unlock()
		return existing
	}

	if entry.Sequence <= 0 {
		s.traceSeq++
		entry.Sequence = s.traceSeq
	} else if entry.Sequence > s.traceSeq {
		s.traceSeq = entry.Sequence
	}

	s.traceKeys[traceKey] = len(s.trace)
	s.trace = append(s.trace, entry)
	s.mu.Unlock()

	PublishTraceEvent(entry)
	return entry
}

// TraceEntries returns a defensive copy of the recorded trace entries.
func (s *RuntimeSession) TraceEntries() []TaskTrace {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]TaskTrace, len(s.trace))
	for i, entry := range s.trace {
		entry.Scope = append([]string(nil), entry.Scope...)
		result[i] = entry
	}
	return result
}

func timeNow() time.Time {
	return time.Now().UTC()
}

func traceDedupKey(entry TaskTrace) string {
	type traceEntryDedup struct {
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
	key := traceEntryDedup{
		RecordedAt:                    entry.RecordedAt,
		StartedAt:                     entry.StartedAt,
		FinishedAt:                    entry.FinishedAt,
		DurationMs:                    entry.DurationMs,
		ConversationSessionID:         entry.ConversationSessionID,
		SessionID:                     entry.SessionID,
		NodeID:                        entry.NodeID,
		ParentID:                      entry.ParentID,
		Depth:                         entry.Depth,
		Profile:                       entry.Profile,
		ProviderID:                    entry.ProviderID,
		ProviderType:                  entry.ProviderType,
		ModelID:                       entry.ModelID,
		RequestID:                     entry.RequestID,
		HTTPTraceID:                   entry.HTTPTraceID,
		Kind:                          entry.Kind,
		Status:                        entry.Status,
		Success:                       entry.Success,
		Goal:                          entry.Goal,
		Scope:                         append([]string(nil), entry.Scope...),
		Attempt:                       entry.Attempt,
		StepNumber:                    entry.StepNumber,
		Input:                         entry.Input,
		Output:                        entry.Output,
		Error:                         entry.Error,
		InputBytes:                    entry.InputBytes,
		OutputBytes:                   entry.OutputBytes,
		InputTokens:                   entry.InputTokens,
		OutputTokens:                  entry.OutputTokens,
		TotalTokens:                   entry.TotalTokens,
		ReasoningTokens:               entry.ReasoningTokens,
		CacheCreationTokens:           entry.CacheCreationTokens,
		CacheReadTokens:               entry.CacheReadTokens,
		EstimatedCostUSD:              entry.EstimatedCostUSD,
		ContextMessageCount:           entry.ContextMessageCount,
		ContextBytes:                  entry.ContextBytes,
		PreflightEstimatedInputTokens: entry.PreflightEstimatedInputTokens,
		ContextWindowTokens:           entry.ContextWindowTokens,
		AutoSummarizeThresholdRatio:   entry.AutoSummarizeThresholdRatio,
		AutoSummarizeThresholdTokens:  entry.AutoSummarizeThresholdTokens,
		AutoSummarizeUsedTokens:       entry.AutoSummarizeUsedTokens,
		AutoSummarizeTriggered:        entry.AutoSummarizeTriggered,
		AttachmentCount:               entry.AttachmentCount,
		FileCount:                     entry.FileCount,
		ToolCount:                     entry.ToolCount,
		ToolSchemaBytes:               entry.ToolSchemaBytes,
		MaxOutputTokens:               entry.MaxOutputTokens,
		FirstEventType:                entry.FirstEventType,
		FirstEventLatencyMs:           entry.FirstEventLatencyMs,
		FirstTextLatencyMs:            entry.FirstTextLatencyMs,
		RetryDelayMs:                  entry.RetryDelayMs,
		FinishReason:                  entry.FinishReason,
		ToolName:                      entry.ToolName,
		ToolCallID:                    entry.ToolCallID,
		ToolInput:                     entry.ToolInput,
		ToolOutput:                    entry.ToolOutput,
		ToolInputBytes:                entry.ToolInputBytes,
		ToolOutputBytes:               entry.ToolOutputBytes,
		ToolIsError:                   entry.ToolIsError,
		ToolStopTurn:                  entry.ToolStopTurn,
		CommandID:                     entry.CommandID,
		Command:                       entry.Command,
		WorkingDir:                    entry.WorkingDir,
		ExitCode:                      entry.ExitCode,
		Outcome:                       entry.Outcome,
		StdoutBytes:                   entry.StdoutBytes,
		StderrBytes:                   entry.StderrBytes,
		ShellID:                       entry.ShellID,
	}
	data, _ := json.Marshal(key)
	return string(data)
}
