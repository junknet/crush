// Package memdir reads a per-workspace cross-session memory index and
// renders it into a prompt section that callers can inject into the static
// part of a system prompt. It is intentionally read-only: writing entries
// is the model's job via the existing view/write tools. Bounded by hard
// caps (200 lines / 25 KiB) so a runaway index can't blow up the prompt.
package memdir

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxIndexLines is the hard cap on how many MEMORY.md lines we inject.
	// Beyond this the body is truncated with an explicit notice so the
	// model sees a clean signal rather than silent loss.
	MaxIndexLines = 200
	// MaxIndexBytes mirrors MaxIndexLines as a byte budget; whichever cap
	// trips first wins.
	MaxIndexBytes = 25 * 1024
)

// WorkspaceSlug returns a stable, filesystem-safe key derived from an
// absolute workspace path. The result is deterministic, short enough for a
// directory name, and tolerant of paths with spaces, dots, or Windows
// drive letters. We keep the base name (for readability) and append the
// first 8 chars of the path's SHA-1 (for uniqueness across forks).
func WorkspaceSlug(workspacePath string) string {
	clean := strings.TrimRight(filepath.Clean(workspacePath), string(filepath.Separator))
	if clean == "" {
		return "default"
	}
	base := filepath.Base(clean)
	base = sanitizeBase(base)
	if base == "" {
		base = "ws"
	}
	sum := sha1.Sum([]byte(clean))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:4]))
}

func sanitizeBase(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return strings.ToLower(string(out))
}

// IndexPath returns the absolute path to MEMORY.md for a workspace under
// the given data directory. The directory tree is created on demand by
// EnsureWorkspace.
func IndexPath(dataDir, workspacePath string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "projects", WorkspaceSlug(workspacePath), "memory", "MEMORY.md")
}

// EnsureWorkspace creates the per-workspace memdir tree if missing and
// seeds an empty MEMORY.md with a usage hint. Errors are returned so the
// caller can decide whether to surface them — typically callers swallow
// them since prompt assembly should never block on disk.
func EnsureWorkspace(dataDir, workspacePath string) error {
	path := IndexPath(dataDir, workspacePath)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(seedTemplate), 0o644)
}

const seedTemplate = `<!--
MEMORY.md — cross-session index for this workspace.

Each entry should be one line, under ~150 chars:
- [Title](file.md) — one-line hook
Detailed memory files live next to this index with frontmatter:
  ---
  name: short-slug
  description: one-line summary
  metadata:
    type: user | feedback | project | reference
  ---
This file is auto-loaded into the brain agent's system prompt every turn.
Keep it lean; truncated past 200 lines / 25 KiB.
-->
`

// IndexPrompt builds the prompt-friendly memory block. It reads MEMORY.md,
// applies the line/byte caps, and wraps the body in a clearly named
// section. Returns the empty string when the workspace has no memdir yet
// (or the file is empty after the header comment).
func IndexPrompt(dataDir, workspacePath string) string {
	path := IndexPath(dataDir, workspacePath)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var (
		lines     []string
		bytes     int
		truncated bool
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if bytes+len(line)+1 > MaxIndexBytes || len(lines) >= MaxIndexLines {
			truncated = true
			break
		}
		bytes += len(line) + 1
		lines = append(lines, line)
	}

	body := strings.TrimSpace(strings.Join(lines, "\n"))
	if body == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("<auto_memory>\n")
	b.WriteString("Cross-session memory for this workspace. Read-only here; write via the view/edit tools on files under ")
	b.WriteString(filepath.Dir(path))
	b.WriteString("/.\n\n")
	b.WriteString(body)
	if truncated {
		fmt.Fprintf(&b, "\n\n... [MEMORY.md truncated at %d lines / %d bytes — full file is at %s] ...", MaxIndexLines, MaxIndexBytes, path)
	}
	b.WriteString("\n</auto_memory>")
	return b.String()
}
