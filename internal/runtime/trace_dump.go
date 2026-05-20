package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type traceDumpEntry struct {
	TraceKey              string   `json:"trace_key"`
	Sequence              int64    `json:"sequence"`
	RecordedAt            string   `json:"recorded_at"`
	StartedAt             string   `json:"started_at,omitempty"`
	FinishedAt            string   `json:"finished_at,omitempty"`
	DurationMs            int64    `json:"duration_ms"`
	ConversationSessionID string   `json:"conversation_session_id"`
	SessionID             string   `json:"session_id"`
	NodeID                string   `json:"node_id"`
	ParentID              string   `json:"parent_id"`
	Depth                 int      `json:"depth"`
	Profile               string   `json:"profile"`
	ProviderID            string   `json:"provider_id,omitempty"`
	ProviderType          string   `json:"provider_type,omitempty"`
	ModelID               string   `json:"model_id,omitempty"`
	RequestID             string   `json:"request_id,omitempty"`
	Kind                  string   `json:"kind"`
	Status                string   `json:"status"`
	Success               bool     `json:"success"`
	Goal                  string   `json:"goal"`
	Scope                 []string `json:"scope"`
	Input                 string   `json:"input,omitempty"`
	Output                string   `json:"output,omitempty"`
	Error                 string   `json:"error,omitempty"`
	InputBytes            int      `json:"input_bytes"`
	OutputBytes           int      `json:"output_bytes"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	TotalTokens           int64    `json:"total_tokens"`
	ReasoningTokens       int64    `json:"reasoning_tokens"`
	CacheCreationTokens   int64    `json:"cache_creation_tokens"`
	CacheReadTokens       int64    `json:"cache_read_tokens"`
	EstimatedCostUSD      float64  `json:"estimated_cost_usd"`
	ToolName              string   `json:"tool_name,omitempty"`
	ToolCallID            string   `json:"tool_call_id,omitempty"`
	ToolInput             string   `json:"tool_input,omitempty"`
	ToolOutput            string   `json:"tool_output,omitempty"`
	CommandID             string   `json:"command_id,omitempty"`
	Command               string   `json:"command,omitempty"`
	WorkingDir            string   `json:"working_dir,omitempty"`
	ExitCode              *int     `json:"exit_code,omitempty"`
	Outcome               string   `json:"outcome,omitempty"`
	StdoutBytes           int      `json:"stdout_bytes"`
	StderrBytes           int      `json:"stderr_bytes"`
	ShellID               string   `json:"shell_id,omitempty"`
}

// WriteTraceJSONL writes trace entries as newline-delimited JSON records.
func WriteTraceJSONL(w io.Writer, traces []TaskTrace) error {
	enc := json.NewEncoder(w)
	for _, trace := range traces {
		entry := traceDumpEntry{
			TraceKey:              trace.TraceKey(),
			Sequence:              trace.Sequence,
			RecordedAt:            trace.RecordedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
			StartedAt:             formatTraceTime(trace.StartedAt),
			FinishedAt:            formatTraceTime(trace.FinishedAt),
			DurationMs:            trace.DurationMs,
			ConversationSessionID: trace.ConversationSessionID,
			SessionID:             trace.SessionID,
			NodeID:                trace.NodeID,
			ParentID:              trace.ParentID,
			Depth:                 trace.Depth,
			Profile:               trace.Profile,
			ProviderID:            trace.ProviderID,
			ProviderType:          trace.ProviderType,
			ModelID:               trace.ModelID,
			RequestID:             trace.RequestID,
			Kind:                  trace.Kind.String(),
			Status:                trace.Status,
			Success:               trace.Success,
			Goal:                  trace.Goal,
			Scope:                 append([]string(nil), trace.Scope...),
			Input:                 trace.Input,
			Output:                trace.Output,
			Error:                 trace.Error,
			InputBytes:            trace.InputBytes,
			OutputBytes:           trace.OutputBytes,
			InputTokens:           trace.InputTokens,
			OutputTokens:          trace.OutputTokens,
			TotalTokens:           trace.TotalTokens,
			ReasoningTokens:       trace.ReasoningTokens,
			CacheCreationTokens:   trace.CacheCreationTokens,
			CacheReadTokens:       trace.CacheReadTokens,
			EstimatedCostUSD:      trace.EstimatedCostUSD,
			ToolName:              trace.ToolName,
			ToolCallID:            trace.ToolCallID,
			ToolInput:             trace.ToolInput,
			ToolOutput:            trace.ToolOutput,
			CommandID:             trace.CommandID,
			Command:               trace.Command,
			WorkingDir:            trace.WorkingDir,
			ExitCode:              trace.ExitCode,
			Outcome:               trace.Outcome,
			StdoutBytes:           trace.StdoutBytes,
			StderrBytes:           trace.StderrBytes,
			ShellID:               trace.ShellID,
		}
		if err := enc.Encode(entry); err != nil {
			return fmt.Errorf("encode trace entry %q: %w", trace.TraceKey(), err)
		}
	}
	return nil
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
