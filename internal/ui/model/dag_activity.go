package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const (
	dagActivityMinWidth = 24
	dagActivityMinRows  = 4
	dagActivityMaxTasks = 8
	dagActivityMaxTools = 10
)

type dagActivityToolInput struct {
	Kind    string                     `json:"kind"`
	Path    string                     `json:"path"`
	Pattern string                     `json:"pattern"`
	Query   string                     `json:"query"`
	Command string                     `json:"command"`
	Nodes   []dagActivityToolInputNode `json:"nodes"`
}

type dagActivityToolInputNode struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Query   string `json:"query"`
	Command string `json:"command"`
}

func (m *UI) drawDagActivity(scr uv.Screen, area uv.Rectangle) {
	if area.Dx() < dagActivityMinWidth || area.Dy() < dagActivityMinRows {
		return
	}
	view := m.dagActivityView(area.Dx(), area.Dy())
	uv.NewStyledString(view).Draw(scr, area)
}

func (m *UI) dagActivityView(width, height int) string {
	s := m.com.Styles
	innerWidth := max(1, width-s.CompactDetails.View.GetHorizontalFrameSize())
	innerHeight := max(1, height-s.CompactDetails.View.GetVerticalFrameSize())

	lines := make([]string, 0, innerHeight)
	title := s.CompactDetails.Title.Width(innerWidth).Render("DAG Activity")
	lines = append(lines, title)

	if summary := m.dagActivitySummaryLine(innerWidth); summary != "" {
		lines = append(lines, s.Resource.AdditionalText.Render(summary))
	}

	taskLines := m.dagActivityTaskLines(innerWidth, max(0, innerHeight-len(lines)-1))
	toolBudget := max(0, innerHeight-len(lines)-len(taskLines)-1)
	toolLines := m.dagActivityToolLines(innerWidth, toolBudget)

	if len(taskLines) > 0 {
		lines = append(lines, taskLines...)
	}
	if len(toolLines) > 0 {
		lines = append(lines, toolLines...)
	}
	if len(lines) <= 2 {
		lines = append(lines, s.Resource.AdditionalText.Render("No active DAG or tool telemetry yet."))
	}
	if len(lines) > innerHeight {
		lines = lines[:innerHeight]
	}

	body := lipgloss.JoinVertical(lipgloss.Left, lines...)
	body = lipgloss.NewStyle().Width(innerWidth).Height(innerHeight).Render(body)
	return s.CompactDetails.View.Width(width).Height(height).Render(body)
}

