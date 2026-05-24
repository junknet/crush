package model

import (
	"context"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func TestCurrentModelSupportsImages(t *testing.T) {
	t.Parallel()

	t.Run("returns false when config is nil", func(t *testing.T) {
		t.Parallel()

		ui := newTestUIWithConfig(t, nil)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns false when worker agent is missing", func(t *testing.T) {
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
				config.AgentBrain: {Model: config.SelectedModelTypeBrain},
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
				config.SelectedModelTypeBrain: {
					Provider: "test-provider",
					Model:    "test-model",
				},
			},
			Providers: providers,
			Agents: map[string]config.Agent{
				config.AgentBrain: {Model: config.SelectedModelTypeBrain},
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
		com:                com,
		dialog:             dialog.NewOverlay(),
		keyMap:             DefaultKeyMap(),
		chat:               NewChat(com),
		textarea:           textarea.New(),
		attachments:        attachments.New(nil, attachments.Keymap{}),
		status:             NewStatus(com, nil),
		pendingSessionMode: session.ModeExecute,
	}
}

type dragScrollTestItem struct {
	*list.Versioned
	label string
}

func newDragScrollTestItem(label string) *dragScrollTestItem {
	return &dragScrollTestItem{
		Versioned: list.NewVersioned(),
		label:     label,
	}
}

func (i *dragScrollTestItem) Render(width int) string {
	return i.label
}

func (i *dragScrollTestItem) Finished() bool {
	return true
}

func (i *dragScrollTestItem) SetFocused(bool) {}

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

func TestShiftTabTogglesPlanMode(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.state = uiChat
	ui.focus = uiFocusEditor
	ui.session = &session.Session{ID: "session-a"}

	cmd := ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	require.NotNil(t, cmd)
	require.Equal(t, session.ModePlan, ui.currentSessionMode())
	require.Equal(t, session.ModePlan, ui.session.Mode)

	cmd = ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	require.NotNil(t, cmd)
	require.Equal(t, session.ModeExecute, ui.currentSessionMode())
	require.Equal(t, session.ModeExecute, ui.session.Mode)
}

func TestToggleSessionModePersistsCurrentSession(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ui.state = uiChat
	ui.session = &session.Session{ID: "session-a", Mode: session.ModeExecute}

	cmd := ui.toggleSessionMode()
	require.NotNil(t, cmd)
	runTestCmd(cmd)

	require.Equal(t, session.ModePlan, ui.currentSessionMode())
	require.Equal(t, session.ModePlan, ui.session.Mode)
	require.Equal(t, session.ModePlan, ws.lastSavedSession.Mode)
}

func TestSendMessagePassesPlanMode(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ui.session = &session.Session{ID: "session-a"}
	ui.setCurrentSessionMode(session.ModePlan)

	cmd := ui.sendMessage("draft a plan")
	require.NotNil(t, cmd)
	runTestCmd(cmd)

	require.True(t, ws.lastAgentRunPlanMode)
	require.Equal(t, "draft a plan", ws.lastAgentRunPrompt)
	require.Equal(t, "session-a", ws.lastAgentRunSessionID)
}

func TestSendMessageCreatesSessionWithCurrentMode(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ui.setCurrentSessionMode(session.ModePlan)

	cmd := ui.sendMessage("draft a plan")
	require.NotNil(t, cmd)
	runTestCmd(cmd)

	require.Equal(t, session.ModePlan, ws.lastCreateSessionMode)
	require.True(t, ws.lastAgentRunPlanMode)
	require.Equal(t, "draft a plan", ws.lastAgentRunPrompt)
	require.Equal(t, "session-a", ws.lastAgentRunSessionID)
	require.NotNil(t, ui.session)
	require.Equal(t, session.ModePlan, ui.session.Mode)
}

func TestMouseDragAtViewportEdgeAutoScrolls(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	items := make([]list.Item, 0, 6)
	for i := range 6 {
		items = append(items, newDragScrollTestItem(string(rune('a'+i))))
	}
	ui.chat.list.SetGap(0)
	ui.chat.list.SetItems(items...)
	ui.chat.SetSize(20, 3)
	ui.chat.ScrollToBottom()
	ui.state = uiChat
	ui.focus = uiFocusMain
	ui.layout.main = uv.Rect(0, 5, 20, 3)

	startIdx, startLine := ui.chat.list.ScrollOffset()
	handled, _ := ui.chat.HandleMouseDown(2, 1)
	require.True(t, handled)

	_, cmd := ui.Update(tea.MouseMotionMsg{X: 2, Y: 5})
	require.NotNil(t, cmd)

	afterMotionIdx, afterMotionLine := ui.chat.list.ScrollOffset()
	require.True(t, afterMotionIdx < startIdx || afterMotionLine < startLine)
	require.True(t, ui.mouseAutoScrollPending)
	require.Equal(t, -1, ui.mouseAutoScrollDirection)

	_, _ = ui.Update(mouseAutoScrollMsg{Token: ui.mouseAutoScrollToken, Direction: -1})
	afterTickIdx, afterTickLine := ui.chat.list.ScrollOffset()
	require.True(t, afterTickIdx < afterMotionIdx || afterTickLine < afterMotionLine)
}

func TestEscapeStopsMouseAutoScroll(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	items := make([]list.Item, 0, 6)
	for i := range 6 {
		items = append(items, newDragScrollTestItem(string(rune('a'+i))))
	}
	ui.chat.list.SetGap(0)
	ui.chat.list.SetItems(items...)
	ui.chat.SetSize(20, 3)
	ui.chat.ScrollToBottom()
	ui.state = uiChat
	ui.focus = uiFocusMain
	ui.layout.main = uv.Rect(0, 5, 20, 3)

	handled, _ := ui.chat.HandleMouseDown(2, 1)
	require.True(t, handled)
	_, cmd := ui.Update(tea.MouseMotionMsg{X: 2, Y: 5})
	require.NotNil(t, cmd)
	require.True(t, ui.mouseAutoScrollPending)
	require.True(t, ui.chat.mouseDown)

	ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	require.False(t, ui.mouseAutoScrollPending)
	require.False(t, ui.chat.mouseDown)
	require.Equal(t, uiFocusEditor, ui.focus)
	require.True(t, ui.textarea.Focused())
}

// testWorkspace is a minimal [workspace.Workspace] stub for unit tests.
type testWorkspace struct {
	workspace.Workspace
	cfg                   *config.Config
	agentReady            bool
	lastCreateSessionMode session.Mode
	lastCreatedSession    session.Session
	lastSavedSession      session.Session
	lastAgentRunPlanMode  bool
	lastAgentRunPrompt    string
	lastAgentRunSessionID string
}

func (w *testWorkspace) Config() *config.Config {
	return w.cfg
}

func (w *testWorkspace) CreateSession(_ context.Context, title string, mode session.Mode) (session.Session, error) {
	w.lastCreateSessionMode = mode
	w.lastCreatedSession = session.Session{ID: "session-a", Title: title, Mode: mode}
	return w.lastCreatedSession, nil
}

func (w *testWorkspace) GetSession(_ context.Context, sessionID string) (session.Session, error) {
	if w.lastCreatedSession.ID == sessionID {
		return w.lastCreatedSession, nil
	}
	return session.Session{ID: sessionID}, nil
}

func (w *testWorkspace) SaveSession(_ context.Context, sess session.Session) (session.Session, error) {
	w.lastSavedSession = sess
	w.lastCreatedSession = sess
	return sess, nil
}

func (w *testWorkspace) ListSessionHistory(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (w *testWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (w *testWorkspace) AgentIsReady() bool {
	return w.agentReady
}

func (w *testWorkspace) AgentIsBusy() bool {
	return false
}

func (w *testWorkspace) AgentQueuedPrompts(string) int {
	return 0
}

func (w *testWorkspace) AgentClearQueue(string) {}

func (w *testWorkspace) AgentCancel(string) {}

func (w *testWorkspace) AgentRun(_ context.Context, sessionID, prompt string, planMode bool, _ ...message.Attachment) error {
	w.lastAgentRunSessionID = sessionID
	w.lastAgentRunPrompt = prompt
	w.lastAgentRunPlanMode = planMode
	return nil
}

func runTestCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if child == nil {
				continue
			}
			_ = child()
		}
	}
}
