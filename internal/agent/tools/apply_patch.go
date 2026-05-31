package tools

import (
	"context"
	"encoding/json"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/permission"
	"golang.org/x/net/html"
	"gopkg.in/yaml.v3"
)

// ApplyPatchToolName is the public tool identifier exposed to agents.
const ApplyPatchToolName = "apply_patch"

// ApplyPatchParams is the single-argument envelope: the model hands us the
// whole Codex-style patch text and we parse + apply it.
type ApplyPatchParams struct {
	Patch string `json:"patch" description:"A Codex-style patch envelope wrapped in *** Begin Patch / *** End Patch. Supports *** Add File:, *** Delete File:, and *** Update File: (with optional *** Move to:). Update hunks use @@ context markers and ' '/'+'/'-' line prefixes. Preserve exact indentation."`
}

// ApplyPatchPermissionsParams is the structured permission payload, one entry
// per file the patch touches, mirroring EditPermissionsParams so the existing
// diff-preview UI renders apply_patch changes identically to edit.
type ApplyPatchPermissionsParams struct {
	Changes []ApplyPatchFileChange `json:"changes"`
}

// ApplyPatchFileChange describes a single planned file mutation for both the
// permission preview and the response metadata.
type ApplyPatchFileChange struct {
	FilePath   string `json:"file_path"`
	Kind       string `json:"kind"` // "add" | "delete" | "update" | "move"
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
	MovePath   string `json:"move_path,omitempty"`
}

// ApplyPatchResponseMetadata carries the aggregate diff stats plus the
// per-file change list so the UI sidebar can attribute additions/removals.
type ApplyPatchResponseMetadata struct {
	Additions int                    `json:"additions"`
	Removals  int                    `json:"removals"`
	Changes   []ApplyPatchFileChange `json:"changes"`
}

//go:embed apply_patch.md
var applyPatchDescription string

// NewApplyPatchTool wires the apply_patch tool with the same dependency set as
// NewEditTool so it slots into buildAgent() identically. The applier reads and
// writes through the context-routed IO backend (CtxReadFile / CtxWriteFile),
// so it operates on remote attached hosts transparently.
func NewApplyPatchTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	files history.Service,
	filetracker filetracker.Service,
	workingDir string,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ApplyPatchToolName,
		applyPatchDescription,
		func(ctx context.Context, params ApplyPatchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if strings.TrimSpace(params.Patch) == "" {
				return fantasy.NewTextErrorResponse("patch is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session ID is required for apply_patch")
			}

			patch, err := parsePatch(params.Patch)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("apply_patch parse error: %s", err.Error())), nil
			}
			if len(patch.ops) == 0 {
				return fantasy.NewTextErrorResponse("apply_patch: patch contains no file operations"), nil
			}

			// PLAN PHASE: compute every file's new content WITHOUT writing.
			// If any op fails to locate its context the whole call aborts
			// before any byte hits disk — matching Codex's all-or-nothing
			// semantics so the model retries the full patch.
			plan, err := planPatch(ctx, patch, workingDir)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("apply_patch error: %s", err.Error())), nil
			}

			// Build aggregate diff + permission preview from the plan.
			var totalAdditions, totalRemovals int
			changes := make([]ApplyPatchFileChange, 0, len(plan))
			for _, pc := range plan {
				_, add, rem := diff.GenerateDiff(
					pc.oldContent,
					pc.newContent,
					strings.TrimPrefix(pc.primaryPath(), workingDir),
				)
				totalAdditions += add
				totalRemovals += rem
				changes = append(changes, ApplyPatchFileChange{
					FilePath:   pc.path,
					Kind:       pc.kind,
					OldContent: pc.oldContent,
					NewContent: pc.newContent,
					MovePath:   pc.movePath,
				})
			}

			granted, err := permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        fsext.PathOrPrefix(plan[0].primaryPath(), workingDir),
					ToolCallID:  call.ID,
					ToolName:    ApplyPatchToolName,
					Action:      "write",
					Description: describePlan(plan),
					Params:      ApplyPatchPermissionsParams{Changes: changes},
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !granted {
				return NewPermissionDeniedResponse(), nil
			}

			// COMMIT PHASE: only now do we touch disk. Each plan entry is a
			// final, validated mutation.
			affectedPaths, err := commitPlan(ctx, plan, sessionID, files, filetracker)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}

			for _, p := range affectedPaths {
				notifyLSPs(ctx, lspManager, p)
			}

			var summary strings.Builder
			summary.WriteString("Applied patch:\n")
			for _, pc := range plan {
				summary.WriteString("  ")
				summary.WriteString(pc.summaryLine())
				summary.WriteByte('\n')
			}

			text := fmt.Sprintf("<result>\n%s</result>\n", summary.String())
			for _, p := range affectedPaths {
				text += getDiagnostics(p, lspManager)
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(text),
				ApplyPatchResponseMetadata{
					Additions: totalAdditions,
					Removals:  totalRemovals,
					Changes:   changes,
				},
			), nil
		},
	)
}

// plannedChange is the resolved, ready-to-write result for one file op. It is
// produced entirely in the plan phase so the commit phase never re-derives
// content or re-locates context.
type plannedChange struct {
	kind       string // "add" | "delete" | "update" | "move"
	path       string // absolute path of the (original) file
	movePath   string // absolute destination path for moves; "" otherwise
	oldContent string // file content before the change ("" for add/missing)
	newContent string // file content after the change ("" for delete)
	isCrlf     bool   // the original file used CRLF line endings
	existed    bool   // the file existed on disk before the change
}

