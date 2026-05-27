package model

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// pillStyle returns the appropriate style for a pill based on focus state.
func pillStyle(focused, panelFocused bool, t *styles.Styles) lipgloss.Style {
	if !panelFocused || focused {
		return t.Pills.Focused
	}
	return t.Pills.Blurred
}

const (
	// pillHeightWithBorder is the height of a pill including its border.
	pillHeightWithBorder = 3
	// maxTaskDisplayLength is the maximum length of a task name in the pill.
	maxTaskDisplayLength = 40
	// maxQueueDisplayLength is the maximum length of a queue item in the list.
	maxQueueDisplayLength = 60
	// maxExpandedPillRows is the maximum number of body rows shown under the
	// footer pills before the rest is summarized.
	maxExpandedPillRows = 10
)

// pillSection represents which section of the pills panel is focused.
type pillSection int

const (
	pillSectionTodos pillSection = iota
	pillSectionQueue
)

// hasIncompleteTodos returns true if there are any non-completed todos.
func hasIncompleteTodos(todos []session.Todo) bool {
	return session.HasIncompleteTodos(todos)
}

// hasActiveTodos returns true if there are any non-completed todos
// or any recently completed todos.
func hasActiveTodos(todos []session.Todo) bool {
	if hasIncompleteTodos(todos) {
		return true
	}
	now := time.Now().UnixMilli()
	for _, todo := range todos {
		if todo.Status == session.TodoStatusCompleted && todo.CompletedAt > 0 {
			if now-todo.CompletedAt < chat.TodoRecentlyCompletedTTL.Milliseconds() {
				return true
			}
		}
	}
	return false
}

// hasInProgressTodo returns true if there is at least one in-progress todo.
func hasInProgressTodo(todos []session.Todo) bool {
	for _, todo := range todos {
		if todo.Status == session.TodoStatusInProgress {
			return true
		}
	}
	return false
}

// queuePill renders the queue count pill with gradient triangles.
func queuePill(queue int, focused, panelFocused bool, t *styles.Styles) string {
	if queue <= 0 {
		return ""
	}
	triangles := styles.ForegroundGrad(t.Pills.QueueIconBase, "▶▶▶▶▶▶▶▶▶", false, t.Pills.QueueGradFromColor, t.Pills.QueueGradToColor)
	if queue < len(triangles) {
		triangles = triangles[:queue]
	}

	text := t.Pills.QueueLabel.Render(fmt.Sprintf("%d Queued", queue))
	content := fmt.Sprintf("%s %s", strings.Join(triangles, ""), text)
	return pillStyle(focused, panelFocused, t).Render(content)
}

// todoPill renders the todo progress pill with the current task name.
func todoPill(todos []session.Todo, inProgressIcon string, focused, panelFocused bool, t *styles.Styles) string {
	if !hasActiveTodos(todos) {
		return ""
	}

	completed := 0
	failed := 0
	var currentTodo *session.Todo
	for i := range todos {
		switch todos[i].Status {
		case session.TodoStatusCompleted:
			completed++
		case session.TodoStatusFailed:
			failed++
		case session.TodoStatusInProgress:
			if currentTodo == nil {
				currentTodo = &todos[i]
			}
		}
	}

	total := len(todos)

	label := t.Pills.TodoLabel.Render("To-Do")
	statusText := fmt.Sprintf("%d/%d", completed, total)
	if failed > 0 {
		statusText += fmt.Sprintf(" (%d failed)", failed)
	}
	progress := t.Pills.TodoProgress.Render(statusText)

	var content string
	if panelFocused {
		content = fmt.Sprintf("%s %s", label, progress)
	} else if currentTodo != nil {
		taskText := currentTodo.Content
		if currentTodo.ActiveForm != "" {
			taskText = currentTodo.ActiveForm
		}
		task := ansi.Truncate(t.Pills.TodoCurrentTask.Render(taskText), maxTaskDisplayLength, "…")
		content = fmt.Sprintf("%s %s %s  %s", inProgressIcon, label, progress, task)
	} else {
		content = fmt.Sprintf("%s %s", label, progress)
	}

	return pillStyle(focused, panelFocused, t).Render(content)
}

// todoList renders the expanded todo list.
func todoList(sessionTodos []session.Todo, t *styles.Styles, width int, maxRows int) string {
	return chat.FormatTodosListWithLimit(t, sessionTodos, width, maxRows)
}

// queueList renders the expanded queue items list.
func queueList(queueItems []string, t *styles.Styles, maxRows int) string {
	if len(queueItems) == 0 {
		return ""
	}
	if maxRows <= 0 {
		return ""
	}

	var lines []string
	visibleItems := min(len(queueItems), maxRows)
	if len(queueItems) > maxRows {
		visibleItems = maxRows - 1
	}
	for _, item := range queueItems[:visibleItems] {
		text := item
		if len(text) > maxQueueDisplayLength {
			text = text[:maxQueueDisplayLength-1] + "…"
		}
		prefix := t.Pills.QueueItemPrefix.Render() + " "
		lines = append(lines, prefix+t.Pills.QueueItemText.Render(text))
	}
	if hidden := len(queueItems) - visibleItems; hidden > 0 {
		lines = append(lines, t.Pills.QueueItemText.Render(fmt.Sprintf("  … +%d queued", hidden)))
	}

	return strings.Join(lines, "\n")
}

