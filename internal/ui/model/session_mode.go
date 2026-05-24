package model

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/util"
)

func (m *UI) currentSessionMode() session.Mode {
	if m == nil {
		return session.ModeExecute
	}
	if m.session != nil {
		return m.session.Mode
	}
	return m.pendingSessionMode
}

func (m *UI) setCurrentSessionMode(mode session.Mode) {
	if m == nil {
		return
	}
	if m.session != nil {
		m.session.Mode = mode
		return
	}
	m.pendingSessionMode = mode
}

func (m *UI) saveCurrentSessionMode() tea.Cmd {
	if m == nil || m.session == nil {
		return nil
	}

	sessionCopy := *m.session
	return func() tea.Msg {
		if _, err := m.com.Workspace.SaveSession(context.Background(), sessionCopy); err != nil {
			return util.NewErrorMsg(err)
		}
		return nil
	}
}

func (m *UI) toggleSessionMode() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.state != uiChat && m.state != uiLanding {
		return nil
	}
	if m.isAgentBusy() {
		return util.ReportWarn("Agent is busy, please wait before switching modes...")
	}

	nextMode := session.ModePlan
	if m.currentSessionMode().IsPlan() {
		nextMode = session.ModeExecute
	}
	m.setCurrentSessionMode(nextMode)

	msg := "Plan mode disabled"
	if nextMode.IsPlan() {
		msg = "Plan mode enabled"
	}

	cmds := []tea.Cmd{
		util.CmdHandler(util.InfoMsg{
			Type: util.InfoTypeInfo,
			Msg:  msg,
			TTL:  3 * time.Second,
		}),
	}
	if saveCmd := m.saveCurrentSessionMode(); saveCmd != nil {
		cmds = append(cmds, saveCmd)
	}
	return tea.Batch(cmds...)
}