// primaryPath is the path used for diff labelling and the permission anchor:
// the destination for moves, otherwise the source path.
func (pc plannedChange) primaryPath() string {
	if pc.movePath != "" {
		return pc.movePath
	}
	return pc.path
}

func (pc plannedChange) summaryLine() string {
	switch pc.kind {
	case "add":
		return "A " + pc.path
	case "delete":
		return "D " + pc.path
	case "move":
		return "R " + pc.path + " -> " + pc.movePath
	default:
		return "M " + pc.path
	}
}

// describePlan renders the one-line permission description.
func describePlan(plan []plannedChange) string {
	if len(plan) == 1 {
		return "apply_patch: " + plan[0].summaryLine()
	}
	return fmt.Sprintf("apply_patch: %d file changes", len(plan))
}

// planPatch resolves every op into a plannedChange without writing anything.
// Any failure (file exists for Add, missing for Update/Delete, unlocatable
// hunk context, ambiguous context) returns an error and aborts the whole
// patch — no partial application.
func planPatch(ctx context.Context, patch parsedPatch, workingDir string) ([]plannedChange, error) {
	plan := make([]plannedChange, 0, len(patch.ops))
	for _, op := range patch.ops {
		if strings.ContainsAny(op.path, "*?[") {
			return nil, fmt.Errorf("apply_patch: file path %q contains wildcard characters (*, ?, [), which is strictly forbidden. Target a single, exact filename", op.path)
		}
		absPath := filepathext.SmartJoin(workingDir, op.path)
		switch op.kind {
		case opAdd:
			pc, err := planAdd(ctx, absPath, op)
			if err != nil {
				return nil, err
			}
			plan = append(plan, pc)
		case opDelete:
			pc, err := planDelete(ctx, absPath)
			if err != nil {
				return nil, err
			}
			plan = append(plan, pc)
		case opUpdate:
			var absMove string
			if op.movePath != "" {
				absMove = filepathext.SmartJoin(workingDir, op.movePath)
			}
			pc, err := planUpdate(ctx, absPath, absMove, op)
			if err != nil {
				return nil, err
			}
			plan = append(plan, pc)
		default:
			return nil, fmt.Errorf("planPatch: unknown op kind %d for path %s", op.kind, op.path)
		}
	}
	return plan, nil
}

func planAdd(ctx context.Context, absPath string, op patchOp) (plannedChange, error) {
	if info, err := CtxStat(ctx, absPath); err == nil {
		if info.IsDir() {
			return plannedChange{}, fmt.Errorf("planAdd: path is a directory, not a file: %s", absPath)
		}
		return plannedChange{}, fmt.Errorf("planAdd: file already exists, cannot Add File: %s", absPath)
	} else if !os.IsNotExist(err) {
		return plannedChange{}, fmt.Errorf("planAdd: failed to stat %s: %w", absPath, err)
	}
	if err := validateFileStructure(absPath, op.addContent); err != nil {
		return plannedChange{}, fmt.Errorf("planAdd: %w", err)
	}
	return plannedChange{
		kind:       "add",
		path:       absPath,
		oldContent: "",
		newContent: op.addContent,
		existed:    false,
	}, nil
}

func planDelete(ctx context.Context, absPath string) (plannedChange, error) {
	info, err := CtxStat(ctx, absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return plannedChange{}, fmt.Errorf("planDelete: file not found, cannot Delete File: %s", absPath)
		}
		return plannedChange{}, fmt.Errorf("planDelete: failed to stat %s: %w", absPath, err)
	}
	if info.IsDir() {
		return plannedChange{}, fmt.Errorf("planDelete: path is a directory, not a file: %s", absPath)
	}
	raw, err := CtxReadFile(ctx, absPath)
	if err != nil {
		return plannedChange{}, fmt.Errorf("planDelete: failed to read %s: %w", absPath, err)
	}
	oldContent, isCrlf := fsext.ToUnixLineEndings(string(raw))
	return plannedChange{
		kind:       "delete",
		path:       absPath,
		oldContent: oldContent,
		newContent: "",
		isCrlf:     isCrlf,
		existed:    true,
	}, nil
}

func planUpdate(ctx context.Context, absPath, absMovePath string, op patchOp) (plannedChange, error) {
	info, err := CtxStat(ctx, absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return plannedChange{}, fmt.Errorf("planUpdate: file not found, cannot Update File: %s", absPath)
		}
		return plannedChange{}, fmt.Errorf("planUpdate: failed to stat %s: %w", absPath, err)
	}
	if info.IsDir() {
		return plannedChange{}, fmt.Errorf("planUpdate: path is a directory, not a file: %s", absPath)
	}
	raw, err := CtxReadFile(ctx, absPath)
	if err != nil {
		return plannedChange{}, fmt.Errorf("planUpdate: failed to read %s: %w", absPath, err)
	}
	oldContent, isCrlf := fsext.ToUnixLineEndings(string(raw))

	newContent, err := applyHunks(oldContent, op.hunks, absPath)
	if err != nil {
		return plannedChange{}, err
	}
	if newContent == oldContent && absMovePath == "" {
		return plannedChange{}, fmt.Errorf("planUpdate: patch for %s produced no change", absPath)
	}
	if err := validateFileStructure(absPath, newContent); err != nil {
		return plannedChange{}, fmt.Errorf("planUpdate: %w", err)
	}

	kind := "update"
	if absMovePath != "" {
		kind = "move"
	}
	return plannedChange{
		kind:       kind,
		path:       absPath,
		movePath:   absMovePath,
		oldContent: oldContent,
		newContent: newContent,
		isCrlf:     isCrlf,
		existed:    true,
	}, nil
}

