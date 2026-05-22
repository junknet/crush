package chat

import (
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// StepMsg is a message type used to trigger the next step in the spinner animation.
type StepMsg struct {
	ID string
}

// brailleSpinnerFrames is the single-cell rotating spinner used for
// every "thinking / working / running" state in the chat surface.
// It replaces the upstream 15-char cycling random-glyph animation that
// leaked random characters (e.g. `^_!€E4FC0£CF*F@`) into the middle of
// the message stream.
var brailleSpinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// renderBrailleSpinner returns a one-cell braille spinner followed by
// the given label, e.g. "⠹ Thinking…". The frame is selected from the
// wall clock so the spinner advances on every redraw without depending
// on the anim package's internal step counter.
//
// Callers that previously did `anim.Render()` should swap to this so
// the chat surface stays free of random glyphs.
func renderBrailleSpinner(sty *styles.Styles, label string) string {
	idx := int(time.Now().UnixMilli()/80) % len(brailleSpinnerFrames)
	if label == "" {
		return lipgloss.NewStyle().
			Foreground(sty.WorkingLabelColor).
			Render(string(brailleSpinnerFrames[idx]))
	}
	return lipgloss.NewStyle().
		Foreground(sty.WorkingLabelColor).
		Render(string(brailleSpinnerFrames[idx]) + " " + label + "…")
}
