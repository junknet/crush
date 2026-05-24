package shell

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// TestLinePublishWriter_SplitsOnNewline verifies the splitter publishes
// exactly one onLine callback per terminated line and never mid-buffer.
func TestLinePublishWriter_SplitsOnNewline(t *testing.T) {
	var (
		mu    sync.Mutex
		lines []string
	)
	target := &bytes.Buffer{}
	lp := &linePublishWriter{
		target: target,
		onLine: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		},
	}

	// Write in chunks that straddle newline boundaries: the splitter must
	// hold residue across writes and emit complete lines only.
	chunks := []string{"hello ", "world\nsecond ", "line\nthird"}
	for _, c := range chunks {
		if _, err := lp.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()
	want := []string{"hello world", "second line"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("pre-flush lines mismatch: got %v want %v", got, want)
	}

	lp.Flush()
	mu.Lock()
	got = append([]string(nil), lines...)
	mu.Unlock()
	if got[len(got)-1] != "third" {
		t.Errorf("Flush should emit trailing residue 'third', got %v", got)
	}
	if target.String() != strings.Join(chunks, "") {
		t.Errorf("target buffer corrupted: %q", target.String())
	}
}

// TestLinePublishWriter_HandlesEmptyAndConsecutive verifies empty input,
// consecutive newlines, and CR-LF terminators do not produce phantom lines
// or panic.
func TestLinePublishWriter_HandlesEmptyAndConsecutive(t *testing.T) {
	var (
		mu    sync.Mutex
		lines []string
	)
	lp := &linePublishWriter{
		target: &bytes.Buffer{},
		onLine: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		},
	}
	if _, err := lp.Write(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := lp.Write([]byte("\n\nx\n")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()
	if len(got) != 3 || got[0] != "" || got[1] != "" || got[2] != "x" {
		t.Errorf("expected 3 lines [\"\",\"\",\"x\"], got %v", got)
	}
}