// commitPlan writes every planned change to disk, records history versions and
// read-tracking, and returns the set of paths that LSPs should be notified
// about. It assumes the plan has already been fully validated.
func commitPlan(
	ctx context.Context,
	plan []plannedChange,
	sessionID string,
	files history.Service,
	filetracker filetracker.Service,
) ([]string, error) {
	var affected []string
	for _, pc := range plan {
		switch pc.kind {
		case "add":
			if err := writeNewFile(ctx, pc.path, pc.newContent, false); err != nil {
				return nil, err
			}
			recordHistory(ctx, files, sessionID, pc.path, "", pc.newContent)
			filetracker.RecordRead(ctx, sessionID, pc.path)
			affected = append(affected, pc.path)

		case "delete":
			if err := CtxRemove(ctx, pc.path); err != nil {
				return nil, fmt.Errorf("commitPlan: failed to delete %s: %w", pc.path, err)
			}
			recordHistory(ctx, files, sessionID, pc.path, pc.oldContent, "")
			affected = append(affected, pc.path)

		case "update":
			out := pc.newContent
			if pc.isCrlf {
				out, _ = fsext.ToWindowsLineEndings(out)
			}
			if err := CtxWriteFile(ctx, pc.path, []byte(out), 0o644); err != nil {
				return nil, fmt.Errorf("commitPlan: failed to write %s: %w", pc.path, err)
			}
			recordHistory(ctx, files, sessionID, pc.path, pc.oldContent, pc.newContent)
			filetracker.RecordRead(ctx, sessionID, pc.path)
			affected = append(affected, pc.path)

		case "move":
			out := pc.newContent
			if pc.isCrlf {
				out, _ = fsext.ToWindowsLineEndings(out)
			}
			// Ensure the destination directory exists, then write the new
			// path and remove the old. We write-then-remove (not rename) so
			// the content edits land atomically with the move.
			if err := CtxMkdirAll(ctx, filepath.Dir(pc.movePath), 0o755); err != nil {
				return nil, fmt.Errorf("commitPlan: failed to create dir for %s: %w", pc.movePath, err)
			}
			if err := CtxWriteFile(ctx, pc.movePath, []byte(out), 0o644); err != nil {
				return nil, fmt.Errorf("commitPlan: failed to write moved file %s: %w", pc.movePath, err)
			}
			if pc.movePath != pc.path {
				if err := CtxRemove(ctx, pc.path); err != nil {
					return nil, fmt.Errorf("commitPlan: failed to remove old path %s after move: %w", pc.path, err)
				}
			}
			recordHistory(ctx, files, sessionID, pc.path, pc.oldContent, "")
			recordHistory(ctx, files, sessionID, pc.movePath, "", pc.newContent)
			filetracker.RecordRead(ctx, sessionID, pc.movePath)
			affected = append(affected, pc.path, pc.movePath)

		default:
			return nil, fmt.Errorf("commitPlan: unknown plan kind %q", pc.kind)
		}
	}
	return affected, nil
}

