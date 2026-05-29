package chat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// -----------------------------------------------------------------------------
// Todos Tool
// -----------------------------------------------------------------------------

// TodosToolMessageItem is a message item that represents a todos tool call.
type TodosToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*TodosToolMessageItem)(nil)

// NewTodosToolMessageItem creates a new [TodosToolMessageItem].
func NewTodosToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &TodosToolRenderContext{}, canceled)
}

// TodosToolRenderContext renders todos tool messages.
type TodosToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (t *TodosToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, "To-Do", opts.Compact)
	}

	var params tools.TodosParams
	var meta tools.TodosResponseMetadata
	var headerText string
	var body string

	// Parse params for pending state (before result is available).
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err == nil {
		completedCount := 0
		inProgressTask := ""
		for _, todo := range params.Todos {
			if todo.Status == "completed" {
				completedCount++
			}
			if todo.Status == "in_progress" {
				if todo.ActiveForm != "" {
					inProgressTask = todo.ActiveForm
				} else {
					inProgressTask = todo.Content
				}
			}
		}

		// Default display from params (used when pending or no metadata).
		ratio := fmt.Sprintf("%d/%d", completedCount, len(params.Todos))
		headerText = ratio
		if opts.Compact && inProgressTask != "" {
			headerText = fmt.Sprintf("%s · %s", ratio, inProgressTask)
		}

		// If we have metadata, use it for richer display.
		if opts.HasResult() && opts.Result.Metadata != "" {
			if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil {
				if meta.IsNew {
					if meta.JustStarted != "" {
						headerText = fmt.Sprintf("created %d todos, starting first", meta.Total)
					} else {
						headerText = fmt.Sprintf("created %d todos", meta.Total)
					}
					body = FormatTodosList(sty, meta.Todos, cappedWidth, opts.IsSpinning)
				} else {
					// Build header based on what changed.
					hasCompleted := len(meta.JustCompleted) > 0
					hasStarted := meta.JustStarted != ""
					allCompleted := meta.Completed == meta.Total

					ratio := fmt.Sprintf("%d/%d", meta.Completed, meta.Total)
					if opts.Compact && hasCompleted && hasStarted {
						text := fmt.Sprintf(" · completed %d, starting next", len(meta.JustCompleted))
						headerText = fmt.Sprintf("%s%s", ratio, text)
					} else if opts.Compact && hasCompleted {
						text := fmt.Sprintf(" · completed %d", len(meta.JustCompleted))
						if allCompleted {
							text = " · completed all"
						}
						headerText = fmt.Sprintf("%s%s", ratio, text)
					} else if opts.Compact && hasStarted {
						headerText = fmt.Sprintf("%s%s", ratio, " · starting task")
					} else {
						headerText = ratio
					}

					// Build body with details.
					if allCompleted {
						// Show all todos when all are completed, like when created.
						body = FormatTodosList(sty, meta.Todos, cappedWidth, opts.IsSpinning)
					} else if meta.JustStarted != "" {
						body = RenderTodoInProgressIcon(sty, opts.IsSpinning) + " " +
							sty.Tool.TodoJustStarted.Render(meta.JustStarted)
					}
				}
			}
		}
	}

	toolParams := []string{headerText}
	header := toolHeader(sty, opts.Status, "To-Do", cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	// Optimization: keep the chat stream clean by hiding the full list body
	// for intermediate updates. The persistent bottom pill area is the primary
	// surface for live task tracking. We only show the full list body in the
	// chat stream when the list is brand new. The completion state is visible
	// in the header ratio and the persistent bottom panel.
	if body != "" && !meta.IsNew {
		body = ""
	}

	if body == "" {
		return header
	}

	return joinToolParts(header, sty.Tool.Body.Render(body))
}

const (
	todoInProgressBlinkInterval = 450 * time.Millisecond
	TodoRecentlyCompletedTTL    = 30 * time.Second
)

// RenderTodoInProgressIcon alternates between the pending empty box and the
// completed solid box so running todos read as active without changing width.
// When agentActive is false (the agent has finished its turn) the icon is
// frozen as the plain pending box — no animation until the agent runs again.
func RenderTodoInProgressIcon(sty *styles.Styles, agentActive bool) string {
	if !agentActive {
		// Agent is idle; show a static pending icon so nothing blinks.
		return sty.Tool.TodoPendingIcon.Render(styles.TodoPendingIcon)
	}
	frame := time.Now().UnixMilli() / todoInProgressBlinkInterval.Milliseconds()
	if frame%2 == 0 {
		return sty.Tool.TodoPendingIcon.Render(styles.TodoPendingIcon)
	}
	return sty.Tool.TodoInProgressIcon.Render(styles.TodoInProgressIcon)
}

