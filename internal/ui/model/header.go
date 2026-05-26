package model

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const (
	headerDiag           = "╱"
	minHeaderDiags       = 3
	leftPadding          = 1
	rightPadding         = 1
	diagToDetailsSpacing = 1 // space between diagonal pattern and details section
)

type header struct {
	// cached logo and compact logo
	logo        string
	compactLogo string

	com                *common.Common
	width              int
	compact            bool
	sessionID          string
	sessionOpenedAt    time.Time
	activeRunStartedAt time.Time
}

// newHeader creates a new header model.
func newHeader(com *common.Common) *header {
	h := &header{
		com: com,
	}
	h.refresh()
	return h
}

// refresh rebuilds cached logo strings using the current styles. Call
// after the theme changes.
func (h *header) refresh() {
	t := h.com.Styles
	isHyper := h.com.IsHyper()
	charm := "Charm™"
	if !isHyper {
		charm = " " + charm
	}
	name := "CRUSH"
	if isHyper {
		name = "HYPERCRUSH"
	}
	h.compactLogo = t.Header.Charm.Render(charm) + " " +
		styles.ApplyBoldForegroundGrad(t.Header.LogoGradCanvas, name, t.Header.LogoGradFromColor, t.Header.LogoGradToColor) + " "
	// Force drawHeader to re-render the wide logo on the next frame.
	h.width = 0
	h.logo = ""
}

// drawHeader draws the header for the given session.
func (h *header) drawHeader(
	scr uv.Screen,
	area uv.Rectangle,
	session *session.Session,
	mode session.Mode,
	compact bool,
	detailsOpen bool,
	width int,
	hyperCredits *int,
	isBusy bool,
	busyStatus string,
	animFrame int,
) {
	t := h.com.Styles
	if width != h.width || compact != h.compact {
		h.logo = renderLogo(h.com.Styles, compact, h.com.IsHyper(), width)
	}

	h.width = width
	h.compact = compact

	if !compact || session == nil {
		uv.NewStyledString(h.logo).Draw(scr, area)
		return
	}

	if session.ID == "" {
		return
	}

	if session.ID != h.sessionID {
		h.sessionID = session.ID
		h.sessionOpenedAt = time.Now()
	}

	var elapsedSec int64 = -1
	if isBusy {
		if h.activeRunStartedAt.IsZero() {
			h.activeRunStartedAt = time.Now()
		}
		elapsedSec = int64(time.Since(h.activeRunStartedAt).Seconds())
	} else {
		h.activeRunStartedAt = time.Time{}
	}

	var b strings.Builder
	b.WriteString(h.compactLogo)

	availDetailWidth := width - leftPadding - rightPadding - lipgloss.Width(b.String()) - minHeaderDiags - diagToDetailsSpacing
	lspErrorCount := 0
	for _, info := range h.com.Workspace.LSPGetStates() {
		lspErrorCount += info.DiagnosticCount
	}
	details := renderHeaderDetails(
		h.com,
		session,
		mode,
		lspErrorCount,
		detailsOpen,
		availDetailWidth,
		hyperCredits,
		elapsedSec,
		busyStatus,
		animFrame,
	)

	remainingWidth := width -
		lipgloss.Width(b.String()) -
		lipgloss.Width(details) -
		leftPadding -
		rightPadding -
		diagToDetailsSpacing

	if remainingWidth > 0 {
		diagsText := strings.Repeat(headerDiag, max(minHeaderDiags, remainingWidth))
		if isBusy {
			b.WriteString(styles.ApplyScrollingForegroundGrad(
				t.Header.Diagonals,
				diagsText,
				t.WorkingGradFromColor,
				t.WorkingGradToColor,
				animFrame,
			))
		} else {
			b.WriteString(t.Header.Diagonals.Render(diagsText))
		}
		b.WriteString(" ")
	}

	b.WriteString(details)

	view := uv.NewStyledString(
		t.Header.Wrapper.Padding(0, rightPadding, 0, leftPadding).Render(b.String()),
	)
	view.Draw(scr, area)
}

