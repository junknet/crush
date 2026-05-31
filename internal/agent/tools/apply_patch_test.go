package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Parser tests
// ---------------------------------------------------------------------------

func TestParsePatchAddFile(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Add File: path/add.go\n" +
		"+package main\n" +
		"+\n" +
		"+func main() {}\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opAdd, p.ops[0].kind)
	require.Equal(t, "path/add.go", p.ops[0].path)
	require.Equal(t, "package main\n\nfunc main() {}\n", p.ops[0].addContent)
}

func TestParsePatchDeleteFile(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Delete File: gone.go\n*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opDelete, p.ops[0].kind)
	require.Equal(t, "gone.go", p.ops[0].path)
}

func TestParsePatchUpdateWithHeaderAndMove(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Update File: a.go\n" +
		"*** Move to: b.go\n" +
		"@@ func f()\n" +
		" ctx\n" +
		"-old\n" +
		"+new\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	op := p.ops[0]
	require.Equal(t, opUpdate, op.kind)
	require.Equal(t, "a.go", op.path)
	require.Equal(t, "b.go", op.movePath)
	require.Len(t, op.hunks, 1)
	require.Equal(t, "func f()", op.hunks[0].contextHeader)
	require.Equal(t, []string{"ctx", "old"}, op.hunks[0].oldLines)
	require.Equal(t, []string{"ctx", "new"}, op.hunks[0].newLines)
}

func TestParsePatchMultipleHunks(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Update File: a.go\n" +
		"@@\n" +
		" a\n" +
		"-b\n" +
		"+B\n" +
		"@@\n" +
		" x\n" +
		"-y\n" +
		"+Y\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Len(t, p.ops[0].hunks, 2)
	require.Equal(t, []string{"a", "b"}, p.ops[0].hunks[0].oldLines)
	require.Equal(t, []string{"x", "y"}, p.ops[0].hunks[1].oldLines)
}

func TestParsePatchMissingAtHeaderTolerance(t *testing.T) {
	t.Parallel()
	// No "@@" before the diff lines: must be treated as one implicit hunk.
	src := "*** Begin Patch\n" +
		"*** Update File: file.py\n" +
		" import foo\n" +
		"+bar\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Len(t, p.ops[0].hunks, 1)
	require.Equal(t, []string{"import foo"}, p.ops[0].hunks[0].oldLines)
	require.Equal(t, []string{"import foo", "bar"}, p.ops[0].hunks[0].newLines)
}

func TestParsePatchCodeFenceStrip(t *testing.T) {
	t.Parallel()
	src := "```patch\n" +
		"*** Begin Patch\n" +
		"*** Delete File: x.go\n" +
		"*** End Patch\n" +
		"```\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opDelete, p.ops[0].kind)
}

func TestParsePatchMultipleOps(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Add File: add.go\n" +
		"+hi\n" +
		"*** Delete File: del.go\n" +
		"*** Update File: up.go\n" +
		"@@\n" +
		"-old\n" +
		"+new\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 3)
	require.Equal(t, opAdd, p.ops[0].kind)
	require.Equal(t, opDelete, p.ops[1].kind)
	require.Equal(t, opUpdate, p.ops[2].kind)
}

func TestParsePatchEndOfFileMarker(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Update File: a.go\n" +
		"@@\n" +
		" last\n" +
		"+appended\n" +
		"*** End of File\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.True(t, p.ops[0].hunks[0].isEndOfFile)
}

