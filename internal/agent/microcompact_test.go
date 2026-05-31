package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// rewriteToolResults must clear stale oversized tool results — counting BOTH
// the text Content and the binary Data (image base64) — spill the recoverable
// text to disk, drop the binary payload, and leave small results untouched.
func TestRewriteToolResultsClearsTextAndImage(t *testing.T) {
	a := &sessionAgent{dataDir: t.TempDir()}
	now := time.Now()

	bigText := strings.Repeat("x", microCompactToolResultMax+1)
	bigImage := strings.Repeat("A", microCompactToolResultMax+1) // base64-ish blob in Data

	msg := &message.Message{
		ID:   "m1",
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "t-text", Content: bigText},
			message.ToolResult{ToolCallID: "t-img", Content: "screenshot", Data: bigImage, MIMEType: "image/png"},
			message.ToolResult{ToolCallID: "t-small", Content: "tiny"},
		},
	}

	changed, cleared := a.rewriteToolResults("s1", msg, now)
	require.True(t, changed)
	require.Greater(t, cleared, 2*microCompactToolResultMax, "both oversized results counted")

	// Large text: cleared to a stub pointing at a recoverable spill file.
	trText := msg.Parts[0].(message.ToolResult)
	require.Contains(t, trText.Content, "cleared by microCompact")

	// Large image: Data dropped (the context-balloon fix), MIME cleared, stub set.
	trImg := msg.Parts[1].(message.ToolResult)
	require.Empty(t, trImg.Data, "stale image payload must be dropped from context")
	require.Empty(t, trImg.MIMEType)
	require.Contains(t, trImg.Content, "cleared by microCompact")

	// Small result is left untouched.
	require.Equal(t, "tiny", msg.Parts[2].(message.ToolResult).Content)

	// The text spill is recoverable on disk.
	path := regexp.MustCompile(`spill at (\S+)\.`).FindStringSubmatch(trText.Content)
	require.Len(t, path, 2, "stub must carry a spill path")
	data, err := os.ReadFile(path[1])
	require.NoError(t, err)
	require.Equal(t, bigText, string(data))
}

// Sub-threshold results (even with a small image) are never rewritten.
func TestRewriteToolResultsSkipsSmall(t *testing.T) {
	a := &sessionAgent{dataDir: t.TempDir()}
	msg := &message.Message{
		ID:    "m1",
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: "t", Content: "ok", Data: "iVBOR", MIMEType: "image/png"}},
	}
	changed, cleared := a.rewriteToolResults("s1", msg, time.Now())
	require.False(t, changed)
	require.Zero(t, cleared)
	require.Equal(t, "ok", msg.Parts[0].(message.ToolResult).Content)
}
