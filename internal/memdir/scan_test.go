package memdir

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanMemoryFilesAndManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	workspace := filepath.Join(dir, "repo")
	path, err := WriteMemoryFile(dir, workspace, Frontmatter{
		Name:        "Build Commands",
		Description: "Go test and task build workflow",
		Type:        MemoryProject,
	}, "Run `go test ./...` before handoff.")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendEntry(dir, workspace, "Build Commands", filepath.Base(path), "test workflow"); err != nil {
		t.Fatal(err)
	}

	headers, err := ScanMemoryFiles(context.Background(), dir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 1 {
		t.Fatalf("expected one header, got %d", len(headers))
	}
	if headers[0].Filename != filepath.Base(path) {
		t.Fatalf("unexpected filename: %s", headers[0].Filename)
	}

	manifest := FormatMemoryManifest(headers)
	if !strings.Contains(manifest, "build-commands") || !strings.Contains(manifest, "project") {
		t.Fatalf("manifest missing fields: %s", manifest)
	}
}

func TestFindRelevantMemories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	workspace := filepath.Join(dir, "repo")
	buildPath, err := WriteMemoryFile(dir, workspace, Frontmatter{
		Name:        "Build Commands",
		Description: "go test task build",
		Type:        MemoryProject,
	}, "Use task build.")
	if err != nil {
		t.Fatal(err)
	}
	_, err = WriteMemoryFile(dir, workspace, Frontmatter{
		Name:        "Editor Preference",
		Description: "vim keybinding",
		Type:        MemoryUser,
	}, "User likes modal editing.")
	if err != nil {
		t.Fatal(err)
	}

	got, err := FindRelevantMemories(context.Background(), dir, workspace, "how do I run go test build", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected relevant memory")
	}
	if got[0].Header.Path != buildPath {
		t.Fatalf("expected build memory first, got %s", got[0].Header.Path)
	}

	already := map[string]struct{}{filepath.Clean(buildPath): {}}
	got, err = FindRelevantMemories(context.Background(), dir, workspace, "go test build", already)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected already surfaced memory to be skipped, got %d", len(got))
	}
}

func TestFindRelevantMemoriesChineseQuery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	workspace := filepath.Join(dir, "repo")
	_, err := WriteMemoryFile(dir, workspace, Frontmatter{
		Name:        "context-window",
		Description: "窗口上下文消耗和自动压缩",
		Type:        MemoryProject,
	}, "底栏显示 context window 消耗。")
	if err != nil {
		t.Fatal(err)
	}

	got, err := FindRelevantMemories(context.Background(), dir, workspace, "窗口上下文llm的消耗在哪里呢", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected Chinese query to match CJK memory")
	}
}

func TestReadMemoryForRecallTruncates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "memory.md")
	var b strings.Builder
	for range MaxRecallLines + 10 {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMemoryForRecall(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Memory truncated") {
		t.Fatalf("expected truncation notice, got %q", got)
	}
}
