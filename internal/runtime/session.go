package runtime

import (
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

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
	defer s.mu.Unlock()

	if entry.ConversationSessionID == "" {
		entry.ConversationSessionID = s.sessionID
	}
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = timeNow()
	}
	if entry.Sequence <= 0 {
		s.traceSeq++
		entry.Sequence = s.traceSeq
	} else if entry.Sequence > s.traceSeq {
		s.traceSeq = entry.Sequence
	}

	entry.Scope = append([]string(nil), entry.Scope...)
	s.trace = append(s.trace, entry)
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
