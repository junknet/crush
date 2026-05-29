package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/message"
)

// Redundant tool compaction thresholds.
const (
	// Redundant tool calls are compacted if they are older than this.
	// We want to keep recent ones as they are likely relevant to the current turn.
	compactionRedundancyProtectRecent = 3
)

// compactRedundantToolResults walks the message history and identifies redundant
// tool outputs (e.g., multiple ls on the same path, multiple rg with same query).
// It keeps only the most recent occurrence of each redundant call and replaces
// the previous ones with a placeholder.
func (a *sessionAgent) compactRedundantToolResults(ctx context.Context, sessionID string) {
	if sessionID == "" {
		return
	}
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		slog.Warn("ContextCompaction: list messages failed", "session", sessionID, "error", err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	// Identify redundant calls.
	// key: tool name + normalized input
	// value: index of the message and part
	type callPos struct {
		msgIdx  int
		partIdx int
		time    int64
	}
	
	// Track the latest position of each unique tool call.
	latestCalls := make(map[string]callPos)
	
	// We only care about Tool messages and their corresponding Assistant tool calls.
	// But since ToolResults already contain the Name and ToolCallID, and we can 
	// get the Input from the Assistant message.
	
	// Map ToolCallID -> Input
	callInputs := make(map[string]string)
	for _, m := range msgs {
		if m.Role == message.Assistant {
			for _, tc := range m.ToolCalls() {
				callInputs[tc.ID] = tc.Input
			}
		}
	}

	// First pass: identify latest occurrences.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != message.Tool {
			continue
		}
		for j, p := range m.Parts {
			tr, ok := p.(message.ToolResult)
			if !ok {
				continue
			}
			
			// Normalized input for redundancy detection.
			input := callInputs[tr.ToolCallID]

			// For edit/multiedit, redundancy is per file.
			// For ls, it's per path + options.
			// For rg, it's per query + path + options.
			// For view, it's per file + options.
			redundancyKey := tr.Name
			if tr.Name == "edit" || tr.Name == "multiedit" || tr.Name == "write" {
				// Keep only the last successful write/edit per file.
				if tr.IsError {
					// Errors are not redundant.
					continue
				}
				filePath := getFilePathFromInput(tr.Name, input)
				if filePath == "" {
					continue
				}
				redundancyKey = fmt.Sprintf("%s:%s", tr.Name, filePath)
			} else {
				redundancyKey = fmt.Sprintf("%s:%s", tr.Name, normalizeToolInput(tr.Name, input))
			}

			if _, exists := latestCalls[redundancyKey]; !exists {
				latestCalls[redundancyKey] = callPos{msgIdx: i, partIdx: j, time: m.CreatedAt}
			}
		}
	}

	// Second pass: compact older ones.
	changedMsgs := make(map[int]message.Message)
	clearedTotal := 0

	// Protect the most recent N tool messages regardless of redundancy.
	protectedMsgs := make(map[string]struct{})
	seenToolMsgs := 0
	for i := len(msgs) - 1; i >= 0 && seenToolMsgs < compactionRedundancyProtectRecent; i-- {
		if msgs[i].Role == message.Tool {
			protectedMsgs[msgs[i].ID] = struct{}{}
			seenToolMsgs++
		}
	}

	for i, m := range msgs {
		if m.Role != message.Tool {
			continue
		}
		if _, protected := protectedMsgs[m.ID]; protected {
			continue
		}

		changed := false
		for j, p := range m.Parts {
			tr, ok := p.(message.ToolResult)
			if !ok {
				continue
			}

			input := callInputs[tr.ToolCallID]
			redundancyKey := tr.Name
			if tr.Name == "edit" || tr.Name == "multiedit" || tr.Name == "write" {
				if tr.IsError {
					continue
				}
				filePath := getFilePathFromInput(tr.Name, input)
				if filePath == "" {
					continue
				}
				redundancyKey = fmt.Sprintf("%s:%s", tr.Name, filePath)
			} else {
				redundancyKey = fmt.Sprintf("%s:%s", tr.Name, normalizeToolInput(tr.Name, input))
			}

			pos, exists := latestCalls[redundancyKey]
			if !exists {
				continue
			}

			// If this is NOT the latest occurrence of this specific call.
			if pos.msgIdx != i || pos.partIdx != j {
				// Don't compact if it's already a placeholder.
				if isCompactionPlaceholder(tr.Content) {
					continue
				}

				clearedTotal += len(tr.Content)
				tr.Content = fmt.Sprintf("[Tool output omitted as redundant — a newer result for '%s' is available later in the history.]", tr.Name)
				// We don't spill these to disk because they are redundant (the content is still available in a later message).
				m.Parts[j] = tr
				changed = true
			}
		}

		if changed {
			changedMsgs[i] = m
		}
	}

	// Apply changes.
	for i, m := range changedMsgs {
		if err := a.messages.Update(ctx, m); err != nil {
			slog.Warn("ContextCompaction: update failed", "session", sessionID, "message", m.ID, "error", err)
		} else {
			slog.Info("ContextCompaction: compacted redundant tool result",
				"session", sessionID,
				"message", m.ID,
				"idx", i,
			)
		}
	}
	
	if clearedTotal > 0 {
		slog.Info("ContextCompaction: finished", "session", sessionID, "cleared_bytes", clearedTotal)
	}
}

// normalizeToolInput attempts to normalize tool inputs so that minor variations 
// (like extra whitespace or reordered JSON keys) don't defeat redundancy detection.
func normalizeToolInput(name, input string) string {
	if input == "" {
		return ""
	}
	
	// For JSON inputs, unmarshal and marshal to normalize.
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err == nil {
		// Remove volatile or irrelevant fields if any.
		// For example, some tools might have a 'session_id' or 'trace_id' that changes.
		
		// Re-marshal to get a stable string.
		if normalized, err := json.Marshal(m); err == nil {
			return string(normalized)
		}
	}
	
	return input
}

func isCompactionPlaceholder(content string) bool {
	return strings.HasPrefix(content, "[Tool output omitted") ||
		strings.HasPrefix(content, "[Tool result cleared")
}

func getFilePathFromInput(name, input string) string {
	if input == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		return ""
	}
	if v, ok := m["file_path"].(string); ok {
		return v
	}
	if v, ok := m["path"].(string); ok {
		return v
	}
	return ""
}