// renderHeaderDetails renders the details section of the header.
func renderHeaderDetails(
	com *common.Common,
	session *session.Session,
	mode session.Mode,
	lspErrorCount int,
	detailsOpen bool,
	availWidth int,
	hyperCredits *int,
	elapsedSec int64,
	busyStatus string,
	animFrame int,
) string {
	t := com.Styles

	var parts []string

	// Display the concrete brain model, not only the model slot name.
	styledModel := styles.ApplyBoldForegroundGrad(
		t.Header.LogoGradCanvas,
		brainHeaderModelLabel(com.Config()),
		t.Header.LogoGradFromColor,
		t.Header.LogoGradToColor,
	)
	parts = append(parts, styledModel)

	// Display the animated color ball & session timer
	if elapsedSec >= 0 {
		colors := []string{
			"#50FA7B", // Green
			"#8BE9FD", // Cyan
			"#FF79C6", // Pink
			"#BD93F9", // Purple
		}
		selectedColor := colors[elapsedSec%int64(len(colors))]
		ballStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(selectedColor))

		// Blink the ball based on animFrame (alternates every 500ms, as animFrame advances every 100ms)
		ballChar := "●"
		if (animFrame/5)%2 == 0 {
			ballChar = " "
		}
		ball := ballStyle.Render(ballChar)

		durationText := ""
		mins := elapsedSec / 60
		secs := elapsedSec % 60
		if mins > 0 {
			durationText = fmt.Sprintf("%dm %ds", mins, secs)
		} else {
			durationText = fmt.Sprintf("%ds", secs)
		}
		timerContent := durationText
		if busyStatus != "" {
			timerContent = fmt.Sprintf("%s %s", busyStatus, durationText)
		}
		timerStr := fmt.Sprintf("%s %s", ball, t.Header.Percentage.Render(timerContent))
		parts = append(parts, timerStr)
	}

	if lspErrorCount > 0 {
		parts = append(parts, t.LSP.ErrorDiagnostic.Render(fmt.Sprintf("%s%d", styles.LSPErrorIcon, lspErrorCount)))
	}

	if contextWindow := contextWindowForBrain(com); contextWindow > 0 {
		percentage := (float64(session.CompletionTokens+session.PromptTokens) / float64(contextWindow)) * 100
		formattedPercentage := t.Header.Percentage.Render(fmt.Sprintf("%d%%", int(percentage)))
		parts = append(parts, formattedPercentage)
	}

	if com.IsHyper() && hyperCredits != nil {
		hc := t.Header.Hypercredit.Render(styles.HypercreditIcon) + " " + t.Header.Percentage.Render(common.FormatCredits(*hyperCredits))
		parts = append(parts, hc)
	}

	if mode.IsPlan() {
		parts = append(parts, t.Header.Keystroke.Render("plan mode"))
	}

	const keystroke = "ctrl+d"
	if detailsOpen {
		parts = append(parts, t.Header.Keystroke.Render(keystroke)+t.Header.KeystrokeTip.Render(" close"))
	} else {
		parts = append(parts, t.Header.Keystroke.Render(keystroke)+t.Header.KeystrokeTip.Render(" open "))
	}

	dot := t.Header.Separator.Render(" • ")
	metadata := strings.Join(parts, dot)
	metadata = dot + metadata

	const dirTrimLimit = 4
	workingDir := com.Workspace.WorkingDir()
	cwd := fsext.DirTrim(fsext.PrettyPath(workingDir), dirTrimLimit)
	cwd = common.PathLink(t.Header.WorkingDir, cwd, workingDir)

	result := cwd + metadata
	return ansi.Truncate(result, max(0, availWidth), "…")
}

func brainHeaderModelLabel(cfg *config.Config) string {
	const role = "BRAIN"
	if cfg == nil {
		return role
	}
	agentCfg, ok := cfg.Agents[config.AgentBrain]
	if !ok {
		return role
	}
	selected, ok := cfg.SelectedModelForType(agentCfg.Model)
	if !ok {
		return strings.ToUpper(string(agentCfg.Model))
	}
	switch {
	case selected.Provider != "" && selected.Model != "":
		return fmt.Sprintf("%s %s/%s", role, selected.Provider, selected.Model)
	case selected.Model != "":
		return fmt.Sprintf("%s %s", role, selected.Model)
	default:
		return role
	}
}