func (m *UI) dagActivitySummaryLine(width int) string {
	taskSummary := summarizeTaskRuntimeEvents(m.taskRuntimeEvents)
	toolSummary := summarizeToolRuntimeTraces(m.toolRuntimeEvents)
	parts := make([]string, 0, 4)
	if taskSummary.total > 0 {
		switch {
		case taskSummary.running > 0:
			parts = append(parts, fmt.Sprintf("dag %d/%d running", taskSummary.running, taskSummary.total))
		case taskSummary.planned > 0:
			parts = append(parts, fmt.Sprintf("dag %d/%d planned", taskSummary.planned, taskSummary.total))
		case taskSummary.failed > 0:
			parts = append(parts, fmt.Sprintf("dag %d/%d failed", taskSummary.failed, taskSummary.total))
		default:
			parts = append(parts, fmt.Sprintf("dag %d done", taskSummary.done))
		}
	}
	if toolSummary.running > 0 || toolSummary.failed > 0 {
		parts = append(parts, fmt.Sprintf("tools %d running", toolSummary.running))
		if toolSummary.failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", toolSummary.failed))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ansi.Truncate(strings.Join(parts, " · "), width, "…")
}

func (m *UI) dagActivityTaskLines(width, budget int) []string {
	if budget <= 1 || len(m.taskRuntimeEvents) == 0 {
		return nil
	}
	events := make([]scheduler.Event, 0, len(m.taskRuntimeEvents))
	for _, ev := range m.taskRuntimeEvents {
		events = append(events, ev)
	}
	slices.SortFunc(events, func(left, right scheduler.Event) int {
		if taskActivityRank(left.Kind) != taskActivityRank(right.Kind) {
			return taskActivityRank(left.Kind) - taskActivityRank(right.Kind)
		}
		return right.RecordedAt.Compare(left.RecordedAt)
	})

	lines := []string{m.com.Styles.Resource.Heading.Width(width).Render("Agents")}
	maxRows := min(dagActivityMaxTasks, budget-1)
	for _, ev := range events {
		if len(lines)-1 >= maxRows {
			break
		}
		lines = append(lines, renderDagTaskLine(ev, width))
		if detail := renderDagTaskDetail(ev, width); detail != "" && len(lines)-1 < maxRows {
			lines = append(lines, detail)
		}
	}
	return lines
}

func taskActivityRank(kind scheduler.EventKind) int {
	switch kind {
	case scheduler.EventTaskStarted, scheduler.EventTaskProgress:
		return 0
	case scheduler.EventTaskPlanned:
		return 1
	case scheduler.EventTaskFailed:
		return 2
	case scheduler.EventTaskFinished:
		return 3
	default:
		return 4
	}
}

func renderDagTaskLine(ev scheduler.Event, width int) string {
	role := subAgentRole(string(ev.Profile))
	status := dagTaskStatusText(ev.Kind)
	duration := elapsedSinceText(ev.RecordedAt)
	line := strings.TrimSpace(fmt.Sprintf("%s %s %s %s", dagTaskIcon(ev.Kind), role, status, duration))
	return ansi.Truncate(line, width, "…")
}

func renderDagTaskDetail(ev scheduler.Event, width int) string {
	text := firstNonEmpty(ev.Error, ev.Output, ev.Goal)
	if text == "" && len(ev.Scope) > 0 {
		text = strings.Join(ev.Scope, ", ")
	}
	text = compactSubAgentText(text)
	if text == "" {
		return ""
	}
	return ansi.Truncate("  "+text, width, "…")
}

func dagTaskIcon(kind scheduler.EventKind) string {
	switch kind {
	case scheduler.EventTaskStarted, scheduler.EventTaskProgress:
		return "•"
	case scheduler.EventTaskFinished:
		return "✓"
	case scheduler.EventTaskFailed:
		return "×"
	case scheduler.EventTaskPlanned:
		return "○"
	default:
		return "?"
	}
}

func dagTaskStatusText(kind scheduler.EventKind) string {
	switch kind {
	case scheduler.EventTaskStarted, scheduler.EventTaskProgress:
		return "running"
	case scheduler.EventTaskFinished:
		return "done"
	case scheduler.EventTaskFailed:
		return "failed"
	case scheduler.EventTaskPlanned:
		return "planned"
	default:
		return string(kind)
	}
}

func (m *UI) dagActivityToolLines(width, budget int) []string {
	if budget <= 1 || len(m.toolRuntimeEvents) == 0 {
		return nil
	}
	traces := make([]agentruntime.TaskTrace, 0, len(m.toolRuntimeEvents))
	for _, trace := range m.toolRuntimeEvents {
		traces = append(traces, trace)
	}
	slices.SortFunc(traces, func(left, right agentruntime.TaskTrace) int {
		if toolActivityRank(left.Kind) != toolActivityRank(right.Kind) {
			return toolActivityRank(left.Kind) - toolActivityRank(right.Kind)
		}
		return right.RecordedAt.Compare(left.RecordedAt)
	})

	lines := []string{m.com.Styles.Resource.Heading.Width(width).Render("Tools")}
	maxRows := min(dagActivityMaxTools, budget-1)
	for _, trace := range traces {
		if len(lines)-1 >= maxRows {
			break
		}
		lines = append(lines, renderDagToolLine(trace, width))
		if detail := renderDagToolDetail(trace, width); detail != "" && len(lines)-1 < maxRows {
			lines = append(lines, detail)
		}
	}
	return lines
}

func toolActivityRank(kind agentruntime.TraceKind) int {
	switch kind {
	case agentruntime.TraceKindToolStarted:
		return 0
	case agentruntime.TraceKindToolFailed:
		return 1
	case agentruntime.TraceKindToolFinished:
		return 2
	default:
		return 3
	}
}

func renderDagToolLine(trace agentruntime.TaskTrace, width int) string {
	name := trace.ToolName
	if name == "" {
		name = "tool"
	}
	status := dagToolStatusText(trace.Kind)
	duration := dagToolDurationText(trace)
	line := strings.TrimSpace(fmt.Sprintf("%s %s %s %s", dagToolIcon(trace.Kind), name, status, duration))
	return ansi.Truncate(line, width, "…")
}

func renderDagToolDetail(trace agentruntime.TaskTrace, width int) string {
	text := firstNonEmpty(trace.Error, summarizeDagToolInput(trace), trace.ToolOutput)
	text = compactSubAgentText(text)
	if text == "" {
		return ""
	}
	return ansi.Truncate("  "+text, width, "…")
}

func summarizeDagToolInput(trace agentruntime.TaskTrace) string {
	if trace.ToolInput == "" {
		return ""
	}
	var input dagActivityToolInput
	if err := json.Unmarshal([]byte(trace.ToolInput), &input); err != nil {
		return trace.ToolInput
	}
	if len(input.Nodes) > 0 {
		kinds := make([]string, 0, len(input.Nodes))
		for _, node := range input.Nodes {
			if node.Kind != "" && !slices.Contains(kinds, node.Kind) {
				kinds = append(kinds, node.Kind)
			}
		}
		if len(kinds) == 0 {
			return fmt.Sprintf("nodes=%d", len(input.Nodes))
		}
		return fmt.Sprintf("nodes=%d %s", len(input.Nodes), strings.Join(kinds, "/"))
	}
	parts := make([]string, 0, 3)
	if input.Kind != "" {
		parts = append(parts, input.Kind)
	}
	if target := firstNonEmpty(input.Path, input.Pattern, input.Query, input.Command); target != "" {
		parts = append(parts, target)
	}
	return strings.Join(parts, " ")
}

func dagToolIcon(kind agentruntime.TraceKind) string {
	switch kind {
	case agentruntime.TraceKindToolStarted:
		return "•"
	case agentruntime.TraceKindToolFinished:
		return "✓"
	case agentruntime.TraceKindToolFailed:
		return "×"
	default:
		return "?"
	}
}

func dagToolStatusText(kind agentruntime.TraceKind) string {
	switch kind {
	case agentruntime.TraceKindToolStarted:
		return "running"
	case agentruntime.TraceKindToolFinished:
		return "done"
	case agentruntime.TraceKindToolFailed:
		return "failed"
	default:
		return string(kind)
	}
}

func dagToolDurationText(trace agentruntime.TaskTrace) string {
	if trace.DurationMs > 0 {
		return formatActivityDuration(time.Duration(trace.DurationMs) * time.Millisecond)
	}
	if trace.StartedAt.IsZero() {
		return elapsedSinceText(trace.RecordedAt)
	}
	end := trace.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	return formatActivityDuration(end.Sub(trace.StartedAt))
}

func elapsedSinceText(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return formatActivityDuration(time.Since(at))
}

func formatActivityDuration(elapsed time.Duration) string {
	if elapsed < 0 {
		return ""
	}
	switch {
	case elapsed < time.Second:
		return fmt.Sprintf("%dms", elapsed.Milliseconds())
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	}
}
