package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// RunToolMessageItem is a message item for short script execution.
type RunToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*RunToolMessageItem)(nil)

// NewRunToolMessageItem creates a new [RunToolMessageItem].
func NewRunToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &RunToolRenderContext{}, canceled)
}

// RunToolRenderContext renders run tool messages.
type RunToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *RunToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, "Run", opts.Compact)
	}

	var params tools.RunParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	language := strings.TrimSpace(params.Language)
	if language == "" {
		language = "shell"
	}
	summary := runScriptSummary(params.Script, cappedWidth/2)
	toolParams := []string{summary, "language", language}
	if params.TimeoutSeconds > 0 {
		toolParams = append(toolParams, "timeout", formatSeconds(params.TimeoutSeconds))
	}
	if !opts.HasResult() && !opts.IsCanceled() {
		if elapsed := runningDurationText(opts.StartedAt); elapsed != "" {
			toolParams = append(toolParams, "elapsed", elapsed)
		}
	}

	header := toolHeader(sty, opts.Status, "Run", cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() || opts.Result.Content == "" {
		return header
	}

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	return joinToolParts(header, body)
}

func runScriptSummary(script string, width int) string {
	script = strings.ReplaceAll(script, "\t", " ")
	lines := strings.FieldsFunc(script, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if width > 0 {
			return ansi.Truncate(line, width, "...")
		}
		return line
	}
	return "script"
}

func formatSeconds(seconds int) string {
	return fmt.Sprintf("%ds", seconds)
}
