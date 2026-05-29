package tools

import (
	"slices"
	"testing"
)

// TestGrepArgvTranspile locks the grep-params → ripgrep/grep argv mapping: the
// model speaks grep semantics, we emit rg flags when available and fall back to
// grep otherwise.
func TestGrepArgvTranspile(t *testing.T) {
	t.Run("rg content", func(t *testing.T) {
		argv, parseRg := grepArgv(GrepParams{Pattern: "foo", IgnoreCase: true, Glob: "*.go"}, true, "/bin/rg", "/src")
		if !parseRg {
			t.Fatal("expected rg JSON parsing")
		}
		want := []string{"/bin/rg", "--json", "-H", "-n", "-i", "--glob", "*.go", "--", "foo", "/src"}
		if !slices.Equal(argv, want) {
			t.Fatalf("argv = %v\nwant %v", argv, want)
		}
	})
	t.Run("rg files_only + literal + no_ignore", func(t *testing.T) {
		argv, parseRg := grepArgv(GrepParams{Pattern: "a.b", Literal: true, FilesOnly: true, NoIgnore: true}, true, "rg", "/src")
		if parseRg {
			t.Fatal("files_only must not use JSON parsing")
		}
		want := []string{"rg", "-l", "-F", "--no-ignore", "--hidden", "--", "a.b", "/src"}
		if !slices.Equal(argv, want) {
			t.Fatalf("argv = %v\nwant %v", argv, want)
		}
	})
	t.Run("grep fallback", func(t *testing.T) {
		argv, parseRg := grepArgv(GrepParams{Pattern: "foo", IgnoreCase: true, Glob: "*.go"}, false, "", "/src")
		if parseRg {
			t.Fatal("grep fallback must not use JSON parsing")
		}
		want := []string{"grep", "-rn", "--color=never", "-i", "--include=*.go", "-e", "foo", "/src"}
		if !slices.Equal(argv, want) {
			t.Fatalf("argv = %v\nwant %v", argv, want)
		}
	})
}

// TestFindArgvTranspile locks the find-params → fd/find argv mapping.
func TestFindArgvTranspile(t *testing.T) {
	t.Run("fd", func(t *testing.T) {
		argv := findArgv(FindParams{Name: "*.go", Type: "f", MaxDepth: 3}, true, "fd", "/src")
		want := []string{"fd", "--color", "never", "-t", "f", "-d", "3", "-g", "*.go", ".", "/src"}
		if !slices.Equal(argv, want) {
			t.Fatalf("argv = %v\nwant %v", argv, want)
		}
	})
	t.Run("find fallback", func(t *testing.T) {
		argv := findArgv(FindParams{Name: "*.go", Type: "d", MaxDepth: 2}, false, "", "/src")
		want := []string{"find", "/src", "-maxdepth", "2", "-type", "d", "-name", "*.go"}
		if !slices.Equal(argv, want) {
			t.Fatalf("argv = %v\nwant %v", argv, want)
		}
	})
}
