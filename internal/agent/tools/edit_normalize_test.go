package tools

import (
	"strings"
	"testing"
)

func TestNormalizeQuotes_FoldsCurlyToStraight(t *testing.T) {
	in := "she said “hi” and ‘bye’"
	got := normalizeQuotes(in)
	want := `she said "hi" and 'bye'`
	if got != want {
		t.Errorf("normalize mismatch:\n got  %q\n want %q", got, want)
	}
}

func TestNormalizeQuotes_Passthrough(t *testing.T) {
	in := "plain ascii, no curly"
	if got := normalizeQuotes(in); got != in {
		t.Errorf("expected passthrough, got %q", got)
	}
}

func TestFindActualString_QuoteFallback(t *testing.T) {
	content := `var msg = "hello world"` + "\n"
	// LLM emits straight quotes; file already uses straight quotes →
	// exact match.
	if got := findActualString(content, `"hello world"`); got != `"hello world"` {
		t.Errorf("exact match should win, got %q", got)
	}

	// File uses curly quotes; LLM emits straight → must fall back to
	// quote-normalised match and return the file's actual curly form.
	curlyFile := "var msg = “hello world”\n"
	got := findActualString(curlyFile, `"hello world"`)
	want := "“hello world”"
	if got != want {
		t.Errorf("curly fallback failed:\n got  %q\n want %q", got, want)
	}
}

func TestFindActualString_NoMatch(t *testing.T) {
	if got := findActualString("totally different text", "missing fragment"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestPreserveQuoteStyle_AppliesFileTypography(t *testing.T) {
	orig := `"hi"`
	actual := "“hi”"
	newStr := `"bye"`
	got := preserveQuoteStyle(orig, actual, newStr)
	if got != "“bye”" {
		t.Errorf("expected file-style curly quotes, got %q", got)
	}
}

func TestPreserveQuoteStyle_KeepsContractions(t *testing.T) {
	// File uses curly singles around a contraction-like apostrophe; we
	// don't want the inner ' in "don't" to become a curly OPEN quote.
	orig := "'go'"
	actual := "‘go’"
	newStr := "don't 'go'"
	got := preserveQuoteStyle(orig, actual, newStr)
	// Contraction apostrophe must use right-single-curly; outer quotes
	// must open/close correctly.
	if !strings.Contains(got, "don’t") {
		t.Errorf("contraction not preserved, got %q", got)
	}
	if !strings.Contains(got, "‘go’") {
		t.Errorf("outer quotes wrong, got %q", got)
	}
}

func TestStripTrailingWhitespace(t *testing.T) {
	in := "a   \nb\t\nc\n"
	got := stripTrailingWhitespace(in)
	if got != "a\nb\nc\n" {
		t.Errorf("LF strip failed, got %q", got)
	}
}

func TestStripTrailingWhitespace_PreservesCRLF(t *testing.T) {
	in := "alpha  \r\nbeta\t\r\n"
	got := stripTrailingWhitespace(in)
	if got != "alpha\r\nbeta\r\n" {
		t.Errorf("CRLF preserve failed, got %q", got)
	}
}

func TestShouldStripTrailingWhitespace(t *testing.T) {
	for _, c := range []struct {
		path string
		want bool
	}{
		{"foo.go", true},
		{"a.md", false},
		{"b.MD", false},
		{"c.mdx", false},
		{"d.markdown", false},
		{"prose.txt", true},
		{"no_ext", true},
	} {
		if got := shouldStripTrailingWhitespace(c.path); got != c.want {
			t.Errorf("path=%s got=%v want=%v", c.path, got, c.want)
		}
	}
}

func TestDesanitizeMatchString_ExpandsFnr(t *testing.T) {
	in := "see <fnr>x</fnr> and <n>y</n>"
	got, applied := desanitizeMatchString(in)
	if !strings.Contains(got, "<function_results>x</function_results>") {
		t.Errorf("fnr not expanded: %q", got)
	}
	if !strings.Contains(got, "<name>y</name>") {
		t.Errorf("n not expanded: %q", got)
	}
	if len(applied) == 0 {
		t.Errorf("expected applied substitutions list to be non-empty")
	}
}

func TestResolveOldString_ExactWins(t *testing.T) {
	old, new, changed := resolveOldString("a literal match here", "literal", "rewritten")
	if changed {
		t.Errorf("exact match should not report change")
	}
	if old != "literal" || new != "rewritten" {
		t.Errorf("unexpected rewrite: old=%q new=%q", old, new)
	}
}

func TestResolveOldString_QuoteFallback(t *testing.T) {
	content := "say “hi” to her"
	old, newStr, changed := resolveOldString(content, `"hi"`, `"bye"`)
	if !changed {
		t.Fatalf("expected change=true")
	}
	if old != "“hi”" {
		t.Errorf("old should be file-actual curly, got %q", old)
	}
	if newStr != "“bye”" {
		t.Errorf("new should be re-curlied, got %q", newStr)
	}
}

func TestResolveOldString_Desanitize(t *testing.T) {
	content := "wrap in <function_results>OUT</function_results> here"
	old, newStr, changed := resolveOldString(content, "<fnr>OUT</fnr>", "<fnr>NEW</fnr>")
	if !changed {
		t.Fatalf("expected change=true")
	}
	if old != "<function_results>OUT</function_results>" {
		t.Errorf("desanitize old failed: %q", old)
	}
	if newStr != "<function_results>NEW</function_results>" {
		t.Errorf("desanitize new failed: %q", newStr)
	}
}
