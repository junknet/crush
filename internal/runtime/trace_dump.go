package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type traceDumpEntry struct {
	TraceKey                      string   `json:"trace_key"`
	Sequence                      int64    `json:"sequence"`
	RecordedAt                    string   `json:"recorded_at"`
	StartedAt                     string   `json:"started_at,omitempty"`
	FinishedAt                    string   `json:"finished_at,omitempty"`
	DurationMs                    int64    `json:"duration_ms"`
	ConversationSessionID         string   `json:"conversation_session_id"`
	SessionID                     string   `json:"session_id"`
	NodeID                        string   `json:"node_id"`
	ParentID                      string   `json:"parent_id"`
	Depth                         int      `json:"depth"`
	Profile                       string   `json:"profile"`
	ProviderID                    string   `json:"provider_id,omitempty"`
	ProviderType                  string   `json:"provider_type,omitempty"`
	ModelID                       string   `json:"model_id,omitempty"`
	RequestID                     string   `json:"request_id,omitempty"`
	HTTPTraceID                   string   `json:"http_trace_id,omitempty"`
	Kind                          string   `json:"kind"`
	Status                        string   `json:"status"`
	Success                       bool     `json:"success"`
	Goal                          string   `json:"goal"`
	Scope                         []string `json:"scope"`
	Attempt                       int      `json:"attempt,omitempty"`
	StepNumber                    int      `json:"step_number,omitempty"`
	Input                         string   `json:"input,omitempty"`
	Output                        string   `json:"output,omitempty"`
	Error                         string   `json:"error,omitempty"`
	InputBytes                    int      `json:"input_bytes"`
	OutputBytes                   int      `json:"output_bytes"`
	InputTokens                   int64    `json:"input_tokens"`
	OutputTokens                  int64    `json:"output_tokens"`
	TotalTokens                   int64    `json:"total_tokens"`
	ReasoningTokens               int64    `json:"reasoning_tokens"`
	CacheCreationTokens           int64    `json:"cache_creation_tokens"`
	CacheReadTokens               int64    `json:"cache_read_tokens"`
	EstimatedCostUSD              float64  `json:"estimated_cost_usd"`
	ContextMessageCount           int      `json:"context_message_count,omitempty"`
	ContextBytes                  int      `json:"context_bytes,omitempty"`
	PreflightEstimatedInputTokens int64    `json:"preflight_estimated_input_tokens,omitempty"`
	ContextWindowTokens           int64    `json:"context_window_tokens,omitempty"`
	AutoSummarizeThresholdRatio   float64  `json:"auto_summarize_threshold_ratio,omitempty"`
	AutoSummarizeThresholdTokens  int64    `json:"auto_summarize_threshold_tokens,omitempty"`
	AutoSummarizeUsedTokens       int64    `json:"auto_summarize_used_tokens,omitempty"`
	AutoSummarizeTriggered        bool     `json:"auto_summarize_triggered,omitempty"`
	AttachmentCount               int      `json:"attachment_count,omitempty"`
	FileCount                     int      `json:"file_count,omitempty"`
	ToolCount                     int      `json:"tool_count,omitempty"`
	ToolSchemaBytes               int      `json:"tool_schema_bytes,omitempty"`
	MaxOutputTokens               int64    `json:"max_output_tokens,omitempty"`
	FirstEventType                string   `json:"first_event_type,omitempty"`
	FirstEventLatencyMs           int64    `json:"first_event_latency_ms,omitempty"`
	FirstTextLatencyMs            int64    `json:"first_text_latency_ms,omitempty"`
	RetryDelayMs                  int64    `json:"retry_delay_ms,omitempty"`
	FinishReason                  string   `json:"finish_reason,omitempty"`
	ToolName                      string   `json:"tool_name,omitempty"`
	ToolCallID                    string   `json:"tool_call_id,omitempty"`
	ToolInput                     string   `json:"tool_input,omitempty"`
	ToolOutput                    string   `json:"tool_output,omitempty"`
	ToolInputBytes                int      `json:"tool_input_bytes,omitempty"`
	ToolOutputBytes               int      `json:"tool_output_bytes,omitempty"`
	ToolIsError                   bool     `json:"tool_is_error,omitempty"`
	ToolStopTurn                  bool     `json:"tool_stop_turn,omitempty"`
	CommandID                     string   `json:"command_id,omitempty"`
	Command                       string   `json:"command,omitempty"`
	WorkingDir                    string   `json:"working_dir,omitempty"`
	ExitCode                      *int     `json:"exit_code,omitempty"`
	Outcome                       string   `json:"outcome,omitempty"`
	StdoutBytes                   int      `json:"stdout_bytes"`
	StderrBytes                   int      `json:"stderr_bytes"`
	ShellID                       string   `json:"shell_id,omitempty"`
}

