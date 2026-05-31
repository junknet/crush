package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// Search Tool
// -----------------------------------------------------------------------------

// SearchToolMessageItem is a message item that represents a search tool call.
type SearchToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*SearchToolMessageItem)(nil)

// NewSearchToolMessageItem creates a new [SearchToolMessageItem].
func NewSearchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &SearchToolRenderContext{}, canceled)
}

// SearchToolRenderContext renders search tool messages.
type SearchToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (g *SearchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, tools.SearchToolName, opts.Compact)
	}

	var params tools.SearchParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	toolParams := []string{params.Pattern}
	if params.Mode != "" {
		toolParams = append(toolParams, "mode", params.Mode)
	}
	if params.Path != "" {
		toolParams = append(toolParams, "path", params.Path)
	}
	if params.Include != "" {
		toolParams = append(toolParams, "include", params.Include)
	}
	if params.Literal {
		toolParams = append(toolParams, "literal", "true")
	}
	if params.FilesOnly {
		toolParams = append(toolParams, "files_only", "true")
	}

	header := toolHeader(sty, opts.Status, tools.SearchToolName, cappedWidth, opts.Compact, toolParams...)
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

// -----------------------------------------------------------------------------
// LS Tool
// -----------------------------------------------------------------------------

// LSToolMessageItem is a message item that represents an ls tool call.
type LSToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*LSToolMessageItem)(nil)

// NewLSToolMessageItem creates a new [LSToolMessageItem].
func NewLSToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &LSToolRenderContext{}, canceled)
}

// LSToolRenderContext renders ls tool messages.
type LSToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (l *LSToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, tools.LSToolName, opts.Compact)
	}

	var params tools.LSParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	path := params.Path
	if path == "" {
		path = "."
	}
	path = fsext.PrettyPath(path)

	header := toolHeader(sty, opts.Status, tools.LSToolName, cappedWidth, opts.Compact, path)
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

// -----------------------------------------------------------------------------
// Sourcegraph Tool
// -----------------------------------------------------------------------------

// SourcegraphToolMessageItem is a message item that represents a sourcegraph tool call.
type SourcegraphToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*SourcegraphToolMessageItem)(nil)

// NewSourcegraphToolMessageItem creates a new [SourcegraphToolMessageItem].
func NewSourcegraphToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &SourcegraphToolRenderContext{}, canceled)
}

// SourcegraphToolRenderContext renders sourcegraph tool messages.
type SourcegraphToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (s *SourcegraphToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, "Sourcegraph", opts.Compact)
	}

	var params tools.SourcegraphParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	toolParams := []string{params.Query}
	if params.Count != 0 {
		toolParams = append(toolParams, "count", formatNonZero(params.Count))
	}
	if params.ContextWindow != 0 {
		toolParams = append(toolParams, "context", formatNonZero(params.ContextWindow))
	}

	header := toolHeader(sty, opts.Status, "Sourcegraph", cappedWidth, opts.Compact, toolParams...)
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
