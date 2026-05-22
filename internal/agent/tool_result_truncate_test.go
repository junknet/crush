package agent

import (
	"strconv"
	"strings"
	"testing"
)

// TestTruncateToolResultContent verifies the universal per-tool-result cap that
// convertToToolResult applies, so one oversized result can't blow the context
// window in a single step (auto-summarize is reactive, see StopWhen).
func TestTruncateToolResultContent(t *testing.T) {
	t.Run("under cap is untouched", func(t *testing.T) {
		in := strings.Repeat("a", maxToolResultLength)
		if got := truncateToolResultContent(in); got != in {
			t.Fatalf("content at the cap should be unchanged: got len %d", len(got))
		}
	})

	t.Run("over cap is truncated with marker and bounded length", func(t *testing.T) {
		head := strings.Repeat("H", 200_000)
		tail := strings.Repeat("T", 100_000)
		in := head + tail // 300k chars, well over the 120k cap
		got := truncateToolResultContent(in)

		if !strings.Contains(got, "characters truncated to protect the context window") {
			t.Fatalf("expected truncation marker, got len %d", len(got))
		}
		if !strings.HasPrefix(got, "HHHH") || !strings.HasSuffix(got, "TTTT") {
			t.Fatalf("head/tail not preserved")
		}
		if len(got) > maxToolResultLength+200 {
			t.Fatalf("truncated result not bounded: len %d > cap %d", len(got), maxToolResultLength)
		}
		half := maxToolResultLength / 2
		wantOmitted := len(in) - 2*half
		if !strings.Contains(got, "["+strconv.Itoa(wantOmitted)+" characters truncated") {
			t.Fatalf("omitted count wrong; want %d in marker", wantOmitted)
		}
	})
}