// FormatTodosList formats a list of todos for display.
// agentActive controls whether the in-progress icon blinks (true) or is
// frozen as a static pending box (false).
func FormatTodosList(sty *styles.Styles, todos []session.Todo, width int, agentActive bool) string {
	return formatTodosList(sty, todos, width, 0, agentActive)
}

// FormatTodosListWithLimit formats todos, clipping the body to maxLines and
// replacing overflow with a compact status summary.
// agentActive controls whether the in-progress icon blinks.
func FormatTodosListWithLimit(sty *styles.Styles, todos []session.Todo, width int, maxLines int, agentActive bool) string {
	if maxLines <= 0 {
		return ""
	}
	return formatTodosList(sty, todos, width, maxLines, agentActive)
}

func formatTodosList(sty *styles.Styles, todos []session.Todo, width int, maxLines int, agentActive bool) string {
	if len(todos) == 0 {
		return ""
	}
	if width <= 0 {
		return ""
	}

	displayTodos := make([]session.Todo, 0, len(todos))
	for _, todo := range todos {
		if strings.TrimSpace(todo.Content) != "" {
			displayTodos = append(displayTodos, todo)
		}
	}
	sortTodos(displayTodos)

	visibleLineBudget := maxLines
	if maxLines > 0 && todosRenderedLineCount(displayTodos) > maxLines && maxLines > 1 {
		visibleLineBudget = maxLines - 1
	}

	var lines []string
	var hidden []session.Todo
	for _, todo := range displayTodos {
		todoLines := formatTodoItemLines(sty, todo, width, agentActive)
		if visibleLineBudget > 0 && len(lines)+len(todoLines) > visibleLineBudget {
			hidden = append(hidden, todo)
			continue
		}
		lines = append(lines, todoLines...)
	}

	if maxLines > 0 && len(hidden) > 0 {
		summary := ansi.Truncate(formatHiddenTodosSummary(sty, hidden), width, "…")
		if len(lines) < maxLines {
			lines = append(lines, summary)
		} else if len(lines) > 0 {
			lines[len(lines)-1] = summary
		}
	}

	return strings.Join(lines, "\n")
}

func todosRenderedLineCount(todos []session.Todo) int {
	return len(todos)
}

func formatTodoItemLines(sty *styles.Styles, todo session.Todo, width int, agentActive bool) []string {
	if strings.TrimSpace(todo.Content) == "" {
		return nil
	}
	var prefix string
	textStyle := sty.Tool.TodoItem

	switch todo.Status {
	case session.TodoStatusCompleted:
		prefix = sty.Tool.TodoCompletedIcon.Render(styles.TodoCompletedIcon) + " "
		textStyle = textStyle.Faint(true)
	case session.TodoStatusInProgress:
		prefix = RenderTodoInProgressIcon(sty, agentActive) + " "
		textStyle = textStyle.Bold(true)
	case session.TodoStatusFailed:
		prefix = sty.Tool.TodoFailedIcon.Render(styles.TodoFailedIcon) + " "
		textStyle = textStyle.Foreground(sty.Tool.TodoFailedIcon.GetForeground())
	default:
		prefix = sty.Tool.TodoPendingIcon.Render(styles.TodoPendingIcon) + " "
	}

	subjectLine := prefix + textStyle.Render(todo.Content)
	subjectLine = ansi.Truncate(subjectLine, width, "…")
	return []string{subjectLine}
}

func formatHiddenTodosSummary(sty *styles.Styles, todos []session.Todo) string {
	hiddenByStatus := map[session.TodoStatus]int{}
	for _, todo := range todos {
		hiddenByStatus[todo.Status]++
	}

	var parts []string
	if count := hiddenByStatus[session.TodoStatusInProgress]; count > 0 {
		parts = append(parts, fmt.Sprintf("%d in progress", count))
	}
	if count := hiddenByStatus[session.TodoStatusPending]; count > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", count))
	}
	if count := hiddenByStatus[session.TodoStatusFailed]; count > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", count))
	}
	if count := hiddenByStatus[session.TodoStatusCompleted]; count > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", count))
	}

	return sty.Tool.TodoStatusNote.Render("… +" + strings.Join(parts, ", "))
}

// sortTodos sorts todos so active and open work stays visible first.
func sortTodos(todos []session.Todo) {
	now := time.Now().UnixMilli()
	slices.SortStableFunc(todos, func(a, b session.Todo) int {
		return statusOrder(a.Status, a.CompletedAt, now) - statusOrder(b.Status, b.CompletedAt, now)
	})
}

// statusOrder returns the sort order for a todo status.
func statusOrder(s session.TodoStatus, completedAt int64, now int64) int {
	if s == session.TodoStatusCompleted && completedAt > 0 {
		if now-completedAt < TodoRecentlyCompletedTTL.Milliseconds() {
			return -1 // Recently completed at the very top
		}
	}
	switch s {
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