// writeNewFile creates parent dirs and writes a brand new file, erroring if it
// already exists unless allowOverwrite is set.
func writeNewFile(ctx context.Context, absPath, content string, allowOverwrite bool) error {
	if !allowOverwrite {
		if _, err := CtxStat(ctx, absPath); err == nil {
			return fmt.Errorf("writeNewFile: file already exists: %s", absPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("writeNewFile: failed to stat %s: %w", absPath, err)
		}
	}
	if err := CtxMkdirAll(ctx, filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("writeNewFile: failed to create parent dirs for %s: %w", absPath, err)
	}
	if err := CtxWriteFile(ctx, absPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writeNewFile: failed to write %s: %w", absPath, err)
	}
	return nil
}

// recordHistory mirrors edit.go's history bookkeeping: ensure a base version
// exists, then append the new version. Failures are logged, never fatal — the
// file write already succeeded and history is best-effort.
func recordHistory(ctx context.Context, files history.Service, sessionID, path, oldContent, newContent string) {
	file, err := files.GetByPathAndSession(ctx, path, sessionID)
	if err != nil {
		if _, cerr := files.Create(ctx, sessionID, path, oldContent); cerr != nil {
			slog.Error("Apply_patch failed to create file history", "path", path, "error", cerr)
		}
	} else if file.Content != oldContent {
		// File changed outside our knowledge; snapshot the pre-edit state.
		if _, verr := files.CreateVersion(ctx, sessionID, path, oldContent); verr != nil {
			slog.Error("Apply_patch failed to snapshot file history", "path", path, "error", verr)
		}
	}
	if _, err := files.CreateVersion(ctx, sessionID, path, newContent); err != nil {
		slog.Error("Apply_patch failed to create file history version", "path", path, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

// opKind discriminates the three file operations the envelope supports.
type opKind int

const (
	opAdd opKind = iota
	opDelete
	opUpdate
)

// patchHunk is one contiguous change block of an Update File op. contextHeader
// is the optional text after "@@" (purely informational; we do not require it
// to match). oldLines is the (context + removed) search block; newLines is the
// (context + added) replacement block. isEndOfFile marks a hunk that the model
// anchored to the file's tail via "*** End of File".
type patchHunk struct {
	contextHeader string
	oldLines      []string
	newLines      []string
	isEndOfFile   bool
}

// patchOp is a single file operation parsed from the envelope.
type patchOp struct {
	kind       opKind
	path       string
	movePath   string      // Update File only; "" when no "*** Move to:"
	addContent string      // Add File only; the full new-file content
	hunks      []patchHunk // Update File only
}

// parsedPatch is the whole envelope: an ordered list of ops.
type parsedPatch struct {
	ops []patchOp
}

const (
	beginPatchMarker = "*** Begin Patch"
	endPatchMarker   = "*** End Patch"
	addFileMarker    = "*** Add File: "
	deleteFileMarker = "*** Delete File: "
	updateFileMarker = "*** Update File: "
	moveToMarker     = "*** Move to: "
	endOfFileMarker  = "*** End of File"
	emptyContext     = "@@"
	contextPrefix    = "@@ "
)

// parsePatch parses the Codex-style patch envelope. It is deliberately
// tolerant of model formatting noise:
//   - a wrapping ```patch ... ``` (or bare ```) code fence is stripped;
//   - leading/trailing whitespace around the Begin/End markers is ignored;
//   - an Update hunk may omit the leading "@@" (the pre-@@ diff lines are
//     treated as one hunk).
//
// It is STRICT about structure: an unknown hunk header, an Update File with no
// hunks, or a diff line with an illegal prefix is a hard error so the model
// retries rather than silently dropping a change.
func parsePatch(raw string) (parsedPatch, error) {
	text := stripCodeFence(raw)
	lines := strings.Split(text, "\n")

	// Locate the Begin/End markers, tolerating surrounding whitespace and
	// blank lines outside the envelope.
	beginIdx, endIdx := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == beginPatchMarker && beginIdx == -1 {
			beginIdx = i
		}
		if t == endPatchMarker {
			endIdx = i // last End marker wins
		}
	}
	if beginIdx == -1 {
		return parsedPatch{}, errors.New("missing '*** Begin Patch' marker on the first line")
	}
	if endIdx == -1 {
		return parsedPatch{}, errors.New("missing '*** End Patch' marker on the last line")
	}
	if endIdx <= beginIdx {
		return parsedPatch{}, errors.New("'*** End Patch' must come after '*** Begin Patch'")
	}

	body := lines[beginIdx+1 : endIdx]

	var ops []patchOp
	i := 0
	for i < len(body) {
		line := body[i]
		trimmed := strings.TrimSpace(line)

		// Skip blank lines between operations.
		if trimmed == "" {
			i++
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, addFileMarker):
			op, consumed, err := parseAddFile(body, i)
			if err != nil {
				return parsedPatch{}, err
			}
			ops = append(ops, op)
			i += consumed

		case strings.HasPrefix(trimmed, deleteFileMarker):
			path := strings.TrimSpace(strings.TrimPrefix(trimmed, deleteFileMarker))
			if path == "" {
				return parsedPatch{}, errors.New("'*** Delete File:' is missing a path")
			}
			ops = append(ops, patchOp{kind: opDelete, path: path})
			i++

		case strings.HasPrefix(trimmed, updateFileMarker):
			op, consumed, err := parseUpdateFile(body, i)
			if err != nil {
				return parsedPatch{}, err
			}
			ops = append(ops, op)
			i += consumed

		default:
			return parsedPatch{}, fmt.Errorf(
				"invalid line %q at offset %d: expected one of '*** Add File:', '*** Delete File:', '*** Update File:'",
				trimmed, i)
		}
	}

	return parsedPatch{ops: ops}, nil
}

// stripCodeFence removes a single wrapping markdown code fence (```patch,
// ```diff, or bare ```) if the model wrapped the whole envelope in one. It only
// strips when BOTH an opening and a closing fence are present so we never
// corrupt a patch whose body legitimately contains a stray backtick line.
func stripCodeFence(raw string) string {
	lines := strings.Split(raw, "\n")
	// Find first non-blank line.
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) {
		return raw
	}
	first := strings.TrimSpace(lines[start])
	if !strings.HasPrefix(first, "```") {
		return raw
	}
	// Find last non-blank line.
	end := len(lines) - 1
	for end > start && strings.TrimSpace(lines[end]) == "" {
		end--
	}
	last := strings.TrimSpace(lines[end])
	if end <= start || last != "```" {
		return raw
	}
	return strings.Join(lines[start+1:end], "\n")
}

// isTopLevelOpMarker reports whether a body line begins a new file operation
// (Add/Delete/Update). It deliberately does NOT match "*** End of File" or
// "*** End Patch" so those stay attached to the hunk being parsed.
func isTopLevelOpMarker(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, addFileMarker) ||
		strings.HasPrefix(t, deleteFileMarker) ||
		strings.HasPrefix(t, updateFileMarker)
}

// parseAddFile parses an "*** Add File:" op starting at index start. Following
// lines beginning with '+' are the file content (the '+' stripped). A line that
// does not start with '+' ends the content block.
func parseAddFile(body []string, start int) (patchOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body[start]), addFileMarker))
	if path == "" {
		return patchOp{}, 0, errors.New("'*** Add File:' is missing a path")
	}
	var contentLines []string
	i := start + 1
	for i < len(body) {
		l := body[i]
		if strings.HasPrefix(strings.TrimSpace(l), "***") {
			break
		}
		if !strings.HasPrefix(l, "+") {
			// Blank lines inside an Add block are written by the model as a
			// bare "+"; a line with no '+' prefix terminates the content.
			break
		}
		contentLines = append(contentLines, l[1:])
		i++
	}
	// Add File content is reconstructed line-by-line; each "+line" contributed
	// one source line, so we join with '\n' and append a trailing newline to
	// match Codex (every "+line\n" produced a line terminator).
	content := ""
	if len(contentLines) > 0 {
		content = strings.Join(contentLines, "\n") + "\n"
	}
	return patchOp{kind: opAdd, path: path, addContent: content}, i - start, nil
}

