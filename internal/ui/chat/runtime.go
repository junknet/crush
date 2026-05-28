package chat

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// RuntimeActivityKind classifies non-message runtime work surfaced in chat.
type RuntimeActivityKind string

const (
	RuntimeActivityConversationCompaction RuntimeActivityKind = "conversation_compaction"
	RuntimeActivityMemoryRecall           RuntimeActivityKind = "memory_recall"
	RuntimeActivityMemorySave             RuntimeActivityKind = "memory_save"
)

// RuntimeActivityStatus records whether a runtime activity is still active.
type RuntimeActivityStatus string

const (
	RuntimeActivityRunning RuntimeActivityStatus = "running"
	RuntimeActivityDone    RuntimeActivityStatus = "done"
	RuntimeActivityFailed  RuntimeActivityStatus = "failed"
)

// RuntimeActivitySnapshot is the render state for live runtime activity rows.
type RuntimeActivitySnapshot struct {
	ID              string
	Kind            RuntimeActivityKind
	Status          RuntimeActivityStatus
	Title           string
	Detail          string
	StartedAt       time.Time
	FinishedAt      time.Time
	Tokens          int64
	TokensAreExact  bool
	ProgressPercent int
	LineCount       int
}

// RuntimeActivityItem renders live runtime work, such as conversation
// compaction, as an updatable chat item.
type RuntimeActivityItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	sty      *styles.Styles
	snapshot RuntimeActivitySnapshot
}

var (
	_ MessageItem = (*RuntimeActivityItem)(nil)
	_ Animatable  = (*RuntimeActivityItem)(nil)
)

// NewRuntimeActivityItem creates a live runtime activity chat item.
func NewRuntimeActivityItem(sty *styles.Styles, snapshot RuntimeActivitySnapshot) *RuntimeActivityItem {
	v := list.NewVersioned()
	if snapshot.ProgressPercent < 0 {
		snapshot.ProgressPercent = -1
	}
	return &RuntimeActivityItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		sty:                      sty,
		snapshot:                 snapshot,
	}
}

// ID implements MessageItem.
func (r *RuntimeActivityItem) ID() string {
	return r.snapshot.ID
}

// Snapshot returns the current render state.
func (r *RuntimeActivityItem) Snapshot() RuntimeActivitySnapshot {
	return r.snapshot
}

// Update replaces the current render state.
func (r *RuntimeActivityItem) Update(snapshot RuntimeActivitySnapshot) tea.Cmd {
	wasRunning := r.isRunning()
	if snapshot.ProgressPercent < 0 {
		snapshot.ProgressPercent = -1
	}
	r.snapshot = snapshot
	r.Bump()
	r.clearCache()
	if !wasRunning && r.isRunning() {
		return r.StartAnimation()
	}
	return nil
}

// StartAnimation starts the activity spinner while the activity is running.
func (r *RuntimeActivityItem) StartAnimation() tea.Cmd {
	if !r.isRunning() {
		return nil
	}
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return StepMsg{ID: r.ID()}
	})
}

// Animate advances the runtime activity spinner.
func (r *RuntimeActivityItem) Animate(msg StepMsg) tea.Cmd {
	if !r.isRunning() {
		return nil
	}
	r.Bump()
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return StepMsg{ID: r.ID()}
	})
}

// Finished implements list.Item.
func (r *RuntimeActivityItem) Finished() bool {
	return !r.isRunning()
}

// RawRender implements MessageItem.
func (r *RuntimeActivityItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)
	content, height, ok := r.getCachedRender(cappedWidth)
	if ok && !r.isRunning() {
		return r.renderHighlighted(content, cappedWidth, height)
	}

	content = r.renderContent(cappedWidth)
	height = lipgloss.Height(content)
	if !r.isRunning() {
		r.setCachedRender(content, cappedWidth, height)
	}
	return r.renderHighlighted(content, cappedWidth, height)
}

