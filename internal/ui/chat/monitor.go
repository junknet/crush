package chat

import (
	"encoding/json"
	"fmt"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// MonitorToolMessageItem renders monitor tool calls as status-only rows.
type MonitorToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*MonitorToolMessageItem)(nil)

// NewMonitorToolMessageItem creates a monitor tool message item.
func NewMonitorToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &MonitorToolRenderContext{}, canceled)
}

// MonitorToolRenderContext renders monitor tool messages without echoing the
// long automatic-continuation instructions returned to the model.
type MonitorToolRenderContext struct{}

// RenderTool implements the ToolRenderer interface.
func (m *MonitorToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, "Monitor", opts.Compact)
	}

	var params tools.MonitorParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	mainParam := fmt.Sprintf("job %s", params.ShellID)
	toolParams := []string{mainParam}
	if params.Pattern != "" {
		toolParams = append(toolParams, "pattern", params.Pattern)
	}
	if params.TimeoutSeconds > 0 {
		toolParams = append(toolParams, "timeout", fmt.Sprintf("%ds", params.TimeoutSeconds))
	}

	header := toolHeader(sty, opts.Status, "Monitor", cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}
	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}
	return header
}