// WriteTraceJSONL writes trace entries as newline-delimited JSON records.
func WriteTraceJSONL(w io.Writer, traces []TaskTrace) error {
	enc := json.NewEncoder(w)
	for _, trace := range traces {
		entry := newTraceDumpEntry(trace)
		if err := enc.Encode(entry); err != nil {
			return fmt.Errorf("encode trace entry %q: %w", trace.TraceKey(), err)
		}
	}
	return nil
}

func newTraceDumpEntry(trace TaskTrace) traceDumpEntry {
	return traceDumpEntry{
		TraceKey:                      trace.TraceKey(),
		Sequence:                      trace.Sequence,
		RecordedAt:                    trace.RecordedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		StartedAt:                     formatTraceTime(trace.StartedAt),
		FinishedAt:                    formatTraceTime(trace.FinishedAt),
		DurationMs:                    trace.DurationMs,
		ConversationSessionID:         trace.ConversationSessionID,
		SessionID:                     trace.SessionID,
		NodeID:                        trace.NodeID,
		ParentID:                      trace.ParentID,
		Depth:                         trace.Depth,
		Profile:                       trace.Profile,
		ProviderID:                    trace.ProviderID,
		ProviderType:                  trace.ProviderType,
		ModelID:                       trace.ModelID,
		RequestID:                     trace.RequestID,
		HTTPTraceID:                   trace.HTTPTraceID,
		Kind:                          trace.Kind.String(),
		Status:                        trace.Status,
		Success:                       trace.Success,
		Goal:                          trace.Goal,
		Scope:                         append([]string(nil), trace.Scope...),
		Attempt:                       trace.Attempt,
		StepNumber:                    trace.StepNumber,
		Input:                         trace.Input,
		Output:                        trace.Output,
		Error:                         trace.Error,
		InputBytes:                    trace.InputBytes,
		OutputBytes:                   trace.OutputBytes,
		InputTokens:                   trace.InputTokens,
		OutputTokens:                  trace.OutputTokens,
		TotalTokens:                   trace.TotalTokens,
		ReasoningTokens:               trace.ReasoningTokens,
		CacheCreationTokens:           trace.CacheCreationTokens,
		CacheReadTokens:               trace.CacheReadTokens,
		EstimatedCostUSD:              trace.EstimatedCostUSD,
		ContextMessageCount:           trace.ContextMessageCount,
		ContextBytes:                  trace.ContextBytes,
		PreflightEstimatedInputTokens: trace.PreflightEstimatedInputTokens,
		ContextWindowTokens:           trace.ContextWindowTokens,
		AutoSummarizeThresholdRatio:   trace.AutoSummarizeThresholdRatio,
		AutoSummarizeThresholdTokens:  trace.AutoSummarizeThresholdTokens,
		AutoSummarizeUsedTokens:       trace.AutoSummarizeUsedTokens,
		AutoSummarizeTriggered:        trace.AutoSummarizeTriggered,
		AttachmentCount:               trace.AttachmentCount,
		FileCount:                     trace.FileCount,
		ToolCount:                     trace.ToolCount,
		ToolSchemaBytes:               trace.ToolSchemaBytes,
		MaxOutputTokens:               trace.MaxOutputTokens,
		FirstEventType:                trace.FirstEventType,
		FirstEventLatencyMs:           trace.FirstEventLatencyMs,
		FirstTextLatencyMs:            trace.FirstTextLatencyMs,
		RetryDelayMs:                  trace.RetryDelayMs,
		FinishReason:                  trace.FinishReason,
		ToolName:                      trace.ToolName,
		ToolCallID:                    trace.ToolCallID,
		ToolInput:                     trace.ToolInput,
		ToolOutput:                    trace.ToolOutput,
		ToolInputBytes:                trace.ToolInputBytes,
		ToolOutputBytes:               trace.ToolOutputBytes,
		ToolIsError:                   trace.ToolIsError,
		ToolStopTurn:                  trace.ToolStopTurn,
		CommandID:                     trace.CommandID,
		Command:                       trace.Command,
		WorkingDir:                    trace.WorkingDir,
		ExitCode:                      trace.ExitCode,
		Outcome:                       trace.Outcome,
		StdoutBytes:                   trace.StdoutBytes,
		StderrBytes:                   trace.StderrBytes,
		ShellID:                       trace.ShellID,
	}
}

func formatTraceTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

// WriteTraceJSONLFile writes trace entries to a JSONL file, creating parent
// directories as needed.
func WriteTraceJSONLFile(path string, traces []TaskTrace) error {
	if path == "" {
		return fmt.Errorf("trace output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create trace output directory %q: %w", filepath.Dir(path), err)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create trace output file %q: %w", path, err)
	}
	defer file.Close()

	if err := WriteTraceJSONL(file, traces); err != nil {
		return fmt.Errorf("write trace output file %q: %w", path, err)
	}
	return nil
}

// TraceJSONLFileWriter appends trace entries to a JSONL file as they happen.
type TraceJSONLFileWriter struct {
	mu     sync.Mutex
	file   *os.File
	enc    *json.Encoder
	path   string
	seen   map[string]struct{}
	closed bool
}

// NewTraceJSONLFileWriter creates or truncates a trace JSONL file for live
// runtime trace streaming.
func NewTraceJSONLFileWriter(path string) (*TraceJSONLFileWriter, error) {
	if path == "" {
		return nil, fmt.Errorf("trace output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create trace output directory %q: %w", filepath.Dir(path), err)
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create trace output file %q: %w", path, err)
	}
	return &TraceJSONLFileWriter{
		file: file,
		enc:  json.NewEncoder(file),
		path: path,
		seen: make(map[string]struct{}),
	}, nil
}

// Append writes one trace entry and syncs it so black-box TUI tests can inspect
// the file before the process exits.
func (w *TraceJSONLFileWriter) Append(trace TaskTrace) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("trace output file %q is closed", w.path)
	}
	uniqueKey := traceDumpUniqueKey(trace)
	if _, ok := w.seen[uniqueKey]; ok {
		return nil
	}
	if err := w.enc.Encode(newTraceDumpEntry(trace)); err != nil {
		return fmt.Errorf("encode trace entry %q: %w", trace.TraceKey(), err)
	}
	w.seen[uniqueKey] = struct{}{}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync trace output file %q: %w", w.path, err)
	}
	return nil
}

func traceDumpUniqueKey(trace TaskTrace) string {
	if trace.Sequence > 0 {
		return fmt.Sprintf("seq:%d:%s:%s", trace.Sequence, trace.RecordedAt.UTC().Format(time.RFC3339Nano), trace.TraceKey())
	}
	return fmt.Sprintf("key:%s:%s", trace.TraceKey(), trace.RecordedAt.UTC().Format(time.RFC3339Nano))
}

// Close closes the live trace writer.
func (w *TraceJSONLFileWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}