// parseUpdateFile parses an "*** Update File:" op (with optional "*** Move to:")
// and one or more hunks. Returns the op and the number of body lines consumed.
func parseUpdateFile(body []string, start int) (patchOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body[start]), updateFileMarker))
	if path == "" {
		return patchOp{}, 0, errors.New("'*** Update File:' is missing a path")
	}
	op := patchOp{kind: opUpdate, path: path}
	i := start + 1

	// Optional move line.
	if i < len(body) && strings.HasPrefix(strings.TrimSpace(body[i]), moveToMarker) {
		mv := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body[i]), moveToMarker))
		if mv == "" {
			return patchOp{}, 0, fmt.Errorf("'*** Move to:' for %s is missing a path", path)
		}
		op.movePath = mv
		i++
	}

	// Collect raw hunk lines up to the next top-level op marker. Note that
	// "*** End of File" also begins with "***" but is part of THIS hunk body,
	// not a new op — so we only stop at a genuine Add/Delete/Update header.
	hunkStart := i
	for i < len(body) {
		if isTopLevelOpMarker(body[i]) {
			break
		}
		i++
	}
	hunkLines := body[hunkStart:i]

	hunks, err := parseHunks(hunkLines, path)
	if err != nil {
		return patchOp{}, 0, err
	}
	if len(hunks) == 0 {
		return patchOp{}, 0, fmt.Errorf("Update File hunk for path %q is empty", path)
	}
	op.hunks = hunks
	return op, i - start, nil
}

// parseHunks splits an Update File's body into hunks. A new hunk begins at each
// "@@"/"@@ <ctx>" line. If the body begins with diff lines (no leading "@@"),
// those lines form an implicit first hunk (missing-@@ tolerance).
func parseHunks(lines []string, path string) ([]patchHunk, error) {
	var hunks []patchHunk
	var cur *patchHunk

	flush := func() {
		if cur != nil && (len(cur.oldLines) > 0 || len(cur.newLines) > 0) {
			hunks = append(hunks, *cur)
		}
		cur = nil
	}

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)

		// Blank lines that separate hunks: a truly empty line is ambiguous —
		// it could be a context line with no body. Codex treats a leading-
		// empty line as an empty context/diff line, but blank separators
		// between hunks are skipped. We only skip a blank line when no hunk is
		// currently open; inside an open hunk an empty line is a context line.
		if trimmed == "" && cur == nil {
			continue
		}

		// Hunk header.
		if l == emptyContext || trimmed == emptyContext {
			flush()
			cur = &patchHunk{}
			continue
		}
		if strings.HasPrefix(l, contextPrefix) {
			flush()
			cur = &patchHunk{contextHeader: l[len(contextPrefix):]}
			continue
		}

		// End-of-file anchor.
		if trimmed == endOfFileMarker {
			if cur == nil {
				return nil, fmt.Errorf("'*** End of File' before any hunk lines in update for %q", path)
			}
			cur.isEndOfFile = true
			continue
		}

		// Diff line. If no hunk is open yet, open an implicit one (missing-@@
		// tolerance).
		if cur == nil {
			cur = &patchHunk{}
		}

		if l == "" {
			// Empty line == an empty context line (present in both sides).
			cur.oldLines = append(cur.oldLines, "")
			cur.newLines = append(cur.newLines, "")
			continue
		}
		switch l[0] {
		case ' ':
			cur.oldLines = append(cur.oldLines, l[1:])
			cur.newLines = append(cur.newLines, l[1:])
		case '+':
			cur.newLines = append(cur.newLines, l[1:])
		case '-':
			cur.oldLines = append(cur.oldLines, l[1:])
		default:
			return nil, fmt.Errorf(
				"invalid diff line %q in update for %q: every line must start with ' ' (context), '+' (add), or '-' (remove)",
				l, path)
		}
	}
	flush()
	return hunks, nil
}

// ---------------------------------------------------------------------------
// Applier
// ---------------------------------------------------------------------------

// applyHunks applies each hunk sequentially to content, returning the new
// content. Hunks are located from a moving cursor so two hunks with identical
// context still resolve to distinct, in-order locations. Any hunk whose search
// block cannot be uniquely located by ANY seek tier aborts the whole apply
// (returns an error) — no partial application.
func applyHunks(content string, hunks []patchHunk, path string) (string, error) {
	fileLines := splitLinesKeepStructure(content)

	cursor := 0 // line index from which the next hunk may start searching
	for hi, h := range hunks {
		searchBlock := h.oldLines
		newLines := h.newLines
		// A hunk that only adds lines (no context, no removals) is ambiguous
		// to place by content alone. Codex permits a pure-add hunk only when
		// anchored to EOF; otherwise we refuse rather than guess insertion
		// point.
		if len(searchBlock) == 0 {
			if !h.isEndOfFile {
				return "", fmt.Errorf(
					"%s hunk %d is a pure insertion with no context to anchor it; add surrounding context lines or anchor with '*** End of File'",
					path, hi+1)
			}
			// Pure add at EOF: append the new lines. If the file ended with a
			// newline, splitLinesKeepStructure left a trailing "" element
			// representing that final newline; insert BEFORE it so the file
			// keeps exactly one trailing newline rather than gaining a blank
			// line. If the file had no trailing newline, append at the end.
			if n := len(fileLines); n > 0 && fileLines[n-1] == "" {
				tail := append([]string{}, newLines...)
				tail = append(tail, "")
				fileLines = append(fileLines[:n-1], tail...)
			} else {
				fileLines = append(fileLines, newLines...)
			}
			cursor = len(fileLines)
			continue
		}

		matchIdx, tier, err := seekUnique(fileLines, searchBlock, cursor, h.isEndOfFile, path, hi+1)
		if err != nil {
			return "", err
		}

		// On a leading-indentation-tolerant match (tier c) or collapse match (tier d),
		// translate the indentation of added lines to fit the file's style.
		if tier == seekTierIndent || tier == seekTierCollapse {
			newLines = reindentNewLinesWithFallback(fileLines[matchIdx:matchIdx+len(searchBlock)], searchBlock, newLines, content)
		}

		// Splice: replace [matchIdx, matchIdx+len(searchBlock)) with newLines.
		end := matchIdx + len(searchBlock)
		out := make([]string, 0, len(fileLines)-len(searchBlock)+len(newLines))
		out = append(out, fileLines[:matchIdx]...)
		out = append(out, newLines...)
		out = append(out, fileLines[end:]...)
		fileLines = out

		// Advance the cursor past the inserted block so subsequent hunks only
		// match later in the file (Codex requires hunks in source order).
		cursor = matchIdx + len(newLines)
	}

	return joinLinesKeepStructure(fileLines, content), nil
}

