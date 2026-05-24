package tools

import (
	"fmt"
	"strings"
)

// notFoundDiagnostic returns a verbose explanation of why an edit's
// old_string was not found in content. The bare "not found, make sure it
// matches exactly" message used to wedge LLMs into retrying the same edit
// over and over — the model could not see *why* the match failed. This
// helper inspects the file, locates the closest fuzzy match (first line
// of old_string, whitespace-trimmed), and emits the surrounding excerpt
// so the model can copy the exact bytes on the next attempt.
//
// pathHint is the file path (or "in-memory edit chain" for multiedit's
// staged content) used only in the error message; it never affects
// matching.
func notFoundDiagnostic(content, oldString, pathHint string) string {
	const baseMsg = "old_string not found. Make sure it matches exactly, including whitespace and line breaks."
	if oldString == "" {
		return baseMsg
	}
	firstLine, _, _ := strings.Cut(oldString, "\n")
	trimmed := strings.TrimSpace(firstLine)
	if trimmed == "" {
		return baseMsg
	}

	idx := strings.Index(content, trimmed)
	if idx < 0 {
		// Not even a fuzzy hit — likely wrong file or stale content.
		preview := truncateForMsg(firstLine, 80)
		hint := ""
		if pathHint != "" {
			hint = " in " + pathHint
		}
		return fmt.Sprintf(
			"%s\n\nDiagnostic: the first line of your old_string (%q) does not appear anywhere%s. The file may have been modified since you last viewed it, or you may be editing the wrong path. Re-read the file with `view` before retrying.",
			baseMsg, preview, hint,
		)
	}

	// Found a fuzzy hit — extract the surrounding lines with explicit
	// markers so the model can see the exact bytes (including whitespace).
	lineNumber := strings.Count(content[:idx], "\n") + 1
	lines := strings.Split(content, "\n")
	start := lineNumber - 3
	if start < 1 {
		start = 1
	}
	end := lineNumber + 3
	if end > len(lines) {
		end = len(lines)
	}
	var excerpt strings.Builder
	for i := start; i <= end; i++ {
		marker := "  "
		if i == lineNumber {
			marker = "→ "
		}
		excerpt.WriteString(fmt.Sprintf("%s%4d| %s\n", marker, i, visualizeWhitespace(lines[i-1])))
	}

	hint := ""
	if pathHint != "" {
		hint = " in " + pathHint
	}
	return fmt.Sprintf(
		"%s\n\nDiagnostic: a similar line exists%s near line %d. Common causes: indentation/whitespace mismatch, tab vs space, or trailing whitespace. File excerpt (· = space, → = tab, ¶ = end of line):\n\n%s\nCopy the exact bytes between · markers from the file (or re-read with `view`) before retrying.",
		baseMsg, hint, lineNumber, excerpt.String(),
	)
}

// multipleMatchesDiagnostic explains how to disambiguate an ambiguous
// edit by showing a short surrounding excerpt for each match.
func multipleMatchesDiagnostic(content, oldString string) string {
	const baseMsg = "old_string appears multiple times. Provide more surrounding context to make it unique, or set replace_all=true."
	if oldString == "" {
		return baseMsg
	}
	firstLine, _, _ := strings.Cut(oldString, "\n")
	trimmed := strings.TrimSpace(firstLine)
	if trimmed == "" {
		return baseMsg
	}
	// Walk the file collecting up to 3 line numbers where the snippet
	// occurs. More than that is overwhelming; the model only needs a few
	// neighbours to expand the context.
	var matches []int
	start := 0
	for i := 0; i < 3; i++ {
		idx := strings.Index(content[start:], trimmed)
		if idx < 0 {
			break
		}
		absIdx := start + idx
		matches = append(matches, strings.Count(content[:absIdx], "\n")+1)
		start = absIdx + len(trimmed)
	}
	if len(matches) == 0 {
		return baseMsg
	}
	lineList := make([]string, len(matches))
	for i, ln := range matches {
		lineList[i] = fmt.Sprintf("line %d", ln)
	}
	return fmt.Sprintf("%s\n\nDiagnostic: matches at %s. Add one or two lines of unique surrounding context to your old_string to disambiguate.", baseMsg, strings.Join(lineList, ", "))
}

func truncateForMsg(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// visualizeWhitespace makes leading whitespace and tabs visible so the
// model can see exactly what the file has versus what its old_string had.
func visualizeWhitespace(line string) string {
	var sb strings.Builder
	leadingDone := false
	for _, r := range line {
		if !leadingDone {
			switch r {
			case ' ':
				sb.WriteRune('·')
				continue
			case '\t':
				sb.WriteRune('→')
				continue
			default:
				leadingDone = true
			}
		}
		sb.WriteRune(r)
	}
	sb.WriteRune('¶')
	return sb.String()
}
