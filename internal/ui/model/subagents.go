package model

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/ui/common"
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
	ToolCallID string
	Prompt     string
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
	// Match by tool-call id.
	for i := range prev {
		if prev[i].ToolCallID != n.SubAgentToolCallID {
			continue
		}
		switch n.Type {
		case notify.TypeSubAgentFinished:
			prev[i].Status = subAgentDone
			prev[i].UpdatedAt = now
		case notify.TypeSubAgentFailed:
			prev[i].Status = subAgentFailed
			prev[i].Error = n.SubAgentError
			prev[i].UpdatedAt = now
		case notify.TypeSubAgentStarted:
			// Duplicate Started — refresh prompt & timestamp.
			prev[i].Prompt = n.SubAgentPrompt
			prev[i].StartedAt = now
			prev[i].UpdatedAt = now
		}
		return prev
	}
	// No existing row. Only Started should create one; ignore stray
	// Finished/Failed without a prior Started.
	if n.Type != notify.TypeSubAgentStarted {
		return prev
	}
	entry := subAgentEntry{
		ToolCallID: n.SubAgentToolCallID,
		Prompt:     n.SubAgentPrompt,
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
		icon := subAgentIcon(e.Status)
		prompt := truncatePrompt(e.Prompt, max(0, width-len(icon)-1))
		line := fmt.Sprintf("%s %s", icon, prompt)
		switch e.Status {
		case subAgentRunning:
			line = t.Resource.Name.Render(line)
		case subAgentFailed:
			line = lipgloss.NewStyle().Foreground(t.Resource.Name.GetForeground()).Render(line)
		default:
			line = t.Resource.Name.Render(line)
		}
		lines = append(lines, line)
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

// ensure common import is used even if styles are referenced
// indirectly; reserved for future ModelContextInfo overlays.
var _ = common.PrettyPath
