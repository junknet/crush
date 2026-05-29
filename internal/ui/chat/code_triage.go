package chat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// -------------------------------------------------------------------
// Code Triage Tool
// -------------------------------------------------------------------

// CodeTriageToolMessageItem is a message item that represents a code triage tool call.
type CodeTriageToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*CodeTriageToolMessageItem)(nil)

// NewCodeTriageToolMessageItem creates a new [CodeTriageToolMessageItem].
func NewCodeTriageToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &CodeTriageToolRenderContext{}, canceled)
}

// CodeTriageToolRenderContext renders code triage tool messages.
type CodeTriageToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (c *CodeTriageToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		var params tools.CodeTriageParams
		header := "Code Triage"
		if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err == nil {
			if len(params.Queries) > 0 && len(params.CheckCommands) > 0 {
				header = fmt.Sprintf("Code Triage · %d queries, %d checks", len(params.Queries), len(params.CheckCommands))
			} else if len(params.Queries) > 0 {
				header = fmt.Sprintf("Code Triage · %d queries", len(params.Queries))
			} else if len(params.CheckCommands) > 0 {
				header = fmt.Sprintf("Code Triage · %d checks", len(params.CheckCommands))
			}
		}
		return pendingTool(sty, header, opts.Compact)
	}

	var params tools.CodeTriageParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	paramsSummary := []string{codeTriageShortDescription(params)}
	if len(params.Queries) > 0 {
		paramsSummary = append(paramsSummary, "queries", fmt.Sprintf("%d", len(params.Queries)))
	}
	if len(params.CheckCommands) > 0 {
		paramsSummary = append(paramsSummary, "checks", fmt.Sprintf("%d", len(params.CheckCommands)))
	}

	header := toolHeader(sty, opts.Status, "Code Triage", cappedWidth, opts.Compact, paramsSummary...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() {
		return header
	}

	var meta tools.CodeTriageResponseMetadata
	if opts.Result.Metadata == "" || json.Unmarshal([]byte(opts.Result.Metadata), &meta) != nil {
		bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
		body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
		return joinToolParts(header, body)
	}

	body := renderCodeTriageResult(sty, meta, cappedWidth)
	if body == "" {
		return header
	}
	body = sty.Tool.Body.Render(toolOutputPlainContent(sty, body, cappedWidth-toolBodyLeftPaddingTotal, opts.ExpandedContent))
	return joinToolParts(header, body)
}

func codeTriageShortDescription(params tools.CodeTriageParams) string {
	if params.MaxResults > 0 {
		return "summary"
	}
	return "triage"
}

func renderCodeTriageResult(sty *styles.Styles, meta tools.CodeTriageResponseMetadata, _ int) string {
	if meta.DurationMs == 0 && meta.TotalMatches == 0 && len(meta.Queries) == 0 && len(meta.Checks) == 0 {
		return "No findings returned."
	}

	lines := []string{
		meta.Summary,
	}
	if meta.Guidance.NextAction != "" {
		lines = append(lines, "Next: "+truncateCodeTriageBodyLine(meta.Guidance.NextAction))
	}
	if meta.Guidance.PrimaryFile != "" {
		focus := fmt.Sprintf("Focus: %s:%d", filepath.Base(meta.Guidance.PrimaryFile), meta.Guidance.PrimaryLine)
		if meta.Guidance.PrimaryMessage != "" {
			focus += " " + truncateCodeTriageBodyLine(meta.Guidance.PrimaryMessage)
		}
		lines = append(lines, focus)
	}

	if len(meta.Queries) > 0 {
		lines = append(lines, "Queries:")
		for _, q := range meta.Queries {
			outcome := q.Outcome
			if outcome == "" {
				outcome = "passed"
			}
			qLine := fmt.Sprintf("  %s [%s] (%s, %d matches)", q.ID, outcome, filepath.Base(q.Path), q.Matches)
			if q.Truncated {
				qLine += ", truncated"
			}
			lines = append(lines, qLine)
			if q.Error != "" {
				lines = append(lines, "    ERROR "+truncateCodeTriageBodyLine(q.Error))
				continue
			}
			for _, m := range q.TopMatches {
				lines = append(lines, formatCodeTriageQueryFinding(sty, m))
			}
			if len(q.TopMatches) > 0 {
				lines = append(lines, "")
			}
		}
	}

	if len(meta.Checks) > 0 {
		lines = append(lines, "Checks:")
		for _, c := range meta.Checks {
			header := fmt.Sprintf("  %s [%s] exit=%d duration=%dms", c.Name, c.Outcome, c.ExitCode, c.DurationMs)
			if c.ExitCode != 0 {
				header += fmt.Sprintf(" (%d)", c.ExitCode)
			}
			lines = append(lines, header)

			if len(c.Findings) > 0 {
				for _, f := range c.Findings {
					lines = append(lines, formatCodeTriageFinding(sty, f))
				}
			} else if c.Output != "" {
				lines = append(lines, "    "+strings.TrimSpace(truncateCodeTriageBodyLine(c.Output)))
			}
			lines = append(lines, "")
		}
	}

	// Remove any trailing empty spacer line.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func formatCodeTriageQueryFinding(sty *styles.Styles, finding tools.CodeTriageQueryFinding) string {
	snippet := truncateCodeTriageBodyLine(finding.Snippet)
	prefix := fmt.Sprintf("    %s:%d:%d", filepath.Base(finding.File), finding.Line, finding.Column)
	if finding.Column == 0 {
		prefix = fmt.Sprintf("    %s:%d", filepath.Base(finding.File), finding.Line)
	}
	return prefix + " " + strings.TrimSpace(sty.Tool.ContentLine.Render(snippet))
}

func formatCodeTriageFinding(sty *styles.Styles, finding tools.CodeTriageFinding) string {
	location := ""
	if finding.File != "" {
		if finding.Line != 0 {
			location = fmt.Sprintf("%s:%d", filepath.Base(finding.File), finding.Line)
			if finding.Column != 0 {
				location = fmt.Sprintf("%s:%d", location, finding.Column)
			}
		} else {
			location = filepath.Base(finding.File)
		}
	}
	if location != "" {
		location = " " + location
	}
	return fmt.Sprintf("    %s%s %s", strings.ToUpper(finding.Severity), location, strings.TrimSpace(sty.Tool.ContentLine.Render(truncateCodeTriageBodyLine(finding.Message))))
}

func truncateCodeTriageBodyLine(content string) string {
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.TrimSpace(content)
	const limit = 170
	if len(content) <= limit {
		return content
	}
	return content[:limit] + "…"
}
