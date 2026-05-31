package tools

import (
	"path/filepath"
	"testing"
)

func TestResolveSearchPathRelativeToWorkingDir(t *testing.T) {
	workingDir := filepath.Join(string(filepath.Separator), "tmp", "work")

	if got := resolveSearchPath("", workingDir, ""); got != workingDir {
		t.Fatalf("empty path = %q, want %q", got, workingDir)
	}
	if got := resolveSearchPath("", workingDir, "."); got != workingDir {
		t.Fatalf("dot path = %q, want %q", got, workingDir)
	}
	if got := resolveSearchPath("", workingDir, "pkg"); got != filepath.Join(workingDir, "pkg") {
		t.Fatalf("relative path = %q, want %q", got, filepath.Join(workingDir, "pkg"))
	}
	if got := resolveSearchPath("/remote/root", workingDir, "."); got != "/remote/root" {
		t.Fatalf("remote dot path = %q, want /remote/root", got)
	}
}

func TestGlobToRegex(t *testing.T) {
	tests := []struct {
		glob string
		want string
	}{
		{"*.go", "^[^/]*\\.go$"},
		{"**/*.ts", "^.*[^/]*\\.ts$"},
		{"src/?", "^src/[^/]$"},
	}
	for _, tt := range tests {
		got := globToRegex(tt.glob)
		if got != tt.want {
			t.Errorf("globToRegex(%q) = %q, want %q", tt.glob, got, tt.want)
		}
	}
}
