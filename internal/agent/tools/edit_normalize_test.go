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

// --- Leading-indentation-tolerant fallback ---------------------------------

func TestResolveLeadingIndent_TabVsSpaces(t *testing.T) {
	// File uses a single tab; model emitted 4 spaces.
	content := "func f() {\n\treturn 1\n}\n"
	old := "func f() {\n    return 1\n}"
	newStr := "func f() {\n    return 2\n}"
	gotOld, gotNew, changed := resolveOldString(content, old, newStr)
	if !changed {
		t.Fatalf("expected indent fallback to fire (change=true)")
	}
	wantOld := "func f() {\n\treturn 1\n}"
	if gotOld != wantOld {
		t.Errorf("old should be file's actual bytes\n got  %q\n want %q", gotOld, wantOld)
	}
	wantNew := "func f() {\n\treturn 2\n}"
	if gotNew != wantNew {
		t.Errorf("new should be reindented to tab\n got  %q\n want %q", gotNew, wantNew)
	}
	// Returned old must be a real substring of content.
	if !strings.Contains(content, gotOld) {
		t.Errorf("returned old not a substring of content: %q", gotOld)
	}
}

func TestResolveLeadingIndent_DifferentSpaceCounts(t *testing.T) {
	// Single-line block: file indents with 2 spaces; model used 4. The
	// matched line itself is the anchor, so the 4->2 rewrite is captured and
	// applied to new_string.
	content := "func wrap() {\n  y := 1\n}\n"
	old := "    y := 1"
	newStr := "    y := 2"
	gotOld, gotNew, changed := resolveOldString(content, old, newStr)
	if !changed {
		t.Fatalf("expected change=true")
	}
	if gotOld != "  y := 1" {
		t.Errorf("old mismatch: %q", gotOld)
	}
	if gotNew != "  y := 2" {
		t.Errorf("new should adopt 2-space indent: %q", gotNew)
	}
}

func TestResolveLeadingIndent_MultiLineBlockReindent(t *testing.T) {
	// File block is tab-indented with a nested level; model used 4/8 spaces.
	content := "func g() {\n\tif ok {\n\t\tdo()\n\t}\n}\n"
	old := "func g() {\n    if ok {\n        do()\n    }\n}"
	newStr := "func g() {\n    if ok {\n        doMore()\n    }\n}"
	gotOld, gotNew, changed := resolveOldString(content, old, newStr)
	if !changed {
		t.Fatalf("expected change=true")
	}
	wantOld := "func g() {\n\tif ok {\n\t\tdo()\n\t}\n}"
	if gotOld != wantOld {
		t.Errorf("old mismatch\n got  %q\n want %q", gotOld, wantOld)
	}
	// Indent map observed over matched lines: ""->"", "    "->"\t",
	// "        "->"\t\t". new_string lines reuse exactly those keys, so each
	// is reindented to the file's tab convention.
	wantNew := "func g() {\n\tif ok {\n\t\tdoMore()\n\t}\n}"
	if gotNew != wantNew {
		t.Errorf("new mismatch\n got  %q\n want %q", gotNew, wantNew)
	}
}

func TestResolveLeadingIndent_MultiLineWithNonEmptyAnchor(t *testing.T) {
	// Whole block is indented; first matched line has a non-empty anchor so
	// the anchor rewrite actually transforms new_string indentation.
	// File: 8 spaces base + 12 spaces nested (4-space step).
	content := "        a = 1\n            b = 2\n"
	// Model: 4-space base + 8-space nested (same 4-space relative step).
	old := "    a = 1\n        b = 2"
	newStr := "    a = 1\n        c = 3"
	gotOld, gotNew, changed := resolveOldString(content, old, newStr)
	if !changed {
		t.Fatalf("expected change=true")
	}
	if gotOld != "        a = 1\n            b = 2" {
		t.Errorf("old mismatch: %q", gotOld)
	}
	// oldAnchor="    " (4), fileAnchor="        " (8). rel for line2 old is
	// "    " (4 more), file line2 is "            " (12 = 8 + 4). Consistent.
	// new line1 "    a"->"        a"; new line2 "        c"->"            c".
	if gotNew != "        a = 1\n            c = 3" {
		t.Errorf("new mismatch: %q", gotNew)
	}
}

