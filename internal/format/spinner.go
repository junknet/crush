package format

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Settings represents the spinner configuration settings.
type Settings struct {
	Size        int
	Label       string
	LabelColor  color.Color
	GradColorA  color.Color
	GradColorB  color.Color
	CycleColors bool
}

// Spinner wraps the bubbles spinner for non-interactive mode.
type Spinner struct {
	done chan struct{}
	prog *tea.Program
}

type model struct {
	cancel  context.CancelFunc
	spinner spinner.Model
	label   string
	style   lipgloss.Style
}

func (m model) Init() tea.Cmd { return m.spinner.Tick }
func (m model) View() tea.View {
	return tea.NewView(fmt.Sprintf("%s %s", m.spinner.View(), m.style.Render(m.label)))
}

// Update implements tea.Model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// NewSpinner creates a new spinner with the given message.
func NewSpinner(ctx context.Context, cancel context.CancelFunc, settings Settings) *Spinner {
	s := spinner.New()
	s.Spinner = spinner.Dot

	spinnerFG := settings.LabelColor
	if settings.GradColorA != nil {
		spinnerFG = settings.GradColorA
	}
	s.Style = lipgloss.NewStyle().Foreground(spinnerFG)

	m := model{
		spinner: s,
		label:   settings.Label,
		style:   lipgloss.NewStyle().Foreground(settings.LabelColor),
		cancel:  cancel,
	}

	p := tea.NewProgram(m, tea.WithOutput(os.Stderr), tea.WithContext(ctx))

	return &Spinner{
		prog: p,
		done: make(chan struct{}, 1),
	}
}

// Start begins the spinner animation.
func (s *Spinner) Start() {
	go func() {
		defer close(s.done)
		_, err := s.prog.Run()
		// Ensures line is cleared.
		fmt.Fprint(os.Stderr, ansi.EraseEntireLine)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, tea.ErrInterrupted) {
			fmt.Fprintf(os.Stderr, "Error running spinner: %v\n", err)
		}
	}()
}

// Stop ends the spinner animation.
func (s *Spinner) Stop() {
	s.prog.Quit()
	<-s.done
}
