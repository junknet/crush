package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// NimRestartToolMessageItem is a message item that represents a lsprestart tool call.
type NimRestartToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*NimRestartToolMessageItem)(nil)

// NewNimRestartToolMessageItem creates a new [NimRestartToolMessageItem].
func NewNimRestartToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &NimRestartToolRenderContext{}, canceled)
}

// NimRestartToolRenderContext renders lsprestart tool messages.
type NimRestartToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *NimRestartToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, "Restart LSP", opts.Compact)
	}

	var params tools.NimRestartParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	var toolParams []string
	if params.Name != "" {
		toolParams = append(toolParams, params.Name)
	}

	header := toolHeader(sty, opts.Status, "Restart LSP", cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	return joinToolParts(header, body)
}
