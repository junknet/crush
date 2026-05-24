package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/memdir"
	"github.com/charmbracelet/crush/internal/message"
)

func TestBackupDiscardedMessages_WritesArchiveAndIndex(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	a := &sessionAgent{
		dataDir:    tmp,
		workingDir: ws,
	}

	discarded := []message.Message{
		{ID: "m1", Role: message.User, SessionID: "sess-abc", Parts: []message.ContentPart{message.TextContent{Text: "first user prompt body"}}},
		{ID: "m2", Role: message.Assistant, SessionID: "sess-abc", Parts: []message.ContentPart{message.TextContent{Text: "first assistant reply"}}},
		{ID: "m3", Role: message.User, SessionID: "sess-abc", Parts: []message.ContentPart{message.TextContent{Text: "follow-up"}}},
	}

	a.backupDiscardedMessages(context.Background(), "sess-abc", discarded, ws)

	// memory/sessions/ should have exactly one session-*.md file.
	sessionsDir := filepath.Join(tmp, "projects", memdir.WorkspaceSlug(ws), "memory", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "session-") {
		t.Fatalf("expected one session-*.md, got %v", entries)
	}

	raw, err := os.ReadFile(filepath.Join(sessionsDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, want := range []string{
		"first user prompt body",
		"first assistant reply",
		"follow-up",
		"Session ID: sess-abc",
		"Messages: 3",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("archive missing %q in:\n%s", want, content)
		}
	}

	// Frontmatter must decode cleanly with type=project.
	fm, _, err := memdir.DecodeFrontmatter(content)
	if err != nil {
		t.Fatalf("decode archive frontmatter: %v", err)
	}
	if fm.Type != memdir.MemoryProject {
		t.Errorf("expected type=project, got %s", fm.Type)
	}

	// MEMORY.md should have one bullet pointing at sessions/<file>.
	indexRaw, err := os.ReadFile(memdir.IndexPath(tmp, ws))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(indexRaw), "Session backup ") {
		t.Errorf("MEMORY.md index missing backup line:\n%s", indexRaw)
	}
	if !strings.Contains(string(indexRaw), "sessions/"+entries[0].Name()) {
		t.Errorf("MEMORY.md index missing rel path:\n%s", indexRaw)
	}
}

func TestBackupDiscardedMessages_EmptyNoop(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	a := &sessionAgent{dataDir: tmp, workingDir: ws}
	a.backupDiscardedMessages(context.Background(), "sess", nil, ws)
	// No memory dir should be created on a no-op.
	if _, err := os.Stat(filepath.Join(tmp, "projects")); err == nil {
		t.Errorf("empty input should not touch disk")
	}
}

func TestBackupDiscardedMessages_TruncatesLargeBody(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	a := &sessionAgent{dataDir: tmp, workingDir: ws}
	huge := strings.Repeat("x", 5000)
	discarded := []message.Message{
		{ID: "m1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: huge}}},
	}
	a.backupDiscardedMessages(context.Background(), "sess", discarded, ws)

	sessionsDir := filepath.Join(tmp, "projects", memdir.WorkspaceSlug(ws), "memory", "sessions")
	entries, _ := os.ReadDir(sessionsDir)
	if len(entries) != 1 {
		t.Fatal("missing archive")
	}
	raw, _ := os.ReadFile(filepath.Join(sessionsDir, entries[0].Name()))
	if !strings.Contains(string(raw), "[truncated]") {
		t.Errorf("expected truncation marker for >2KB body")
	}
}
