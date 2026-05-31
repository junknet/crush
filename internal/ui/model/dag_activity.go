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
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const (
	dagActivityMinWidth = 24
	dagActivityMinRows  = 4
	dagActivityMaxNodes = 14
	dagActivityMaxTodos = 8
)

// Status glyphs follow the free-code visual vocabulary: an open diamond for
// in-flight work, a filled diamond for completed, a multiplication sign for
// failure, and an open circle for not-yet-started. They are colored through
// the existing Resource icon styles so the panel tracks the active theme.
const (
	dagGlyphRunning = "◇"
	dagGlyphDone    = "◆"
	dagGlyphFailed  = "×"
	dagGlyphPlanned = "○"
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

// dagNode is a unified tree node merging scheduler task events and runtime tool
// traces by NodeID/ParentID so the panel can render the actual DAG hierarchy
// (brain → worker → tool) instead of two flat lists.
type dagNode struct {
	id         string
	parentID   string
	glyph      string
	style      lipgloss.Style
	label      string
	meta       string
	rank       int
	recordedAt time.Time
	children   []*dagNode
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
	lines = append(lines, s.CompactDetails.Title.Width(innerWidth).Render("ACTIVITY"))
	if summary := m.dagActivitySummaryLine(innerWidth); summary != "" {
		lines = append(lines, s.Resource.AdditionalText.Render(summary))
	}
	lines = append(lines, "")

	// Reserve room at the bottom for the TODO section so a long DAG tree
	// never starves the task list the user is steering by.
	visibleTodos := dagVisibleTodos(m.session)
	remaining := max(0, innerHeight-len(lines))
	todoReserve := 0
	if len(visibleTodos) > 0 {
		todoReserve = min(len(visibleTodos)+2, remaining/2+1) // +2 = divider + header
	}
	treeBudget := max(0, remaining-todoReserve)

	roots := m.buildDagTree()
	treeLines := renderDagTree(roots, innerWidth, min(dagActivityMaxNodes, treeBudget))
	if len(treeLines) == 0 {
		treeLines = []string{s.Resource.AdditionalText.Render("idle — no active agents")}
	}
	lines = append(lines, treeLines...)

	if todoReserve > 0 && len(lines) < innerHeight {
		lines = append(lines, "")
		lines = append(lines, s.Resource.AdditionalText.Render(strings.Repeat("─", min(innerWidth, 22))))
		todoBudget := max(0, innerHeight-len(lines))
		lines = append(lines, m.dagTodoLines(visibleTodos, innerWidth, todoBudget)...)
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
	parts := make([]string, 0, 3)
	if taskSummary.total > 0 {
		parts = append(parts, fmt.Sprintf("dag %d/%d", taskSummary.running, taskSummary.total))
	}
	if toolSummary.running > 0 || toolSummary.failed > 0 {
		tools := fmt.Sprintf("tools %d", toolSummary.running)
		if toolSummary.failed > 0 {
			tools += fmt.Sprintf(" (%d failed)", toolSummary.failed)
		}
		parts = append(parts, tools)
	}
	if len(parts) == 0 {
		return ""
	}
	return ansi.Truncate(strings.Join(parts, " · "), width, "…")
}

// buildDagTree merges task events and tool traces into a parent/child forest.
func (m *UI) buildDagTree() []*dagNode {
	s := m.com.Styles
	nodes := make(map[string]*dagNode, len(m.taskRuntimeEvents)+len(m.toolRuntimeEvents))

	for _, ev := range m.taskRuntimeEvents {
		if ev.NodeID == "" {
			continue
		}
		glyph, style := dagTaskGlyph(s, ev.Kind)
		nodes[ev.NodeID] = &dagNode{
			id:         ev.NodeID,
			parentID:   ev.ParentID,
			glyph:      glyph,
			style:      style,
			label:      subAgentRole(string(ev.Profile)),
			meta:       dagJoinMeta(dagTaskStatusText(ev.Kind), elapsedSinceText(ev.RecordedAt), compactSubAgentText(firstNonEmpty(ev.Error, ev.Goal))),
			rank:       taskActivityRank(ev.Kind),
			recordedAt: ev.RecordedAt,
		}
	}
	for _, tr := range m.toolRuntimeEvents {
		if tr.NodeID == "" {
			continue
		}
		if _, exists := nodes[tr.NodeID]; exists {
			continue // a task node already owns this id
		}
		name := tr.ToolName
		if name == "" {
			name = "tool"
		}
		glyph, style := dagToolGlyph(s, tr.Kind)
		nodes[tr.NodeID] = &dagNode{
			id:         tr.NodeID,
			parentID:   tr.ParentID,
			glyph:      glyph,
			style:      style,
			label:      name,
			meta:       dagJoinMeta(dagToolStatusText(tr.Kind), dagToolDurationText(tr), compactSubAgentText(firstNonEmpty(tr.Error, summarizeDagToolInput(tr)))),
			rank:       toolActivityRank(tr.Kind) + 10,
			recordedAt: tr.RecordedAt,
		}
	}

	var roots []*dagNode
	for _, n := range nodes {
		if parent, ok := nodes[n.parentID]; ok && n.parentID != "" && n.parentID != n.id {
			parent.children = append(parent.children, n)
		} else {
			roots = append(roots, n)
		}
	}
	sortDagNodes(roots)
	for _, n := range nodes {
		sortDagNodes(n.children)
	}
	return roots
}

func sortDagNodes(nodes []*dagNode) {
	slices.SortFunc(nodes, func(a, b *dagNode) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return b.recordedAt.Compare(a.recordedAt)
	})
}

// renderDagTree walks the forest depth-first, drawing ├ └ │ connectors so the
// nesting reads at a glance. Root nodes carry no connector prefix.
func renderDagTree(roots []*dagNode, width, budget int) []string {
	if budget <= 0 {
		return nil
	}
	out := make([]string, 0, budget)
	var walk func(n *dagNode, prefix string, isLast, isRoot bool)
	walk = func(n *dagNode, prefix string, isLast, isRoot bool) {
		if len(out) >= budget {
			return
		}
		branch := prefix
		childPrefix := prefix
		if !isRoot {
			if isLast {
				branch += "└ "
				childPrefix += "  "
			} else {
				branch += "├ "
				childPrefix += "│ "
			}
		}
		out = append(out, renderDagNodeLine(n, branch, width))
		for i, child := range n.children {
			walk(child, childPrefix, i == len(n.children)-1, false)
		}
	}
	for i, root := range roots {
		if len(out) >= budget {
			break
		}
		walk(root, "", i == len(roots)-1, true)
	}
	if len(out) > budget {
		out = out[:budget]
	}
	return out
}

// renderDagNodeLine lays out "<connector><glyph> <label>" on the left and the
// dot-joined metadata flush right, truncating the label first when space runs out.
func renderDagNodeLine(n *dagNode, branch string, width int) string {
	left := branch + n.style.Render(n.glyph) + " " + n.label
	leftWidth := ansi.StringWidth(left)
	if n.meta == "" || leftWidth+2 >= width {
		return ansi.Truncate(left, width, "…")
	}
	// Always keep the (right-aligned) metadata visible — truncate it to the
	// remaining space rather than dropping status+duration when the trailing
	// goal text is long.
	avail := width - leftWidth - 1
	meta := n.meta
	if ansi.StringWidth(meta) > avail {
		meta = ansi.Truncate(meta, avail, "…")
	}
	pad := max(1, width-leftWidth-ansi.StringWidth(meta))
	return left + strings.Repeat(" ", pad) + meta
}

func dagJoinMeta(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

// ---- TODO section -------------------------------------------------------

func dagVisibleTodos(sess *session.Session) []session.Todo {
	if sess == nil {
		return nil
	}
	return sess.Todos
}

func (m *UI) dagTodoLines(todos []session.Todo, width, budget int) []string {
	if budget <= 0 || len(todos) == 0 {
		return nil
	}
	s := m.com.Styles
	done := 0
	for _, td := range todos {
		if td.Status == session.TodoStatusCompleted {
			done++
		}
	}
	header := fmt.Sprintf("TODO %s %d/%d", dagProgressBar(done, len(todos), 5), done, len(todos))
	lines := []string{s.Resource.Heading.Width(width).Render(ansi.Truncate(header, width, "…"))}

	// Running/pending first so the actionable items stay visible; completed
	// drop to the bottom and get truncated under budget pressure.
	ordered := slices.Clone(todos)
	slices.SortStableFunc(ordered, func(a, b session.Todo) int {
		return todoStatusRank(a.Status) - todoStatusRank(b.Status)
	})

	maxRows := min(dagActivityMaxTodos, budget-1)
	for _, td := range ordered {
		if len(lines)-1 >= maxRows {
			break
		}
		glyph, style := dagTodoGlyph(s, td.Status)
		text := td.Content
		if td.Status == session.TodoStatusInProgress && strings.TrimSpace(td.ActiveForm) != "" {
			text = td.ActiveForm
		}
		line := style.Render(glyph) + " " + text
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return lines
}

func dagProgressBar(done, total, cells int) string {
	if total <= 0 || cells <= 0 {
		return strings.Repeat("░", max(0, cells))
	}
	filled := done * cells / total
	filled = max(0, min(cells, filled))
	return strings.Repeat("▓", filled) + strings.Repeat("░", cells-filled)
}

func todoStatusRank(st session.TodoStatus) int {
	switch st {
	case session.TodoStatusInProgress:
		return 0
	case session.TodoStatusPending:
		return 1
	case session.TodoStatusFailed:
		return 2
	case session.TodoStatusCompleted:
		return 3
	default:
		return 4
	}
}

func dagTodoGlyph(s *styles.Styles, st session.TodoStatus) (string, lipgloss.Style) {
	switch st {
	case session.TodoStatusCompleted:
		return dagGlyphDone, dagGlyphStyle(s.Resource.OnlineIcon)
	case session.TodoStatusInProgress:
		return dagGlyphRunning, dagGlyphStyle(s.Resource.BusyIcon)
	case session.TodoStatusFailed:
		return dagGlyphFailed, dagGlyphStyle(s.Resource.ErrorIcon)
	default:
		return dagGlyphPlanned, dagGlyphStyle(s.Resource.OfflineIcon)
	}
}

// dagGlyphStyle borrows only the foreground color from a Resource icon style.
// The Resource icon styles carry a fixed SetString("●"); rendering our diamond
// glyph through them would emit "● ◇", so we strip everything but the color.
func dagGlyphStyle(base lipgloss.Style) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(base.GetForeground())
}

// ---- glyph + status mappers --------------------------------------------

func dagTaskGlyph(s *styles.Styles, kind scheduler.EventKind) (string, lipgloss.Style) {
	switch kind {
	case scheduler.EventTaskStarted, scheduler.EventTaskProgress:
		return dagGlyphRunning, dagGlyphStyle(s.Resource.BusyIcon)
	case scheduler.EventTaskFinished:
		return dagGlyphDone, dagGlyphStyle(s.Resource.OnlineIcon)
	case scheduler.EventTaskFailed:
		return dagGlyphFailed, dagGlyphStyle(s.Resource.ErrorIcon)
	case scheduler.EventTaskPlanned:
		return dagGlyphPlanned, dagGlyphStyle(s.Resource.OfflineIcon)
	default:
		return dagGlyphPlanned, dagGlyphStyle(s.Resource.OfflineIcon)
	}
}

func dagToolGlyph(s *styles.Styles, kind agentruntime.TraceKind) (string, lipgloss.Style) {
	switch kind {
	case agentruntime.TraceKindToolStarted:
		return dagGlyphRunning, dagGlyphStyle(s.Resource.BusyIcon)
	case agentruntime.TraceKindToolFinished:
		return dagGlyphDone, dagGlyphStyle(s.Resource.OnlineIcon)
	case agentruntime.TraceKindToolFailed:
		return dagGlyphFailed, dagGlyphStyle(s.Resource.ErrorIcon)
	default:
		return dagGlyphPlanned, dagGlyphStyle(s.Resource.OfflineIcon)
	}
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
