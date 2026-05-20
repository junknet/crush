package model

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestCurrentModelSupportsImages(t *testing.T) {
	t.Parallel()

	t.Run("returns false when config is nil", func(t *testing.T) {
		t.Parallel()

		ui := newTestUIWithConfig(t, nil)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns false when coder agent is missing", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{
			Providers: csync.NewMap[string, config.ProviderConfig](),
			Agents:    map[string]config.Agent{},
		}
		ui := newTestUIWithConfig(t, cfg)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns false when model is not found", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{
			Providers: csync.NewMap[string, config.ProviderConfig](),
			Agents: map[string]config.Agent{
				config.AgentBuild: {Model: config.SelectedModelTypeBuild},
			},
		}
		ui := newTestUIWithConfig(t, cfg)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns true when current model supports images", func(t *testing.T) {
		t.Parallel()

		providers := csync.NewMap[string, config.ProviderConfig]()
		providers.Set("test-provider", config.ProviderConfig{
			ID: "test-provider",
			Models: []catwalk.Model{
				{ID: "test-model", SupportsImages: true},
			},
		})

		cfg := &config.Config{
			Models: map[config.SelectedModelType]config.SelectedModel{
				config.SelectedModelTypeBuild: {
					Provider: "test-provider",
					Model:    "test-model",
				},
			},
			Providers: providers,
			Agents: map[string]config.Agent{
				config.AgentBuild: {Model: config.SelectedModelTypeBuild},
			},
		}

		ui := newTestUIWithConfig(t, cfg)
		require.True(t, ui.currentModelSupportsImages())
	})
}

func newTestUIWithConfig(t *testing.T, cfg *config.Config) *UI {
	t.Helper()

	ws := &testWorkspace{cfg: cfg}
	com := common.DefaultCommon(ws)
	com.Workspace = ws

	return &UI{
		com:         com,
		dialog:      dialog.NewOverlay(),
		keyMap:      DefaultKeyMap(),
		textarea:    textarea.New(),
		attachments: attachments.New(nil, attachments.Keymap{}),
		status:      NewStatus(com, nil),
	}
}

func TestCtrlCClearsComposerAndArmsQuit(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.textarea.SetValue("draft text")
	ui.attachments.Update(message.Attachment{FileName: "note.txt"})

	cmd := ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	require.NotNil(t, cmd)
	require.Empty(t, ui.textarea.Value())
	require.Empty(t, ui.attachments.List())
	require.True(t, ui.ctrlCArmed)
}

func TestCtrlCSecondPressQuits(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.ctrlCArmed = true
	ui.ctrlCArmedAt = time.Now()

	cmd := ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	require.NotNil(t, cmd)
	require.False(t, ui.ctrlCArmed)
	require.IsType(t, tea.QuitMsg{}, cmd())
}

func TestCtrlCArmSurvivesOtherKeysUntilTimeout(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.ctrlCArmed = false

	cmd := ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	require.NotNil(t, cmd)
	require.True(t, ui.ctrlCArmed)

	_ = ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: 'x'}))
	require.True(t, ui.ctrlCArmed)

	cmd = ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	require.NotNil(t, cmd)
	require.IsType(t, tea.QuitMsg{}, cmd())
	require.False(t, ui.ctrlCArmed)
}

func TestCtrlCArmExpires(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.ctrlCArmed = true

	_, _ = ui.Update(ctrlCTimerExpiredMsg{})
	require.False(t, ui.ctrlCArmed)
}

func TestSchedulerEventUpdatesStatus(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	_, _ = ui.Update(pubsub.Event[scheduler.Event]{
		Type: pubsub.UpdatedEvent,
		Payload: scheduler.Event{
			Kind: scheduler.EventTaskStarted,
			Goal: "review plan",
		},
	})

	require.NotNil(t, ui.status)
	require.Contains(t, ui.status.msg.Msg, "Task started")
}

func TestSchedulerEventIgnoresOtherConversationSession(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.session = &session.Session{ID: "session-a"}

	cmd := ui.handleSchedulerEvent(scheduler.Event{
		ConversationSessionID: "session-b",
		Kind:                  scheduler.EventTaskStarted,
		Goal:                  "review plan",
	})

	require.Nil(t, cmd)
}

// testWorkspace is a minimal [workspace.Workspace] stub for unit tests.
type testWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *testWorkspace) Config() *config.Config {
	return w.cfg
}