// seekTier identifies which tolerance tier produced a match, so the splice can
// decide whether the added lines need reindentation.
type seekTier int

const (
	seekTierExact    seekTier = iota // exact line equality
	seekTierRstrip                   // trailing-whitespace-insensitive
	seekTierIndent                   // leading-indentation-tolerant
	seekTierCollapse                 // collapse all spaces/tabs and match
)

// seekUnique locates pattern within fileLines at or after start using a layered
// seek with strictly increasing tolerance, and verifies UNIQUENESS at the tier
// that first matches. Tiers:
//
//	(a) exact line equality;
//	(b) trailing-whitespace-insensitive line equality (rstrip per line);
//	(c) leading-indentation-tolerant equality (strip leading ' '/'\t', require
//	    interior+trailing bytes equal).
//	(d) collapse whitespace (ignore leading/trailing, collapse inner spaces).
//
// Within a tier we scan the whole window space [start, len-n]: if exactly one
// window matches we return it; if two or more match the tier is AMBIGUOUS and
// we abort (we never pick the "first" of several to avoid mis-applying). If
// zero match we fall to the next tier. If all tiers miss we run high-signal
// diagnostics to return an error explaining the closest match.
//
// When eof is set we additionally require the unique match to end at the file's
// final line, matching Codex's "*** End of File" anchoring.
func seekUnique(fileLines, pattern []string, start int, eof bool, path string, hunkNo int) (int, seekTier, error) {
	n := len(pattern)
	if n == 0 {
		return start, seekTierExact, nil
	}
	if n > len(fileLines) {
		return 0, 0, fmt.Errorf("%s hunk %d context (%d lines) is longer than the file", path, hunkNo, n)
	}

	type matcher func(a, b string) bool
	exact := func(a, b string) bool { return a == b }
	rstrip := func(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") }

	tiers := []struct {
		tier seekTier
		eq   matcher
	}{
		{seekTierExact, exact},
		{seekTierRstrip, rstrip},
	}
	for _, t := range tiers {
		idx, count := scanWindows(fileLines, pattern, start, t.eq)
		if count == 1 {
			if eof && idx+n != len(fileLines) {
				return 0, 0, fmt.Errorf(
					"%s hunk %d is anchored to end-of-file but its context was found earlier in the file", path, hunkNo)
			}
			return idx, t.tier, nil
		}
		if count > 1 {
			return 0, 0, fmt.Errorf(
				"%s hunk %d context is ambiguous: it matches %d locations. Add more surrounding context lines to make it unique",
				path, hunkNo, count)
		}
	}

	// Tier (c) leading-indentation-tolerant unique match
	idx, count := scanWindowsIndentTolerant(fileLines, pattern, start)
	if count == 1 {
		if eof && idx+n != len(fileLines) {
			return 0, 0, fmt.Errorf(
				"%s hunk %d is anchored to end-of-file but its context was found earlier in the file", path, hunkNo)
		}
		return idx, seekTierIndent, nil
	}
	if count > 1 {
		return 0, 0, fmt.Errorf(
			"%s hunk %d context is ambiguous under indentation-tolerant matching: it matches %d locations. Add more surrounding context",
			path, hunkNo, count)
	}

	// Tier (d) collapse whitespace matching
	idx, count = scanWindowsCollapse(fileLines, pattern, start)
	if count == 1 {
		if eof && idx+n != len(fileLines) {
			return 0, 0, fmt.Errorf(
				"%s hunk %d is anchored to end-of-file but its context was found earlier in the file", path, hunkNo)
		}
		return idx, seekTierCollapse, nil
	}
	if count > 1 {
		return 0, 0, fmt.Errorf(
			"%s hunk %d context is ambiguous under whitespace-collapsed matching: it matches %d locations. Add more surrounding context",
			path, hunkNo, count)
	}

	return 0, 0, diagnoseMatchError(fileLines, pattern, path, hunkNo)
}

// reindentNewLinesWithFallback translates hunk's added lines to the file's style.
// If strict oldIndent->fileIndent fails (due to model using unobserved indents),
// it detects the indentation unit dynamically to scale the indentation levels.
func reindentNewLinesWithFallback(fileWindow, oldLines, newLines []string, fullContent string) []string {
	res, ok := reindentNewLines(fileWindow, oldLines, newLines)
	if ok {
		return res
	}

	fileIndentUnit := detectIndentUnit(fileWindow, fullContent)
	modelIndentUnit := detectIndentUnit(oldLines, "")

	out := make([]string, len(newLines))
	for i, l := range newLines {
		if isBlankPatchLine(l) {
			out[i] = l
			continue
		}
		ind, rest := leadingIndent(l)
		to := translateIndent(ind, modelIndentUnit, fileIndentUnit)
		out[i] = to + rest
	}
	return out
}

func detectIndentUnit(lines []string, fullContent string) string {
	indents := make(map[string]int)
	gather := func(ls []string) {
		for _, l := range ls {
			if isBlankPatchLine(l) {
				continue
			}
			ind, _ := leadingIndent(l)
			if ind != "" {
				indents[ind]++
			}
		}
	}
	gather(lines)
	if len(indents) == 0 && fullContent != "" {
		gather(strings.Split(fullContent, "\n"))
	}
	if len(indents) == 0 {
		return "    "
	}
	var shortest string
	for ind := range indents {
		if shortest == "" || len(ind) < len(shortest) {
			shortest = ind
		}
	}
	return shortest
}

func translateIndent(indent, modelUnit, fileUnit string) string {
	if modelUnit == "" || fileUnit == "" || indent == "" {
		return indent
	}
	if strings.Contains(modelUnit, " ") && strings.Contains(indent, " ") {
		levels := len(indent) / len(modelUnit)
		if levels == 0 {
			levels = 1
		}
		return strings.Repeat(fileUnit, levels)
	}
	if strings.Contains(modelUnit, "\t") && strings.Contains(indent, "\t") {
		levels := len(indent) / len(modelUnit)
		if levels == 0 {
			levels = 1
		}
		return strings.Repeat(fileUnit, levels)
	}
	levels := len(indent) / 4
	if len(modelUnit) > 0 {
		levels = len(indent) / len(modelUnit)
	}
	if levels == 0 {
		levels = 1
	}
	return strings.Repeat(fileUnit, levels)
}

func collapseWS(s string) string {
	s = strings.TrimSpace(s)
	var sb strings.Builder
	inWS := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !inWS {
				sb.WriteRune(' ')
				inWS = true
			}
		} else {
			sb.WriteRune(r)
			inWS = false
		}
	}
	return sb.String()
}

