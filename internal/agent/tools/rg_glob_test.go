package tools

import (
	"regexp"
	"testing"
)

func TestGlobToRegex(t *testing.T) {
	cases := []struct {
		glob    string
		matches []string // matched against basename (no / in glob) or relpath
		rejects []string
	}{
		{
			glob:    "*.go",
			matches: []string{"main.go", "rg.go", "agent.go"},
			rejects: []string{"main.ts", "main.go.bak"},
		},
		{
			glob: "**/*.go",
			// **/*.go has / so it matches relative paths
			matches: []string{"main.go", "src/main.go", "internal/agent/tools/rg.go"},
			rejects: []string{"main.ts", "src/main.ts"},
		},
		{
			glob:    "*.{ts,tsx}",
			// {ts,tsx} is not a glob wildcard — it stays as literal (no * or ?)
			// so isGlobPattern returns false and it goes through as regex.
			// This tests that we do NOT mis-classify it.
		},
		{
			glob:    "impl_*.nim",
			matches: []string{"impl_foo.nim", "impl_bar.nim"},
			rejects: []string{"impl_foo.go", "foo.nim"},
		},
		{
			glob: "src/*.go",
			// src/*.go has / so match against relative path
			matches: []string{"src/main.go", "src/rg.go"},
			rejects: []string{"main.go", "src/sub/main.go"},
		},
	}

	for _, tc := range cases {
		if !isGlobPattern(tc.glob) {
			continue // skip non-glob patterns in this test
		}
		rxStr := globToRegex(tc.glob)
		rx, err := regexp.Compile(rxStr)
		if err != nil {
			t.Fatalf("globToRegex(%q) produced invalid regex %q: %v", tc.glob, rxStr, err)
		}
		for _, m := range tc.matches {
			if !rx.MatchString(m) {
				t.Errorf("glob %q regex %q: expected match for %q", tc.glob, rxStr, m)
			}
		}
		for _, r := range tc.rejects {
			if rx.MatchString(r) {
				t.Errorf("glob %q regex %q: expected NO match for %q", tc.glob, rxStr, r)
			}
		}
	}
}

func TestIsGlobPattern(t *testing.T) {
	globs := []string{"*.go", "**/*.ts", "impl_*.nim", "src/?.go"}
	nonGlobs := []string{`\.go$`, "main.go", `impl_.*\.nim`, "*.{ts,tsx}"}

	for _, g := range globs {
		if !isGlobPattern(g) {
			t.Errorf("isGlobPattern(%q): expected true", g)
		}
	}
	for _, g := range nonGlobs {
		if isGlobPattern(g) {
			t.Errorf("isGlobPattern(%q): expected false", g)
		}
	}
}

func TestSanitizeGlobInclude(t *testing.T) {
	cases := [][2]string{
		{"./src/*.go", "src/*.go"},
		{"/src/*.go", "src/*.go"},
		{"*.go", "*.go"},
		{"src/*.go", "src/*.go"},
	}
	for _, c := range cases {
		got := sanitizeGlobInclude(c[0])
		if got != c[1] {
			t.Errorf("sanitizeGlobInclude(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestResolveFilePatternGlob(t *testing.T) {
	// Glob inputs should resolve without error.
	globs := []string{"*.go", "**/*.nim", "impl_*.ts"}
	for _, g := range globs {
		p, err := resolveFilePattern(g)
		if err != nil {
			t.Errorf("resolveFilePattern(%q) error: %v", g, err)
		}
		if _, err := regexp.Compile(p); err != nil {
			t.Errorf("resolveFilePattern(%q) produced invalid regex %q", g, p)
		}
	}
}

func TestResolveFilePatternRegex(t *testing.T) {
	// Valid regex inputs should pass through unchanged.
	p, err := resolveFilePattern(`\.go$`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != `\.go$` {
		t.Errorf("expected passthrough, got %q", p)
	}

	// Invalid regex should return error.
	_, err = resolveFilePattern(`[unclosed`)
	if err == nil {
		t.Error("expected error for invalid regex, got nil")
	}
}
