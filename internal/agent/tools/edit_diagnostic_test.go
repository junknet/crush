package tools

import (
	"strings"
	"testing"
)

func TestNotFoundDiagnostic_FuzzyMatchSurfacesExcerpt(t *testing.T) {
	content := `package foo

func Bar() {
	doStuff()
	doOther()
	return
}
`
	// old_string has wrong indentation (4 spaces instead of tab)
	oldString := "    doStuff()"
	got := notFoundDiagnostic(content, oldString, "foo.go")

	if !strings.Contains(got, "near line") {
		t.Errorf("missing line hint: %s", got)
	}
	if !strings.Contains(got, "doStuff") {
		t.Errorf("missing matched line in excerpt: %s", got)
	}
	if !strings.Contains(got, "→") {
		t.Errorf("excerpt should visualise tab whitespace: %s", got)
	}
}

func TestNotFoundDiagnostic_NoFuzzyHit(t *testing.T) {
	content := "the quick brown fox\njumps over\nthe lazy dog\n"
	got := notFoundDiagnostic(content, "totally unrelated text\n", "doc.txt")
	if !strings.Contains(got, "does not appear anywhere") {
		t.Errorf("expected wrong-file hint, got: %s", got)
	}
	if !strings.Contains(got, "doc.txt") {
		t.Errorf("expected file path in message, got: %s", got)
	}
}

func TestMultipleMatchesDiagnostic_ListsLineNumbers(t *testing.T) {
	content := "foo\nbar\nfoo\nbaz\nfoo\n"
	got := multipleMatchesDiagnostic(content, "foo")
	if !strings.Contains(got, "line 1") || !strings.Contains(got, "line 3") || !strings.Contains(got, "line 5") {
		t.Errorf("expected three line numbers, got: %s", got)
	}
}

func TestVisualizeWhitespace_OnlyLeadingMarkers(t *testing.T) {
	got := visualizeWhitespace("\t  hello world  ")
	// Leading tab → "→", leading two spaces → "··", inner spaces and trailing
	// spaces stay verbatim, end marked with ¶.
	if got != "→··hello world  ¶" {
		t.Errorf("unexpected visualisation: %q", got)
	}
}
