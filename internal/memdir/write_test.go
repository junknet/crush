package memdir

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEncodeDecodeFrontmatter_RoundTrip(t *testing.T) {
	fm := Frontmatter{
		Name:        "user-likes-nim",
		Description: "user defaults to Nim 2.0+ unless ML libs force Python",
		Type:        MemoryUser,
	}
	body := "Use Nim 2.0 for everything; Python only when sklearn etc. demand it.\n"
	encoded := EncodeFrontmatter(fm) + body
	got, gotBody, err := DecodeFrontmatter(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != fm {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, fm)
	}
	if gotBody != body {
		t.Errorf("body mismatch: got %q want %q", gotBody, body)
	}
}

func TestEncodeFrontmatter_StripsNewlineInDescription(t *testing.T) {
	fm := Frontmatter{
		Name:        "x",
		Description: "line one\nline two",
		Type:        MemoryProject,
	}
	out := EncodeFrontmatter(fm)
	// Header has 6 lines + one blank-line separator = 7 newlines. Any
	// extra newline means description's embedded newline leaked through.
	if strings.Count(out, "\n") != 7 {
		t.Errorf("Description newline leaked into header: %s", out)
	}
}

func TestDecodeFrontmatter_TypeWhitelist(t *testing.T) {
	bad := "---\nname: x\ndescription: y\nmetadata:\n  type: random\n---\n\nbody\n"
	_, _, err := DecodeFrontmatter(bad)
	if err == nil {
		t.Errorf("expected error for invalid type, got nil")
	}
}

func TestWriteMemoryFile_TypeWhitelistGuard(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	_, err := WriteMemoryFile(tmp, ws, Frontmatter{Name: "x", Type: "garbage"}, "body")
	if err == nil {
		t.Errorf("WriteMemoryFile should reject unknown type")
	}
}

func TestWriteMemoryFile_EmptyNameGuard(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	_, err := WriteMemoryFile(tmp, ws, Frontmatter{Name: "!!!", Type: MemoryUser}, "body")
	if err == nil {
		t.Errorf("WriteMemoryFile should reject name that sanitises to empty")
	}
}

func TestWriteMemoryFile_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	fm := Frontmatter{Name: "Pref Nim", Description: "stable", Type: MemoryUser}
	body := "Prefer Nim everywhere.\n"
	p1, err := WriteMemoryFile(tmp, ws, fm, body)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := WriteMemoryFile(tmp, ws, fm, body)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Errorf("same fm+body should yield same path; got %s vs %s", p1, p2)
	}
	// Slugified name should be lower-case kebab.
	if !strings.Contains(filepath.Base(p1), "pref-nim-") {
		t.Errorf("file name should slugify name: %s", p1)
	}
	// Read back: round-trip should succeed.
	raw, err := os.ReadFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	got, gotBody, err := DecodeFrontmatter(string(raw))
	if err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	if got.Name != "pref-nim" || got.Type != MemoryUser {
		t.Errorf("decoded fm wrong: %+v", got)
	}
	if strings.TrimRight(gotBody, "\n") != strings.TrimRight(body, "\n") {
		t.Errorf("decoded body wrong: %q", gotBody)
	}
}

func TestAppendEntry_Concurrent(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	if err := EnsureWorkspace(tmp, ws); err != nil {
		t.Fatal(err)
	}
	const goroutines = 5
	const perGoroutine = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for i := range perGoroutine {
				_ = AppendEntry(tmp, ws,
					"T", "x.md",
					strings.Repeat("h", (g*perGoroutine+i)%20+1))
			}
		}()
	}
	wg.Wait()

	raw, err := os.ReadFile(IndexPath(tmp, ws))
	if err != nil {
		t.Fatal(err)
	}
	// Count appended lines (those starting with "- [T](x.md)").
	count := strings.Count(string(raw), "- [T](x.md)")
	if count != goroutines*perGoroutine {
		t.Errorf("expected %d appended lines, got %d", goroutines*perGoroutine, count)
	}
	// Every appended line must be complete (terminated by \n and not split).
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "- [T](x.md) — ") {
			continue
		}
		if strings.ContainsAny(line, "\r") {
			t.Errorf("line corrupted: %q", line)
		}
	}
}

func TestAppendEntry_TruncatesLongHook(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	huge := strings.Repeat("z", MaxAppendHookBytes+50)
	if err := AppendEntry(tmp, ws, "T", "x.md", huge); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(IndexPath(tmp, ws))
	line := ""
	for l := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasPrefix(l, "- [T](x.md)") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("appended line not found")
	}
	if len(line) > MaxAppendHookBytes+50 {
		t.Errorf("line not truncated: len=%d %q", len(line), line)
	}
}

func TestAppendEntry_RequiresTitleAndPath(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	if err := AppendEntry(tmp, ws, "", "x.md", "h"); err == nil {
		t.Errorf("expected error on empty title")
	}
	if err := AppendEntry(tmp, ws, "T", "", "h"); err == nil {
		t.Errorf("expected error on empty relPath")
	}
}