// Render implements MessageItem.
func (r *RuntimeActivityItem) Render(width int) string {
	useCache := !r.isRunning() && !r.isHighlighted()
	var key uint64
	if r.focused {
		key = 1
	}
	if useCache {
		if cached, ok := r.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}

	prefix := r.sty.Messages.AssistantBlurred.Render()
	if r.focused {
		prefix = r.sty.Messages.AssistantFocused.Render()
	}
	lines := strings.Split(r.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	if useCache {
		r.setCachedPrefixedRender(out, width, key)
	}
	return out
}

func (r *RuntimeActivityItem) renderContent(width int) string {
	title := r.snapshot.Title
	if title == "" {
		title = string(r.snapshot.Kind)
	}

	header := r.renderHeader(title)
	if meta := r.renderMeta(); meta != "" {
		header += " (" + meta + ")"
	}
	header = ansi.Truncate(header, width, "…")

	lines := []string{header}
	if r.snapshot.ProgressPercent >= 0 {
		lines = append(lines, r.renderProgress(width))
	}
	if detail := strings.TrimSpace(r.snapshot.Detail); detail != "" && width > 2 {
		lines = append(lines, ansi.Truncate("  "+detail, width, "…"))
	}
	return strings.Join(lines, "\n")
}

func (r *RuntimeActivityItem) renderHeader(title string) string {
	if r.isRunning() {
		return renderBrailleSpinner(r.sty, title)
	}
	icon := "✓"
	style := lipgloss.NewStyle().Foreground(r.sty.WorkingLabelColor)
	if r.snapshot.Status == RuntimeActivityFailed {
		icon = "!"
		style = r.sty.Messages.ErrorTitle
	}
	return style.Render(icon + " " + title)
}

func (r *RuntimeActivityItem) renderMeta() string {
	parts := make([]string, 0, 3)
	if elapsed := r.elapsed(); elapsed > 0 {
		parts = append(parts, formatRuntimeDuration(elapsed))
	}
	if r.snapshot.Tokens > 0 {
		tokenText := formatRuntimeTokenCount(r.snapshot.Tokens) + " tokens"
		if !r.snapshot.TokensAreExact {
			tokenText = "~" + tokenText
		}
		parts = append(parts, tokenText)
	}
	if r.snapshot.LineCount > 0 {
		parts = append(parts, fmt.Sprintf("%d lines", r.snapshot.LineCount))
	}
	return strings.Join(parts, " · ")
}

func (r *RuntimeActivityItem) renderProgress(width int) string {
	progress := max(0, min(100, r.snapshot.ProgressPercent))
	barWidth := max(6, min(24, width-8))
	filled := (barWidth * progress) / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	return fmt.Sprintf("  %s %d%%", lipgloss.NewStyle().Foreground(r.sty.WorkingLabelColor).Render(bar), progress)
}

func (r *RuntimeActivityItem) elapsed() time.Duration {
	startedAt := r.snapshot.StartedAt
	if startedAt.IsZero() {
		return 0
	}
	finishedAt := r.snapshot.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	if finishedAt.Before(startedAt) {
		return 0
	}
	return finishedAt.Sub(startedAt)
}

func (r *RuntimeActivityItem) isRunning() bool {
	return r.snapshot.Status == RuntimeActivityRunning
}

func formatRuntimeDuration(duration time.Duration) string {
	totalSeconds := int(duration.Round(time.Second).Seconds())
	if totalSeconds <= 0 {
		return "0s"
	}
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	if minutes <= 0 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm %ds", minutes, seconds)
}

func formatRuntimeTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return trimRuntimeTokenUnit(fmt.Sprintf("%.1fM", float64(tokens)/1_000_000))
	case tokens >= 1_000:
		return trimRuntimeTokenUnit(fmt.Sprintf("%.1fK", float64(tokens)/1_000))
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func trimRuntimeTokenUnit(value string) string {
	value = strings.Replace(value, ".0K", "K", 1)
	value = strings.Replace(value, ".0M", "M", 1)
	return value
}