// togglePillsExpanded toggles the pills panel expansion state.
func (m *UI) togglePillsExpanded() tea.Cmd {
	if !m.hasSession() {
		return nil
	}
	hasPills := hasActiveTodos(m.session.Todos) || m.promptQueue > 0
	if !hasPills {
		return nil
	}
	m.pillsExpanded = !m.pillsExpanded
	if m.pillsExpanded {
		if hasIncompleteTodos(m.session.Todos) {
			m.focusedPillSection = pillSectionTodos
		} else {
			m.focusedPillSection = pillSectionQueue
		}
	}
	m.updateLayoutAndSize()

	// Make sure to follow scroll if follow is enabled when toggling pills.
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}

	return nil
}

// switchPillSection changes focus between todo and queue sections.
func (m *UI) switchPillSection(dir int) tea.Cmd {
	if !m.pillsExpanded || !m.hasSession() {
		return nil
	}
	hasActive := hasActiveTodos(m.session.Todos)
	hasQueue := m.promptQueue > 0

	if dir < 0 && m.focusedPillSection == pillSectionQueue && hasActive {
		m.focusedPillSection = pillSectionTodos
		m.updateLayoutAndSize()
		return nil
	}
	if dir > 0 && m.focusedPillSection == pillSectionTodos && hasQueue {
		m.focusedPillSection = pillSectionQueue
		m.updateLayoutAndSize()
		return nil
	}
	return nil
}

// pillsAreaHeight calculates the total height needed for the pills area.
func (m *UI) pillsAreaHeight() int {
	if !m.hasSession() {
		return 0
	}
	hasActive := hasActiveTodos(m.session.Todos)
	hasQueue := m.promptQueue > 0
	hasPills := hasActive || hasQueue
	if !hasPills {
		return 0
	}

	pillsAreaHeight := pillHeightWithBorder
	if m.pillsExpanded {
		maxExpandedRows := m.maxExpandedPillRows()
		if m.focusedPillSection == pillSectionTodos && hasActive {
			pillsAreaHeight += m.renderedTodoListHeight(maxExpandedRows)
		} else if m.focusedPillSection == pillSectionQueue && hasQueue {
			pillsAreaHeight += min(m.promptQueue, maxExpandedRows)
		}
	}
	return pillsAreaHeight
}

func (m *UI) maxExpandedPillRows() int {
	if m.height <= 0 {
		return maxExpandedPillRows
	}
	if m.height <= 10 {
		return 0
	}
	return min(maxExpandedPillRows, max(3, m.height-14))
}

func (m *UI) renderedTodoListHeight(maxRows int) int {
	if maxRows <= 0 || m.com == nil || m.com.Styles == nil {
		return 0
	}
	contentWidth := max(m.width-3, 0)
	rendered := chat.FormatTodosListWithLimit(m.com.Styles, m.session.Todos, contentWidth, maxRows)
	if rendered == "" {
		return 0
	}
	return strings.Count(rendered, "\n") + 1
}

// renderPills renders the pills panel and stores it in m.pillsView.
func (m *UI) renderPills() {
	m.pillsView = ""
	if !m.hasSession() {
		return
	}

	width := m.layout.pills.Dx()
	if width <= 0 {
		return
	}

	paddingLeft := 3
	contentWidth := max(width-paddingLeft, 0)

	hasActive := hasActiveTodos(m.session.Todos)
	hasQueue := m.promptQueue > 0

	if !hasActive && !hasQueue {
		return
	}

	t := m.com.Styles
	todosFocused := m.pillsExpanded && m.focusedPillSection == pillSectionTodos
	queueFocused := m.pillsExpanded && m.focusedPillSection == pillSectionQueue

	inProgressIcon := chat.RenderTodoInProgressIcon(t)

	var pills []string
	if hasActive {
		pills = append(pills, todoPill(m.session.Todos, inProgressIcon, todosFocused, m.pillsExpanded, t))
	}
	if hasQueue {
		pills = append(pills, queuePill(m.promptQueue, queueFocused, m.pillsExpanded, t))
	}

	var expandedList string
	maxExpandedRows := m.maxExpandedPillRows()
	if m.pillsExpanded {
		if todosFocused && hasActive {
			expandedList = todoList(m.session.Todos, t, contentWidth, maxExpandedRows)
		} else if queueFocused && hasQueue {
			if m.com.Workspace.AgentIsReady() {
				queueItems := m.com.Workspace.AgentQueuedPromptsList(m.session.ID)
				expandedList = queueList(queueItems, t, maxExpandedRows)
			}
		}
	}

	if len(pills) == 0 {
		return
	}

	pillsRow := lipgloss.JoinHorizontal(lipgloss.Top, pills...)

	helpDesc := "open"
	if m.pillsExpanded {
		helpDesc = "close"
	}
	helpKey := t.Pills.HelpKey.Render("ctrl+t")
	helpText := t.Pills.HelpText.Render(helpDesc)
	helpHint := lipgloss.JoinHorizontal(lipgloss.Center, helpKey, " ", helpText)
	pillsRow = lipgloss.JoinHorizontal(lipgloss.Center, pillsRow, " ", helpHint)

	pillsArea := pillsRow
	if expandedList != "" {
		// Indent the list to align with the pill labels (which have 1px border + 1px padding).
		indentedList := lipgloss.NewStyle().PaddingLeft(2).Render(expandedList)
		pillsArea = lipgloss.JoinVertical(lipgloss.Left, pillsRow, indentedList)
	}

	m.pillsView = t.Pills.Area.MaxWidth(width).PaddingLeft(paddingLeft).Render(pillsArea)
}
