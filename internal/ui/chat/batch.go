package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

var (
	batchProgressMu sync.RWMutex
	batchProgresses = make(map[string]tools.BatchProgress) // ToolCallID -> BatchProgress
)

func UpdateBatchProgress(p tools.BatchProgress) {
	batchProgressMu.Lock()
	defer batchProgressMu.Unlock()
	batchProgresses[p.ToolCallID] = p
}

func GetBatchProgress(toolCallID string) (tools.BatchProgress, bool) {
	batchProgressMu.RLock()
	defer batchProgressMu.RUnlock()
	p, ok := batchProgresses[toolCallID]
	return p, ok
}

// BatchToolMessageItem represents the message item for Batch execution.
type BatchToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*BatchToolMessageItem)(nil)

func NewBatchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &BatchToolRenderContext{}, canceled)
}

type BatchToolRenderContext struct{}

func (b *BatchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		if p, ok := GetBatchProgress(opts.ToolCall.ID); ok {
			lines := []string{
				toolHeader(sty, opts.Status, tools.EvidenceBatchToolName, cappedWidth, opts.Compact, fmt.Sprintf("%d/%d completed", p.Completed, p.Total)),
			}
			if !opts.Compact {
				bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
				_ = bodyWidth
				subcallLines := []string{}
				for _, sub := range p.Subcalls {
					icon := "•"
					stateStyle := sty.Tool.AgentPrompt
					if sub.State == tools.BatchSubcallSucceeded {
						icon = "✓"
						stateStyle = sty.Tool.IconSuccess
					} else if sub.State == tools.BatchSubcallFailed {
						icon = "✗"
						stateStyle = sty.Tool.IconError
					}
					line := fmt.Sprintf("  %s %s: %s", stateStyle.Render(icon), sub.Name, sub.ID)
					subcallLines = append(subcallLines, line)
				}
				body := sty.Tool.Body.Render(strings.Join(subcallLines, "\n"))
				return joinToolParts(lines[0], body)
			}
			return lines[0]
		}
		return pendingTool(sty, tools.EvidenceBatchToolName, opts.Compact)
	}

	header := toolHeader(sty, opts.Status, tools.EvidenceBatchToolName, cappedWidth, opts.Compact)
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

	var resp struct {
		Nodes []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Kind   string `json:"kind"`
			Error  string `json:"error"`
		} `json:"nodes"`
		Summary struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
			Failed    int `json:"failed"`
		} `json:"summary"`
	}

	if opts.Result != nil && opts.Result.Metadata != "" {
		if err := json.Unmarshal([]byte(opts.Result.Metadata), &resp); err == nil && len(resp.Nodes) > 0 {
			lines := []string{}
			for _, node := range resp.Nodes {
				icon := "✓"
				stateStyle := sty.Tool.IconSuccess
				if node.Status != "completed" {
					icon = "✗"
					stateStyle = sty.Tool.IconError
				}
				line := fmt.Sprintf("  %s %s: %s", stateStyle.Render(icon), node.Kind, node.ID)
				if node.Error != "" {
					line += fmt.Sprintf(" (error: %s)", node.Error)
				}
				lines = append(lines, line)
			}
			lines = append(lines, "")
			lines = append(lines, fmt.Sprintf("  Completed %d/%d nodes. Failed %d.", resp.Summary.Completed, resp.Summary.Total, resp.Summary.Failed))

			body := sty.Tool.Body.Render(strings.Join(lines, "\n"))
			return joinToolParts(header, body)
		}
	}

	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	return joinToolParts(header, body)
}
