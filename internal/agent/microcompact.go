package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/message"
)

// MicroCompact thresholds. These are deliberately conservative — clearing
// too eagerly hides evidence the next assistant turn might still need, but
// clearing too late lets one giant tool result balloon the whole
// conversation token budget. The numbers below were picked to match the
// free-code reference: anything above 50 KiB that has aged out of recent
// reference is fair game.
const (
	microCompactToolResultMax = 50 * 1024
	microCompactIdleDuration  = 5 * time.Minute
	// microCompactProtectRecent keeps the latest N tool messages untouched
	// even if they exceed the size threshold; recent tool results are
	// often consulted again on the next step.
	microCompactProtectRecent = 2
)

// microCompactStep walks the recent message history and rewrites oversized
// tool-result content to a placeholder + spill path. It runs at the end of
// every assistant step and is intentionally fast: pure local IO, no LLM
// call, no network. Failures are logged and swallowed so a compaction hiccup
// never breaks the live conversation.
func (a *sessionAgent) microCompactStep(ctx context.Context, sessionID string) {
	if a.dataDir == "" || sessionID == "" {
		return
	}
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		slog.Warn("MicroCompact: list messages failed", "session", sessionID, "error", err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	now := time.Now()
	cutoff := now.Add(-microCompactIdleDuration).UnixMilli()

	// Identify the indexes of the most recent N tool messages so we can
	// shield them from compaction.
	protected := make(map[string]struct{}, microCompactProtectRecent)
	seen := 0
	for i := len(msgs) - 1; i >= 0 && seen < microCompactProtectRecent; i-- {
		if msgs[i].Role == message.Tool {
			protected[msgs[i].ID] = struct{}{}
			seen++
		}
	}

	for _, msg := range msgs {
		if msg.Role != message.Tool {
			continue
		}
		if _, shield := protected[msg.ID]; shield {
			continue
		}
		if msg.CreatedAt > cutoff {
			continue
		}
		changed, cleared := a.rewriteToolResults(sessionID, &msg, now)
		if !changed {
			continue
		}
		if err := a.messages.Update(ctx, msg); err != nil {
			slog.Warn("MicroCompact: update failed", "session", sessionID, "message", msg.ID, "error", err)
			continue
		}
		slog.Info("MicroCompact: rewrote tool results",
			"session", sessionID,
			"message", msg.ID,
			"cleared_bytes", cleared,
			"parts", len(msg.Parts),
			"ts", now.UnixMilli(),
		)
	}
}

// rewriteToolResults trims oversized ToolResult parts inside one message.
// Returns (changed, totalClearedBytes). Each rewritten content is preceded
// by a path to the spill file so the model can `view` it if needed.
func (a *sessionAgent) rewriteToolResults(sessionID string, msg *message.Message, now time.Time) (bool, int) {
	changed := false
	cleared := 0
	for i, p := range msg.Parts {
		tr, ok := p.(message.ToolResult)
		if !ok {
			continue
		}
		// Count both the text Content and the binary Data (e.g. a base64
		// image): an image result carries its payload in Data with only a
		// short Content, so a Content-only check would let large stale
		// screenshots balloon the context unchecked.
		size := len(tr.Content) + len(tr.Data)
		if size <= microCompactToolResultMax {
			continue
		}
		// Spill the recoverable text to disk under the same tool-results tree
		// bash uses so all per-session evidence sits together. Binary/image
		// Data is dropped (not spilled): a stale base64 blob is not usefully
		// re-viewable and spilling megabytes per old screenshot wastes disk.
		spillContent := tr.Content
		if tr.Data != "" {
			spillContent += fmt.Sprintf("\n[+ %d bytes of %s binary/image data dropped by microCompact]", len(tr.Data), tr.MIMEType)
		}
		path, _, err := writeMicroCompactSpill(a.dataDir, sessionID, tr.ToolCallID, spillContent, now)
		if err != nil {
			slog.Warn("MicroCompact: spill write failed", "session", sessionID, "tool_call", tr.ToolCallID, "error", err)
			continue
		}
		cleared += size
		tr.Content = fmt.Sprintf("[Tool result cleared by microCompact — original %d bytes (incl %d image/binary), spill at %s. Use the view tool on that path to recover the text.]", size, len(tr.Data), path)
		tr.Data = ""
		tr.MIMEType = ""
		msg.Parts[i] = tr
		changed = true
	}
	return changed, cleared
}

func writeMicroCompactSpill(dataDir, sessionID, toolCallID, content string, now time.Time) (string, int, error) {
	if dataDir == "" || sessionID == "" {
		return "", 0, fmt.Errorf("missing dataDir or sessionID")
	}
	dir := filepath.Join(dataDir, "tool-results", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir spill dir: %w", err)
	}
	// Best-effort GC.
	if rand.Intn(100) == 0 {
		gcMicroCompactDir(filepath.Join(dataDir, "tool-results"), 7*24*time.Hour)
	}
	name := fmt.Sprintf("micro-%s-%d.log", sanitizeMicroFilename(toolCallID), now.UnixNano())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", 0, fmt.Errorf("write spill: %w", err)
	}
	return path, len(content), nil
}

// sanitizeMicroFilename mirrors bash.sanitizeCallID; duplicated here to
// avoid leaking a private helper across package boundaries.
func sanitizeMicroFilename(id string) string {
	if id == "" {
		return "anon"
	}
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "anon"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

func gcMicroCompactDir(root string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(p)
		}
		return nil
	})
}
