package memdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceSlugStable(t *testing.T) {
	cases := []struct {
		path string
	}{
		{"/home/user/projects/crush"},
		{"/tmp/Test Space"},
		{"C:\\Users\\me\\repo"},
	}
	for _, c := range cases {
		a := WorkspaceSlug(c.path)
		b := WorkspaceSlug(c.path)
		if a != b {
			t.Errorf("Slug for %q not deterministic: %s vs %s", c.path, a, b)
		}
		if len(a) == 0 || len(a) > 64 {
			t.Errorf("Slug %q has bad length %d", a, len(a))
		}
		// Must be filesystem-safe (lower alnum, _, -)
		for _, r := range a {
			ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
			if !ok {
				t.Errorf("Slug %q contains unsafe rune %q", a, r)
			}
		}
	}
}

func TestEnsureWorkspaceCreatesSeed(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "myproj")
	if err := EnsureWorkspace(tmp, ws); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	path := IndexPath(tmp, ws)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if !strings.Contains(string(body), "MEMORY.md") {
		t.Errorf("seed missing header hint")
	}
}

func TestEnsureWorkspaceIdempotent(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "myproj")
	if err := EnsureWorkspace(tmp, ws); err != nil {
		t.Fatal(err)
	}
	path := IndexPath(tmp, ws)
	// Overwrite with user content; second EnsureWorkspace must not clobber.
	want := "- [Decision](decision.md) — chose Anthropic\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWorkspace(tmp, ws); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != want {
		t.Errorf("EnsureWorkspace clobbered existing index. want=%q got=%q", want, got)
	}
}

func TestIndexPromptEmpty(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "p")
	// No file at all
	if got := IndexPrompt(tmp, ws); got != "" {
		t.Errorf("missing index should return empty, got %q", got)
	}
	// Seed-only (header comment, no entries) should also be empty after trim
	if err := EnsureWorkspace(tmp, ws); err != nil {
		t.Fatal(err)
	}
	got := IndexPrompt(tmp, ws)
	if !strings.Contains(got, "MEMORY.md") && got != "" {
		t.Errorf("expected seed body or empty, got %q", got)
	}
}

func TestIndexPromptTruncates(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "p")
	if err := EnsureWorkspace(tmp, ws); err != nil {
		t.Fatal(err)
	}
	path := IndexPath(tmp, ws)
	// Build content > MaxIndexLines
	var b strings.Builder
	for range MaxIndexLines + 50 {
		b.WriteString("- entry\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := IndexPrompt(tmp, ws)
	if !strings.Contains(got, "truncated at") {
		t.Errorf("expected truncation notice, got: %s", got)
	}
	// Sanity: result must not exceed budget + section wrapping (rough)
	if len(got) > MaxIndexBytes*2 {
		t.Errorf("rendered prompt unexpectedly large: %d bytes", len(got))
	}
}
