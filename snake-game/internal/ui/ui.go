package ui

import (
	"fmt"
	"snake-game/internal/game"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	tickInterval = time.Millisecond * 150
)

type tickMsg time.Time

type Model struct {
	game *game.Game
}

func NewModel() Model {
	return Model{
		game: game.NewGame(20, 15),
	}
}

func (m Model) Init() tea.Cmd {
	return tick()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "w":
			m.game.SetDirection(game.Up)
		case "down", "s":
			m.game.SetDirection(game.Down)
		case "left", "a":
			m.game.SetDirection(game.Left)
		case "right", "d":
			m.game.SetDirection(game.Right)
		case "r":
			if m.game.GameOver {
				m.game = game.NewGame(20, 15)
				return m, tick()
			}
		}

	case tickMsg:
		if !m.game.GameOver {
			m.game.Update()
			return m, tick()
		}
	}

	return m, nil
}

func (m Model) View() string {
	var s strings.Builder

	s.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render("--- SNAKE GAME ---") + "\n")
	s.WriteString(fmt.Sprintf("Score: %d\n", m.game.Score))

	board := make([][]string, m.game.Height)
	for y := range board {
		board[y] = make([]string, m.game.Width)
		for x := range board[y] {
			board[y][x] = "."
		}
	}

	// Draw food
	board[m.game.Food.Y][m.game.Food.X] = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("O")

	// Draw snake
	for i, p := range m.game.Snake {
		char := "o"
		if i == 0 {
			char = "@"
		}
		board[p.Y][p.X] = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(char)
	}

	// Render board
	borderStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	var boardStr strings.Builder
	for y := 0; y < m.game.Height; y++ {
		for x := 0; x < m.game.Width; x++ {
			boardStr.WriteString(board[y][x] + " ")
		}
		boardStr.WriteString("\n")
	}
	s.WriteString(borderStyle.Render(boardStr.String()))

	if m.game.GameOver {
		s.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("GAME OVER! Press 'r' to restart or 'q' to quit"))
	} else {
		s.WriteString("\nUse arrow keys or WASD to move. 'q' to quit.")
	}

	return s.String()
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
