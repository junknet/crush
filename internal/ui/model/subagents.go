package model

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/x/ansi"
)

// subAgentHistoryMax caps how many sub-agent entries the sidebar will
// remember. Older entries fall off as new ones arrive.
const subAgentHistoryMax = 8

// subAgentStatus mirrors the lifecycle of a single sub-agent invocation.
type subAgentStatus int

const (
	subAgentRunning subAgentStatus = iota
	subAgentDone
	subAgentFailed
)

// subAgentEntry is one row in the sidebar's sub-agent section. Keyed by
// the parent's tool-call id so Started → Finished/Failed can update the
// same row in place.
type subAgentEntry struct {
	SessionID  string
	ToolCallID string
	Prompt     string
	Goal       string
	Profile    string
	LastStatus string
	LastOutput string
	TraceID    string
	Status     subAgentStatus
	Error      string
	StartedAt  time.Time
	UpdatedAt  time.Time
}

// recordSubAgentEvent merges a notify.Notification into the sub-agent
// history. New Started events insert at the front; matching Finished/
// Failed events update in place. Returns the updated slice.
func recordSubAgentEvent(prev []subAgentEntry, n notify.Notification) []subAgentEntry {
	if n.SubAgentToolCallID == "" {
		return prev
	}
	now := time.Now()
	if idx := findSubAgentEntry(prev, n.SessionID, n.SubAgentToolCallID); idx >= 0 {
		prev[idx] = mergeSubAgentNotification(prev[idx], n, now)
		return moveSubAgentEntryToFront(prev, idx)
	}
	// No existing row. Only Started should create one; ignore stray
	// Finished/Failed without a prior Started.
	if n.Type != notify.TypeSubAgentStarted {
		return prev
	}
	entry := subAgentEntry{
		SessionID:  n.SessionID,
		ToolCallID: n.SubAgentToolCallID,
		Prompt:     n.SubAgentPrompt,
		Profile:    n.SubAgentProfile,
		LastStatus: "running",
		Status:     subAgentRunning,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	// Newest first, then trim.
	prev = append([]subAgentEntry{entry}, prev...)
	if len(prev) > subAgentHistoryMax {
		prev = prev[:subAgentHistoryMax]
	}
	return prev
}

// recordSubAgentTaskEvent merges scheduler task telemetry into an existing
// sub-agent row. Scheduler events are keyed by the child session id while
// notifications are keyed by the parent tool-call id, so this intentionally
// updates only rows already created by a sub-agent notification.
func recordSubAgentTaskEvent(prev []subAgentEntry, ev scheduler.Event) []subAgentEntry {
	if ev.SessionID == "" {
		return prev
	}
	idx := findSubAgentEntry(prev, ev.SessionID, "")
	if idx < 0 {
		return prev
	}

	entry := prev[idx]
	now := time.Now()
	if ev.Profile != "" {
		entry.Profile = string(ev.Profile)
	}
	if ev.Goal != "" {
		entry.Goal = ev.Goal
	}
	if ev.Status != "" {
		entry.LastStatus = ev.Status
	}
	if ev.Output != "" {
		entry.LastOutput = ev.Output
	}
	if ev.TraceID != "" {
		entry.TraceID = ev.TraceID
	}

	switch ev.Kind {
	case scheduler.EventTaskPlanned, scheduler.EventTaskStarted, scheduler.EventTaskProgress:
		if entry.Status == subAgentRunning {
			entry.Status = subAgentRunning
		}
	case scheduler.EventTaskFinished:
		entry.Status = subAgentDone
	case scheduler.EventTaskFailed:
		entry.Status = subAgentFailed
		if ev.Error != "" {
			entry.Error = ev.Error
		}
	}
	entry.UpdatedAt = now
	prev[idx] = entry
	return moveSubAgentEntryToFront(prev, idx)
}

func mergeSubAgentNotification(entry subAgentEntry, n notify.Notification, now time.Time) subAgentEntry {
	if n.SessionID != "" {
		entry.SessionID = n.SessionID
	}
	if n.SubAgentToolCallID != "" {
		entry.ToolCallID = n.SubAgentToolCallID
	}
	if n.SubAgentProfile != "" {
		entry.Profile = n.SubAgentProfile
	}

	switch n.Type {
	case notify.TypeSubAgentFinished:
		entry.Status = subAgentDone
		entry.LastStatus = "done"
		entry.UpdatedAt = now
	case notify.TypeSubAgentFailed:
		entry.Status = subAgentFailed
		entry.Error = n.SubAgentError
		entry.LastStatus = "failed"
		entry.UpdatedAt = now
	case notify.TypeSubAgentStarted:
		// Duplicate Started — refresh prompt & timestamp.
		entry.Prompt = n.SubAgentPrompt
		entry.LastStatus = "running"
		entry.Status = subAgentRunning
		entry.StartedAt = now
		entry.UpdatedAt = now
	}
	return entry
}

func findSubAgentEntry(entries []subAgentEntry, sessionID, toolCallID string) int {
	for i, entry := range entries {
		if sessionID != "" && entry.SessionID == sessionID {
			return i
		}
		if toolCallID != "" && entry.ToolCallID == toolCallID {
			return i
		}
	}
	return -1
}

func moveSubAgentEntryToFront(entries []subAgentEntry, idx int) []subAgentEntry {
	if idx <= 0 || idx >= len(entries) {
		return entries
	}
	entry := entries[idx]
	copy(entries[1:idx+1], entries[:idx])
	entries[0] = entry
	return entries
}

// subAgentInfo renders the sub-agent activity section for the sidebar.
// Returns "" when there's nothing to show so the sidebar layout can
// skip the section entirely.
func (m *UI) subAgentInfo(width, maxRows int) string {
	if len(m.subAgents) == 0 {
		return ""
	}
	t := m.com.Styles

	var heading string
	running := 0
	for _, e := range m.subAgents {
		if e.Status == subAgentRunning {
			running++
		}
	}
	if running > 0 {
		heading = fmt.Sprintf("Sub-agents (%d active)", running)
	} else {
		heading = "Sub-agents"
	}
	heading = t.Resource.Heading.Width(width).Render(heading)

	if maxRows <= 0 {
		maxRows = subAgentHistoryMax
	}
	rows := m.subAgents
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	lines := []string{heading}
	for _, e := range rows {
		entryLines := renderSubAgentEntry(e, width)
		switch e.Status {
		case subAgentRunning:
			for i := range entryLines {
				entryLines[i] = t.Resource.Name.Render(entryLines[i])
			}
		case subAgentFailed:
			style := lipgloss.NewStyle().Foreground(t.Resource.Name.GetForeground())
			for i := range entryLines {
				entryLines[i] = style.Render(entryLines[i])
			}
		default:
			for i := range entryLines {
				entryLines[i] = t.Resource.Name.Render(entryLines[i])
			}
		}
		lines = append(lines, entryLines...)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(
		lipgloss.JoinVertical(lipgloss.Left, lines...),
	)
}

func subAgentIcon(s subAgentStatus) string {
	switch s {
	case subAgentRunning:
		return "•"
	case subAgentDone:
		return "✓"
	case subAgentFailed:
		return "✗"
	}
	return "?"
}

func renderSubAgentEntry(e subAgentEntry, width int) []string {
	if width <= 0 {
		return nil
	}
	icon := subAgentIcon(e.Status)
	role := subAgentRole(e.Profile)
	status := subAgentStatusText(e)
	duration := subAgentDurationText(e)
	header := strings.TrimSpace(fmt.Sprintf("%s %s %s %s", icon, role, status, duration))
	header = ansi.Truncate(header, width, "…")

	input := subAgentInputSummary(e)
	output := subAgentOutputSummary(e)
	if input == "" && output == "" {
		return []string{header}
	}
	return []string{
		header,
		ansi.Truncate(subAgentIOLine(input, output, width), width, "…"),
	}
}

func subAgentRole(profile string) string {
	switch scheduler.WorkerProfile(profile) {
	case scheduler.ProfileBrainAgent:
		return "brain"
	case scheduler.ProfileExploreAgent:
		return "explore"
	case scheduler.ProfileWorkerAgent:
		return "worker"
	default:
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return "agent"
		}
		return strings.TrimSuffix(profile, "_agent")
	}
}

func subAgentStatusText(e subAgentEntry) string {
	switch e.Status {
	case subAgentRunning:
		return "running"
	case subAgentDone:
		return "done"
	case subAgentFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func subAgentDurationText(e subAgentEntry) string {
	if e.StartedAt.IsZero() {
		return ""
	}
	finishedAt := e.UpdatedAt
	if e.Status == subAgentRunning || finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	elapsed := finishedAt.Sub(e.StartedAt)
	if elapsed < 0 {
		return ""
	}
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	}
}

func subAgentInputSummary(e subAgentEntry) string {
	return compactSubAgentText(firstNonEmpty(e.Goal, e.Prompt))
}

func subAgentOutputSummary(e subAgentEntry) string {
	output := firstNonEmpty(e.Error, e.LastOutput)
	if output != "" {
		return compactSubAgentText(output)
	}
	status := strings.TrimSpace(e.LastStatus)
	switch status {
	case "", "planned", "running", "done", "failed":
		return ""
	default:
		return compactSubAgentText(status)
	}
}

func subAgentIOLine(input, output string, width int) string {
	switch {
	case input == "" && output == "":
		return ""
	case input == "":
		return "out " + output
	case output == "":
		return "in " + input
	}

	const separator = " | out "
	inputPrefix := "in "
	budget := width - ansi.StringWidth(inputPrefix) - ansi.StringWidth(separator)
	if budget <= 0 {
		return "out " + output
	}
	inputWidth := max(4, budget/2)
	outputWidth := max(4, budget-inputWidth)
	return inputPrefix + ansi.Truncate(input, inputWidth, "…") + separator + ansi.Truncate(output, outputWidth, "…")
}

func compactSubAgentText(text string) string {
	text = ansi.Strip(text)
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.Join(strings.Fields(text), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// truncatePrompt shortens a multi-line prompt to a single ellipsised
// line of the given width. width <= 4 returns "…".
func truncatePrompt(p string, width int) string {
	one := strings.ReplaceAll(strings.ReplaceAll(p, "\n", " "), "\r", "")
	one = strings.TrimSpace(one)
	if width <= 0 {
		return ""
	}
	if width <= 4 || len(one) <= width {
		return one
	}
	return one[:width-1] + "…"
}