func TestParsePatchErrorsMissingMarkers(t *testing.T) {
	t.Parallel()
	_, err := parsePatch("nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Begin Patch")

	_, err = parsePatch("*** Begin Patch\n*** Add File: x\n+hi\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "End Patch")
}

func TestParsePatchErrorsBadDiffLine(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Update File: a.go\n@@\nbadline\n*** End Patch\n"
	_, err := parsePatch(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid diff line")
}

func TestParsePatchErrorsEmptyUpdate(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Update File: a.go\n*** End Patch\n"
	_, err := parsePatch(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestParsePatchErrorsUnknownHeader(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Frobnicate File: a.go\n*** End Patch\n"
	_, err := parsePatch(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected one of")
}

// ---------------------------------------------------------------------------
// Applier tests (applyHunks)
// ---------------------------------------------------------------------------

func TestApplyHunksExactMatch(t *testing.T) {
	t.Parallel()
	content := "line 1\nline 2\nline 3\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n line 1\n-line 2\n+LINE 2\n line 3\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "line 1\nLINE 2\nline 3\n", out)
}

func TestApplyHunksRstripMatch(t *testing.T) {
	t.Parallel()
	// File has trailing whitespace; hunk context omits it. Tier (b) must match.
	content := "alpha   \nbeta\t\ngamma\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n alpha\n-beta\n+BETA\n gamma\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	// Context lines are NOT rewritten — only the removed/added lines change.
	// The matched window is spliced out and replaced wholesale by newLines,
	// so the context line that survives comes from newLines ("alpha" without
	// trailing ws) and ("gamma").
	require.Equal(t, "alpha\nBETA\ngamma\n", out)
}

func TestApplyHunksLeadingIndentTolerantTabVsSpace(t *testing.T) {
	t.Parallel()
	// File uses a tab; hunk uses four spaces. Tier (c) must match and reindent
	// the added line to the file's tab.
	content := "func f() {\n\treturn 1\n}\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n func f() {\n-    return 1\n+    return 2\n }\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "func f() {\n\treturn 2\n}\n", out)
}

func TestApplyHunksMultiHunkSequential(t *testing.T) {
	t.Parallel()
	content := "a\nb\nc\nd\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n a\n-b\n+B\n@@\n c\n-d\n+D\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "a\nB\nc\nD\n", out)
}

func TestApplyHunksHunkNotFound(t *testing.T) {
	t.Parallel()
	content := "a\nb\nc\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n-zzz\n+yyy\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestApplyHunksAmbiguousContextAborts(t *testing.T) {
	t.Parallel()
	// "x" appears twice; a single-line removal context is ambiguous.
	content := "x\nmid\nx\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n-x\n+X\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
}

func TestApplyHunksDisambiguatedByContext(t *testing.T) {
	t.Parallel()
	// Same "x" twice but extra context makes the second one unique.
	content := "x\nmid\nx\ntail\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n-x\n+X\n tail\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "x\nmid\nX\ntail\n", out)
}

func TestApplyHunksEndOfFileAppend(t *testing.T) {
	t.Parallel()
	content := "a\nb\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n+c\n*** End of File\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "a\nb\nc\n", out)
}

func TestApplyHunksPureInsertWithoutAnchorAborts(t *testing.T) {
	t.Parallel()
	content := "a\nb\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n+c\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pure insertion")
}

func TestApplyHunksContextLongerThanFileAborts(t *testing.T) {
	t.Parallel()
	content := "a\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n a\n-b\n-c\n+d\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Through-the-tool tests (real temp files, permission auto-granted)
// ---------------------------------------------------------------------------

func runApplyPatch(t *testing.T, workingDir, patch string) fantasy.ToolResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	tool := NewApplyPatchTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workingDir)
	input, err := json.Marshal(ApplyPatchParams{Patch: patch})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: ApplyPatchToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestApplyPatchToolUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644))

	patch := "*** Begin Patch\n" +
		"*** Update File: main.go\n" +
		"@@ func main()\n" +
		" func main() {\n" +
		"-\tprintln(\"hi\")\n" +
		"+\tprintln(\"bye\")\n" +
		" }\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "package main\n\nfunc main() {\n\tprintln(\"bye\")\n}\n", string(got))
}

func TestApplyPatchToolAddFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: sub/new.txt\n+hello\n+world\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	got, err := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello\nworld\n", string(got))
}

func TestApplyPatchToolAddFileAlreadyExistsAborts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "exists.txt")
	require.NoError(t, os.WriteFile(target, []byte("orig\n"), 0o644))

	patch := "*** Begin Patch\n*** Add File: exists.txt\n+new\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "already exists")

	// File must be untouched.
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "orig\n", string(got))
}

func TestApplyPatchToolDeleteFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "gone.txt")
	require.NoError(t, os.WriteFile(target, []byte("bye\n"), 0o644))

	patch := "*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	_, err := os.Stat(target)
	require.True(t, os.IsNotExist(err))
}

func TestApplyPatchToolMoveFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "old.txt")
	require.NoError(t, os.WriteFile(src, []byte("a\nb\nc\n"), 0o644))

	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n" +
		"*** Move to: nested/new.txt\n" +
		"@@\n a\n-b\n+B\n c\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	_, err := os.Stat(src)
	require.True(t, os.IsNotExist(err), "old path must be gone after move")

	got, err := os.ReadFile(filepath.Join(dir, "nested", "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "a\nB\nc\n", string(got))
}

func TestApplyPatchToolHunkNotFoundNoPartialWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	orig := "package main\n\nfunc main() {}\n"
	require.NoError(t, os.WriteFile(target, []byte(orig), 0o644))

	// First hunk applies, second cannot be located -> whole call must abort
	// and the file must be byte-for-byte unchanged.
	patch := "*** Begin Patch\n" +
		"*** Update File: main.go\n" +
		"@@\n-package main\n+package other\n" +
		"@@\n-DOES NOT EXIST\n+x\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not found")

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, orig, string(got), "file must be unchanged when any hunk fails")
}

func TestApplyPatchToolMultiFileAtomicAbort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	require.NoError(t, os.WriteFile(good, []byte("keep\n"), 0o644))

	// good.txt update is valid, but the second op deletes a missing file ->
	// the whole patch must abort with NO write to good.txt.
	patch := "*** Begin Patch\n" +
		"*** Update File: good.txt\n@@\n-keep\n+CHANGED\n" +
		"*** Delete File: missing.txt\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.True(t, resp.IsError)

	got, err := os.ReadFile(good)
	require.NoError(t, err)
	require.Equal(t, "keep\n", string(got), "first file must not be written when a later op fails")
}

func TestApplyPatchToolCRLFPreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "crlf.txt")
	require.NoError(t, os.WriteFile(target, []byte("a\r\nb\r\nc\r\n"), 0o644))

	patch := "*** Begin Patch\n*** Update File: crlf.txt\n@@\n a\n-b\n+B\n c\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "a\r\nB\r\nc\r\n", string(got))
}

// mustHunks parses a full patch envelope and returns the hunks of its single
// Update File op. Test helper for the applier-level tests.
func mustHunks(t *testing.T, src string) []patchHunk {
	t.Helper()
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opUpdate, p.ops[0].kind)
	return p.ops[0].hunks
}

// guard against accidental unused-import drift.
var _ = strings.TrimSpace

func TestApplyHunksCollapseWhitespace(t *testing.T) {
	t.Parallel()
	content := "func  Calculate(a,  b  int)  int  {\n\treturn  a  +  b\n}\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n func Calculate(a, b int) int {\n- return a + b\n+ return a * b\n }\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "func Calculate(a, b int) int {\n\treturn a * b\n}\n", out)
}

func TestApplyHunksFallbackReindent(t *testing.T) {
	t.Parallel()
	content := "class Demo:\n    def run(self):\n        pass\n"
	// Hunk context uses 2 spaces indentation (different from file's 4 spaces).
	// New line print('ok') uses 3 spaces (which cannot map strictly, triggering fallback).
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n class Demo:\n   def run(self):\n-    pass\n+   print('ok')\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	// fallback reindents 3 spaces into 4 spaces (1 unit level based on model unit 2 and file unit 4)
	require.Equal(t, "class Demo:\n    def run(self):\n    print('ok')\n", out)
}

func TestApplyPatchASTGuardJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "conf.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"name": "crush", "version": "1.0"}`), 0o644))

	// Invalid JSON patch: trailing comma
	badPatch := "*** Begin Patch\n*** Update File: conf.json\n@@\n-{\"name\": \"crush\", \"version\": \"1.0\"}\n+{\"name\": \"crush\", \"version\": \"1.0\",}\n*** End Patch\n"
	resp := runApplyPatch(t, dir, badPatch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "semantic AST guard")

	// Valid JSON patch
	goodPatch := "*** Begin Patch\n*** Update File: conf.json\n@@\n-{\"name\": \"crush\", \"version\": \"1.0\"}\n+{\"name\": \"crush\", \"version\": \"2.0\"}\n*** End Patch\n"
	resp = runApplyPatch(t, dir, goodPatch)
	require.False(t, resp.IsError, resp.Content)
}

func TestApplyPatchASTGuardYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "conf.yaml")
	require.NoError(t, os.WriteFile(target, []byte("settings:\n  enabled: true\n"), 0o644))

	// Invalid YAML patch: illegal indentation tab in YAML (some yaml parsers fail on this)
	badPatch := "*** Begin Patch\n*** Update File: conf.yaml\n@@\n settings:\n-  enabled: true\n+\t  enabled: true\n*** End Patch\n"
	resp := runApplyPatch(t, dir, badPatch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "semantic AST guard")
}

func TestApplyPatchDiagnostics(t *testing.T) {
	t.Parallel()
	content := "func ProcessData() {\n\tlog.Println(\"starting\")\n\tdoWork()\n}\n"
	// Hunk expects 'doWork(x)' which is slightly different from 'doWork()'
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n func ProcessData() {\n  log.Println(\"starting\")\n- doWork(x)\n+ doWork(y)\n }\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
	require.Contains(t, err.Error(), "context not found. We found a very similar section around line 1")
	require.Contains(t, err.Error(), "Mismatch reason: Content difference")
}

func TestApplyPatchWildcardForbidden(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: src/*.go\n+hello\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "contains wildcard characters")
}

func TestApplyPatchSniffingBypass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	
	// Test sniffing: extensionless file containing JSON format should trigger AST check
	jsonPatch := "*** Begin Patch\n*** Add File: config_no_ext\n+{\"name\": \"crush\",}\n*** End Patch\n"
	resp := runApplyPatch(t, dir, jsonPatch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "semantic AST guard") // triggers JSON verification due to sniff
	
	// Test bypass: custom unknown extension should safely bypass checking and write successfully
	customPatch := "*** Begin Patch\n*** Add File: config.custom\n+some random config\n*** End Patch\n"
	resp = runApplyPatch(t, dir, customPatch)
	require.False(t, resp.IsError, resp.Content)
	
	got, err := os.ReadFile(filepath.Join(dir, "config.custom"))
	require.NoError(t, err)
	require.Equal(t, "some random config\n", string(got))
}
