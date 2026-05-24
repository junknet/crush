package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpiller_NilSafe(t *testing.T) {
	var s *Spiller
	res, ok := s.MaybeSpill("sess", "call", "bash", 1, SpillPart{Content: "x"})
	if ok || res.Path != "" {
		t.Errorf("nil Spiller should no-op, got %+v ok=%v", res, ok)
	}
}

func TestSpiller_BelowThreshold(t *testing.T) {
	tmp := t.TempDir()
	s := NewSpiller(tmp)
	res, ok := s.MaybeSpill("sess", "call", "bash", 100,
		SpillPart{Content: "tiny stdout"},
		SpillPart{Name: "stderr", Content: "tiny stderr"},
	)
	if ok {
		t.Errorf("under-threshold should not spill; got %+v", res)
	}
	entries, _ := os.ReadDir(filepath.Join(tmp, SpillSubdir))
	if len(entries) != 0 {
		t.Errorf("no files should exist below threshold, got %v", entries)
	}
}

func TestSpiller_AboveThreshold_WritesBothParts(t *testing.T) {
	tmp := t.TempDir()
	s := &Spiller{
		DataDir: tmp,
		Subdir:  SpillSubdir,
		Now:     func() time.Time { return time.Unix(1700000000, 42) },
	}
	stdout := strings.Repeat("o", 50)
	stderr := strings.Repeat("e", 50)
	res, ok := s.MaybeSpill("sess-abc", "call-xyz", "bash", 50,
		SpillPart{Content: stdout},
		SpillPart{Name: "stderr", Content: stderr},
	)
	if !ok {
		t.Fatal("over-threshold should spill")
	}
	if !strings.Contains(res.Path, "bash-call-xyz-") {
		t.Errorf("filename should include label+callID: %s", res.Path)
	}
	if !strings.Contains(res.Path, filepath.Join(SpillSubdir, "sess-abc")) {
		t.Errorf("path should include subdir/session: %s", res.Path)
	}
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := stdout + "\n--- stderr ---\n" + stderr
	if string(raw) != want {
		t.Errorf("byte mismatch.\n got=%q\nwant=%q", raw, want)
	}
	if res.Bytes != len(want) {
		t.Errorf("Bytes %d != actual %d", res.Bytes, len(want))
	}
}

func TestSpiller_SkipsEmptyPart(t *testing.T) {
	tmp := t.TempDir()
	s := NewSpiller(tmp)
	big := strings.Repeat("o", 200)
	res, ok := s.MaybeSpill("sess", "call", "bash", 50,
		SpillPart{Content: big},
		SpillPart{Name: "stderr", Content: ""},
	)
	if !ok {
		t.Fatal("expected spill")
	}
	raw, _ := os.ReadFile(res.Path)
	if strings.Contains(string(raw), "stderr") {
		t.Errorf("empty stderr part should not emit header: %q", raw)
	}
	if string(raw) != big {
		t.Errorf("only primary content should be written")
	}
}

func TestSpiller_MissingDataDir(t *testing.T) {
	s := &Spiller{DataDir: ""}
	res, ok := s.MaybeSpill("sess", "call", "bash", 1, SpillPart{Content: "x"})
	if ok || res.Path != "" {
		t.Errorf("empty DataDir should no-op")
	}
}
