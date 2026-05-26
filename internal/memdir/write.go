// Package memdir's write-path. Read-path stays in memdir.go; everything
// here exists to let the extractMemories background agent and the
// summarize-session backup path persist new entries without re-implementing
// the same path/slug/frontmatter dance.
package memdir

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// MemoryType is one of four canonical memory categories. The set mirrors
// the user's global auto-memory taxonomy so extractMemories output and
// hand-written entries share a vocabulary the brain prompt can rely on.
type MemoryType string

const (
	MemoryUser      MemoryType = "user"
	MemoryFeedback  MemoryType = "feedback"
	MemoryProject   MemoryType = "project"
	MemoryReference MemoryType = "reference"
)

// Valid reports whether t is one of the four canonical categories.
func (t MemoryType) Valid() bool {
	switch t {
	case MemoryUser, MemoryFeedback, MemoryProject, MemoryReference:
		return true
	}
	return false
}

// Frontmatter is the minimal YAML header carried by each memory file.
// We deliberately resist a richer schema — the index in MEMORY.md is the
// search surface, the body is the long-form content, and Type is the only
// classification the brain actually reasons about.
type Frontmatter struct {
	Name        string
	Description string
	Type        MemoryType
}

var (
	slugReplace = regexp.MustCompile(`[^a-z0-9-]+`)
	appendMu    sync.Mutex // serialises MEMORY.md appends within this process
)

// MaxAppendHookBytes caps the trailing "— hook" snippet so a runaway
// description can't push a single index line past readable size.
const MaxAppendHookBytes = 120

func slugifyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugReplace.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// WriteMemoryFile writes one memory entry under
// <dataDir>/projects/<slug>/memory/<name>-<sha1(body)[:8]>.md. Same fm+body
// yields the same path and overwrites idempotently — callers that want a
// new file for an updated body must change the body. Frontmatter type must
// be one of the four canonical categories or the call returns an error
// without touching disk.
func WriteMemoryFile(dataDir, workspacePath string, fm Frontmatter, body string) (string, error) {
	if dataDir == "" {
		return "", errors.New("memdir.WriteMemoryFile: empty dataDir")
	}
	if !fm.Type.Valid() {
		return "", fmt.Errorf("memdir.WriteMemoryFile: invalid type %q", string(fm.Type))
	}
	name := slugifyName(fm.Name)
	if name == "" {
		return "", errors.New("memdir.WriteMemoryFile: empty name after sanitization")
	}
	fm.Name = name
	if err := EnsureWorkspace(dataDir, workspacePath); err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, "projects", WorkspaceSlug(workspacePath), "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(body))
	fileName := fmt.Sprintf("%s-%s.md", name, hex.EncodeToString(sum[:4]))
	path := filepath.Join(dir, fileName)
	content := EncodeFrontmatter(fm) + strings.TrimRight(body, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// AppendEntry adds one bullet line to MEMORY.md:
//
//   - [title](relPath) — hook
//
// Concurrent callers within this process are serialised by an in-process
// mutex; the file is opened with O_APPEND so external writers are still
// tolerated without losing data. Title and relPath are required; hook is
// truncated to MaxAppendHookBytes.
func AppendEntry(dataDir, workspacePath, title, relPath, hook string) error {
	if dataDir == "" {
		return errors.New("memdir.AppendEntry: empty dataDir")
	}
	title = strings.TrimSpace(strings.ReplaceAll(title, "\n", " "))
	hook = strings.TrimSpace(strings.ReplaceAll(hook, "\n", " "))
	if title == "" || relPath == "" {
		return errors.New("memdir.AppendEntry: title and relPath required")
	}
	if len(hook) > MaxAppendHookBytes {
		hook = hook[:MaxAppendHookBytes]
	}
	if err := EnsureWorkspace(dataDir, workspacePath); err != nil {
		return err
	}
	path := IndexPath(dataDir, workspacePath)
	appendMu.Lock()
	defer appendMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := fmt.Sprintf("- [%s](%s) — %s\n", title, relPath, hook)
	_, err = f.WriteString(line)
	return err
}

// EncodeFrontmatter renders the minimal YAML header followed by a blank
// line. Output is round-trip stable with DecodeFrontmatter.
func EncodeFrontmatter(fm Frontmatter) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: ")
	b.WriteString(fm.Name)
	b.WriteString("\n")
	b.WriteString("description: ")
	b.WriteString(strings.ReplaceAll(fm.Description, "\n", " "))
	b.WriteString("\n")
	b.WriteString("metadata:\n  type: ")
	b.WriteString(string(fm.Type))
	b.WriteString("\n---\n\n")
	return b.String()
}

// DecodeFrontmatter parses what EncodeFrontmatter produces. Keys beyond
// the supported set are ignored; missing or invalid type returns an error
// alongside whatever was decoded so callers can salvage the body.
func DecodeFrontmatter(s string) (Frontmatter, string, error) {
	const open = "---\n"
	const close = "\n---\n"
	if !strings.HasPrefix(s, open) {
		return Frontmatter{}, "", errors.New("memdir.DecodeFrontmatter: missing opening ---")
	}
	rest := s[len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		return Frontmatter{}, "", errors.New("memdir.DecodeFrontmatter: missing closing ---")
	}
	header := rest[:end]
	body := ""
	if cut := end + len(close); cut <= len(rest) {
		body = rest[cut:]
	}
	// EncodeFrontmatter ends with "---\n\n" so the first body byte is
	// the blank-line separator; strip it so round-trip preserves the
	// original body byte-for-byte.
	body = strings.TrimPrefix(body, "\n")
	fm := Frontmatter{}
	inMetadata := false
	for line := range strings.SplitSeq(header, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Track whether the current line is a metadata child by
		// leading-whitespace heuristic: nested keys are indented.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inMetadata = false
		}
		switch {
		case strings.HasPrefix(line, "name:"):
			fm.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			fm.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		case strings.HasPrefix(line, "metadata:"):
			inMetadata = true
		case inMetadata:
			if rest, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "type:"); ok {
				fm.Type = MemoryType(strings.TrimSpace(rest))
			}
		}
	}
	if !fm.Type.Valid() {
		return fm, body, fmt.Errorf("memdir.DecodeFrontmatter: invalid type %q", string(fm.Type))
	}
	return fm, body, nil
}
