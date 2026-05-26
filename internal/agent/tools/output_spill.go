package tools

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

const (
	// SpillSubdir is the default subdirectory under dataDir where spilled
	// tool outputs land. Kept stable so the View tool's path-validation can
	// recognise spill paths if it ever wants to gate access.
	SpillSubdir = "tool-results"
	// SpillGCMaxAge controls how long a spilled artefact lingers before
	// the next-write sweep removes it. Seven days is enough for the model
	// to ask for an older transcript by ID within a normal session arc.
	SpillGCMaxAge = 7 * 24 * time.Hour
	// spillGCSweepEveryNth is the 1-in-N coin flip on each write that
	// triggers a directory sweep. Keeps the path fast under load and
	// avoids a long-running goroutine.
	spillGCSweepEveryNth = 100
)

// SpillPart is one labelled segment written to a spill file. Empty Name
// means "no header" — used for the primary stream (typically stdout).
// Non-primary parts (stderr, matches, raw HTML) get a `--- name ---`
// header so a human or the model can reassemble the original streams.
type SpillPart struct {
	Name    string
	Content string
}

// SpillResult is what callers feed back into their response metadata so
// the UI and trace can surface "full output at PATH" without re-reading
// the disk.
type SpillResult struct {
	Path  string
	Bytes int
}

// Spiller writes a "too big to embed in a tool response" payload to a
// stable per-session path under dataDir, gated by a caller-owned byte
// threshold. Multiple tools share one Spiller; the label argument keeps
// their files distinguishable (`bash-…`, `view-…`, `rg-…`).
//
// A nil receiver is safe — MaybeSpill returns (zero, false) so callers
// that don't have a dataDir (older test fixtures) degrade silently to
// the inline-only path.
type Spiller struct {
	DataDir string
	Subdir  string           // defaults to SpillSubdir when empty
	Now     func() time.Time // defaults to time.Now when nil
}

// NewSpiller constructs a Spiller pinned to the given dataDir. Subdir
// defaults to SpillSubdir; callers wanting per-tool isolation can set
// it explicitly after construction.
func NewSpiller(dataDir string) *Spiller {
	return &Spiller{DataDir: dataDir, Subdir: SpillSubdir}
}

// MaybeSpill writes parts to disk only if their combined byte length
// exceeds threshold. Returns (SpillResult{}, false) on no-op or write
// failure — failure is treated as no-op so the calling tool response is
// never lost just because the disk hiccuped.
func (s *Spiller) MaybeSpill(sessionID, callID, label string, threshold int, parts ...SpillPart) (SpillResult, bool) {
	if s == nil || s.DataDir == "" || sessionID == "" {
		return SpillResult{}, false
	}
	total := 0
	for _, p := range parts {
		total += len(p.Content)
	}
	if total <= threshold {
		return SpillResult{}, false
	}
	subdir := s.Subdir
	if subdir == "" {
		subdir = SpillSubdir
	}
	dir := filepath.Join(s.DataDir, subdir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SpillResult{}, false
	}
	// Best-effort GC: 1% of writes sweep ageing files. Keeps the path
	// fast under load and avoids a long-running goroutine.
	if rand.Intn(spillGCSweepEveryNth) == 0 {
		gcSpillDir(filepath.Join(s.DataDir, subdir), SpillGCMaxAge)
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	name := fmt.Sprintf("%s-%s-%d.log", label, sanitizeCallID(callID), now().UnixNano())
	path := filepath.Join(dir, name)
	var buf bytes.Buffer
	wrote := false
	for _, p := range parts {
		if p.Content == "" {
			continue
		}
		if wrote {
			buf.WriteByte('\n')
		}
		if p.Name != "" {
			fmt.Fprintf(&buf, "--- %s ---\n", p.Name)
		}
		buf.WriteString(p.Content)
		wrote = true
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return SpillResult{}, false
	}
	return SpillResult{Path: path, Bytes: buf.Len()}, true
}