func scanWindowsCollapse(fileLines, pattern []string, start int) (int, int) {
	n := len(pattern)
	patBodies := make([]string, n)
	for j, p := range pattern {
		patBodies[j] = collapseWS(p)
	}
	first := -1
	count := 0
	for i := start; i+n <= len(fileLines); i++ {
		ok := true
		for j := 0; j < n; j++ {
			if collapseWS(fileLines[i+j]) != patBodies[j] {
				ok = false
				break
			}
		}
		if ok {
			if first == -1 {
				first = i
			}
			count++
		}
	}
	return first, count
}

func diagnoseMatchError(fileLines, pattern []string, path string, hunkNo int) error {
	n := len(pattern)
	if n == 0 {
		return fmt.Errorf("%s hunk %d context is empty", path, hunkNo)
	}

	bestIdx := -1
	bestScore := -1.0

	for i := 0; i+n <= len(fileLines); i++ {
		score := 0.0
		for j := 0; j < n; j++ {
			fNorm := collapseWS(normalizeQuotes(fileLines[i+j]))
			pNorm := collapseWS(normalizeQuotes(pattern[j]))
			if fNorm == pNorm {
				score += 1.0
			} else if strings.Contains(fNorm, pNorm) || strings.Contains(pNorm, fNorm) {
				score += 0.5
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestIdx != -1 && bestScore >= float64(n)*0.3 {
		var diffSummary strings.Builder
		diffSummary.WriteString(fmt.Sprintf("%s hunk %d context not found. We found a very similar section around line %d, but it failed to match strictly:\n", path, hunkNo, bestIdx+1))
		for j := 0; j < n; j++ {
			fileLine := fileLines[bestIdx+j]
			patLine := pattern[j]
			diffSummary.WriteString(fmt.Sprintf("  Line %d in file: %q\n", bestIdx+j+1, fileLine))
			diffSummary.WriteString(fmt.Sprintf("  Hunk expected:  %q\n", patLine))

			if fileLine == patLine {
				diffSummary.WriteString("    (Matches exactly)\n")
			} else if strings.TrimSpace(fileLine) == strings.TrimSpace(patLine) {
				diffSummary.WriteString("    -> Mismatch reason: Indentation difference (spaces vs tabs or count)\n")
			} else if collapseWS(normalizeQuotes(fileLine)) == collapseWS(normalizeQuotes(patLine)) {
				diffSummary.WriteString("    -> Mismatch reason: Quote style (straight vs curly quotes) or extra spacing inside line\n")
			} else {
				diffSummary.WriteString("    -> Mismatch reason: Content difference\n")
			}
		}
		diffSummary.WriteString("Please rebuild the hunk using the exact lines from the file.")
		return errors.New(diffSummary.String())
	}

	return fmt.Errorf(
		"%s hunk %d context not found in the file. The context lines did not match any section. Re-read the file and rebuild the hunk",
		path, hunkNo)
}

func validateFileStructure(path, content string) error {
	ext := strings.ToLower(filepath.Ext(path))
	
	// Content sniffing for extensionless config files
	if ext == "" {
		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			ext = ".json"
		} else if (strings.HasPrefix(trimmed, "---") || strings.Contains(trimmed, ": ")) && !strings.Contains(trimmed, "\n\t") {
			// Basic YAML detection heuristic: start with --- or contains ': ' and does not mix with tabs (yaml forbidden tabs)
			ext = ".yaml"
		}
	}

	switch ext {
	case ".json":
		var js json.RawMessage
		if err := json.Unmarshal([]byte(content), &js); err != nil {
			return fmt.Errorf("semantic AST guard: invalid JSON syntax: %w", err)
		}
	case ".yaml", ".yml":
		var y interface{}
		if err := yaml.Unmarshal([]byte(content), &y); err != nil {
			return fmt.Errorf("semantic AST guard: invalid YAML syntax: %w", err)
		}
	case ".html", ".htm":
		if _, err := html.Parse(strings.NewReader(content)); err != nil {
			return fmt.Errorf("semantic AST guard: invalid HTML syntax: %w", err)
		}
	}
	return nil
}

// reindentNewLines maps the hunk's added lines from the model's indentation to
// the file's actual indentation, using a per-level translation derived from the
// matched window. It mirrors resolveLeadingIndent's REINDENT RULE in
// edit_normalize.go:
//
//   - Build M: oldIndent -> fileIndent from non-blank matched lines. If the
//     same oldIndent maps to two different fileIndents the transform is
//     ambiguous -> return ok=false.
//   - Each non-blank new line's leading ws MUST be a known key; replace it with
//     M[key]. An unknown key -> ok=false (we never invent an indent for a level
//     the match never observed). Blank lines pass through unchanged.
//   - If M is the identity on every key there is no drift to repair; return the
//     new lines unchanged.
//
// fileWindow are the actual file lines the hunk matched; oldLines are the hunk's
// context+removed lines (same length, same order). newLines are the
// context+added replacement lines.
func reindentNewLines(fileWindow, oldLines, newLines []string) ([]string, bool) {
	indentMap := make(map[string]string, len(oldLines))
	identity := true
	for i := range oldLines {
		if isBlankPatchLine(oldLines[i]) {
			continue
		}
		from, _ := leadingIndent(oldLines[i])
		to, _ := leadingIndent(fileWindow[i])
		if prev, seen := indentMap[from]; seen {
			if prev != to {
				return nil, false
			}
			continue
		}
		indentMap[from] = to
		if from != to {
			identity = false
		}
	}
	if len(indentMap) == 0 {
		return nil, false
	}
	if identity {
		return newLines, true
	}
	out := make([]string, len(newLines))
	for i, l := range newLines {
		if isBlankPatchLine(l) {
			out[i] = l
			continue
		}
		ind, rest := leadingIndent(l)
		to, ok := indentMap[ind]
		if !ok {
			return nil, false
		}
		out[i] = to + rest
	}
	return out, true
}

// scanWindows counts, and returns the index of, windows in fileLines[start:]
// that match pattern under the supplied per-line equality predicate. Returns
// (firstMatchIndex, matchCount). Caller decides uniqueness.
func scanWindows(fileLines, pattern []string, start int, eq func(a, b string) bool) (int, int) {
	n := len(pattern)
	first := -1
	count := 0
	for i := start; i+n <= len(fileLines); i++ {
		ok := true
		for j := 0; j < n; j++ {
			if !eq(fileLines[i+j], pattern[j]) {
				ok = false
				break
			}
		}
		if ok {
			if first == -1 {
				first = i
			}
			count++
		}
	}
	return first, count
}

// scanWindowsIndentTolerant is tier (c): match after stripping the leading run
// of ' '/'\t' from both the file line and the pattern line. Interior and
// trailing bytes must still match exactly — only LEADING indentation drift is
// tolerated (the dominant model edit-miss class per edit_normalize.go).
func scanWindowsIndentTolerant(fileLines, pattern []string, start int) (int, int) {
	n := len(pattern)
	// Pre-strip pattern bodies once.
	patBodies := make([]string, n)
	for j, p := range pattern {
		_, patBodies[j] = leadingIndent(p)
	}
	first := -1
	count := 0
	for i := start; i+n <= len(fileLines); i++ {
		ok := true
		for j := 0; j < n; j++ {
			_, body := leadingIndent(fileLines[i+j])
			if body != patBodies[j] {
				ok = false
				break
			}
		}
		if ok {
			if first == -1 {
				first = i
			}
			count++
		}
	}
	return first, count
}

// splitLinesKeepStructure splits content into lines on '\n'. A trailing
// newline yields a final empty element (consistent with strings.Split), which
// joinLinesKeepStructure restores faithfully. This keeps the
// has-final-newline / no-final-newline distinction byte-exact.
func splitLinesKeepStructure(content string) []string {
	if content == "" {
		return []string{}
	}
	return strings.Split(content, "\n")
}

// joinLinesKeepStructure rejoins lines with '\n'. orig is unused for content
// but kept to make the round-trip intent explicit at call sites: split→edit→
// join is byte-faithful for the unchanged regions because Split/Join on '\n'
// are exact inverses.
func joinLinesKeepStructure(lines []string, _ string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// leadingIndent returns the maximal prefix of s composed solely of ' ' and
// '\t', plus the remainder. Only space and tab count as indentation; any other
// byte (including other Unicode space) terminates the run so we never silently
// absorb a meaningful character into the indent.
func leadingIndent(s string) (indent, rest string) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i], s[i:]
}

// isBlankPatchLine reports whether the line is empty or only space/tab. Blank
// lines carry no body to match and no indent to anchor on, so the indent
// translation treats them as transparent.
func isBlankPatchLine(s string) bool {
	_, rest := leadingIndent(s)
	return rest == ""
}