func TestResolveLeadingIndent_UniquenessRejection(t *testing.T) {
	// The same body block appears twice (different indents). Must refuse.
	content := "block:\n  x = 1\nother\nblock:\n    x = 1\n"
	old := "block:\n        x = 1"
	newStr := "block:\n        x = 2"
	gotOld, gotNew, changed := resolveOldString(content, old, newStr)
	if changed {
		t.Fatalf("expected refusal on ambiguous (>=2) windows, got change=true old=%q new=%q", gotOld, gotNew)
	}
	if gotOld != old || gotNew != newStr {
		t.Errorf("on refusal must echo inputs, got old=%q new=%q", gotOld, gotNew)
	}
}

func TestResolveLeadingIndent_GenuinelyAbsent(t *testing.T) {
	content := "totally unrelated\ncontent here\n"
	old := "    func missing() {}"
	newStr := "    func missing() { return }"
	gotOld, gotNew, changed := resolveOldString(content, old, newStr)
	if changed {
		t.Fatalf("absent string must not match, got change=true")
	}
	if gotOld != old || gotNew != newStr {
		t.Errorf("must echo inputs on miss: old=%q new=%q", gotOld, gotNew)
	}
}

func TestResolveLeadingIndent_InteriorWhitespaceStillFails(t *testing.T) {
	// Only LEADING indentation is tolerated. Interior whitespace differs
	// (file: "a = 1", old: "a  =  1") -> must NOT match.
	content := "if x:\n\ta = 1\n"
	old := "if x:\n    a  =  1"
	newStr := "if x:\n    a  =  2"
	_, _, changed := resolveOldString(content, old, newStr)
	if changed {
		t.Fatalf("interior whitespace difference must not match")
	}
}

func TestResolveLeadingIndent_TrailingWhitespaceStillFails(t *testing.T) {
	// Trailing whitespace must match exactly too. File line has no trailing
	// space; old line has one.
	content := "if x:\n\ta = 1\n"
	old := "if x:\n    a = 1 "
	newStr := "if x:\n    a = 2 "
	_, _, changed := resolveOldString(content, old, newStr)
	if changed {
		t.Fatalf("trailing whitespace difference must not match")
	}
}

func TestResolveLeadingIndent_ExactStillTakesFastPath(t *testing.T) {
	// An exact match must NOT route through the indent fallback; changed must
	// be false (fast path) even though indentation is involved.
	content := "func f() {\n\treturn 1\n}\n"
	old := "func f() {\n\treturn 1\n}"
	newStr := "func f() {\n\treturn 2\n}"
	gotOld, gotNew, changed := resolveOldString(content, old, newStr)
	if changed {
		t.Fatalf("exact match must take fast path (change=false)")
	}
	if gotOld != old || gotNew != newStr {
		t.Errorf("fast path must echo inputs: old=%q new=%q", gotOld, gotNew)
	}
}

func TestResolveLeadingIndent_InconsistentTransformAborts(t *testing.T) {
	// Two non-blank lines at the SAME model indent ("    ") but DIFFERENT
	// file indents (tab vs 2 spaces) imply conflicting anchor rewrites.
	// Anchor from line1: old "    " -> file "\t". line2 old "    " rel "" then
	// expects file "\t" but file has "  " -> abort.
	content := "x:\n\ta\n  b\n"
	old := "x:\n    a\n    b"
	newStr := "x:\n    a\n    b2"
	_, _, changed := resolveOldString(content, old, newStr)
	if changed {
		t.Fatalf("inconsistent indent transform must abort (change=false)")
	}
}

func TestResolveLeadingIndent_UnreindentableNewAborts(t *testing.T) {
	// Match succeeds (anchor rewrite "    " -> "\t"), but a non-blank
	// new_string line's indent does NOT begin with oldAnchor ("  shifted" has
	// only 2 spaces, less than the 4-space anchor) -> cannot reindent -> abort.
	content := "fn:\n\tkeep\n"
	old := "fn:\n    keep"
	newStr := "fn:\n  shifted"
	_, _, changed := resolveOldString(content, old, newStr)
	if changed {
		t.Fatalf("new_string line not reindentable under anchor must abort")
	}
}
