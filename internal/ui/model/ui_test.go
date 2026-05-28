package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/notify"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	uichat "github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
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

type unfinishedMessageItem struct {
	*list.Versioned
	id string
}

func (i *unfinishedMessageItem) ID() string { return i.id }

func (i *unfinishedMessageItem) Render(int) string { return "unfinished" }

func (i *unfinishedMessageItem) RawRender(int) string { return "unfinished" }

func (i *unfinishedMessageItem) Finished() bool { return false }

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
	require.Contains(t, ui.runtimeStatusLine(), "dag 1 running/1")
	require.Contains(t, ui.runtimeStatusLine(), "parallel 1")
}

func TestRuntimeStatusLineOmitsIdleCounters(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)

	require.Empty(t, ui.runtimeStatusLine())
}

func TestPillsAreaHeightAccountsForTodoActivityLines(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ws.agentBusy = true
	ui.width = 100
	ui.height = 40
	ui.session = &session.Session{
		ID: "session-a",
		Todos: []session.Todo{
			{Content: "running", ActiveForm: "doing focused work", Status: session.TodoStatusInProgress},
			{Content: "pending", Status: session.TodoStatusPending},
			{Content: "done", Status: session.TodoStatusCompleted},
		},
	}
	ui.pillsExpanded = true
	ui.focusedPillSection = pillSectionTodos

	require.Equal(t, pillHeightWithBorder+3, ui.pillsAreaHeight())
}

func TestSupportedImageMimeOfRejectsEmptyData(t *testing.T) {
	t.Parallel()

	_, ok := supportedImageMimeOf(nil)

	require.False(t, ok)
}

func TestSupportedImageMimeOfAcceptsPNGBytes(t *testing.T) {
	t.Parallel()

	pngHeader := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00,
	}

	mimeType, ok := supportedImageMimeOf(pngHeader)

	require.True(t, ok)
	require.Equal(t, "image/png", mimeType)
}

func TestRuntimeStatusLineShowsActiveSignals(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("test-provider", config.ProviderConfig{
		ID: "test-provider",
		Models: []catwalk.Model{
			{ID: "test-model", ContextWindow: 10_000},
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
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ws.agentBusy = true
	ws.shellStats = shell.BackgroundShellStats{
		Running:        2,
		Completed:      9,
		ActiveMonitors: 1,
	}
	ui.session = &session.Session{
		PromptTokens:     1_000,
		CompletionTokens: 500,
	}

	require.Equal(t, "model running  ·  jobs 2  ·  monitor 1  ·  ctx 15% 1.5K/10K auto@70%", ui.runtimeStatusLine())
}

func TestRuntimeStatusLineShowsUnknownContextWindow(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("test-provider", config.ProviderConfig{
		ID: "test-provider",
		Models: []catwalk.Model{
			{ID: "test-model"},
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
	ui.session = &session.Session{
		PromptTokens:     12_000,
		CompletionTokens: 6_000,
	}

	require.Equal(t, "ctx -- 18K/unknown auto@70%", ui.runtimeStatusLine())
}

func TestAgentFinishedDrainsRunningSubAgents(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.subAgents = []subAgentEntry{
		{
			SessionID:  "child-running",
			ToolCallID: "tool-running",
			LastStatus: "running",
			Status:     subAgentRunning,
		},
		{
			SessionID:  "child-done",
			ToolCallID: "tool-done",
			LastStatus: "done",
			Status:     subAgentDone,
		},
	}

	_ = ui.handleAgentNotification(notify.Notification{
		Type: notify.TypeAgentFinished,
	})

	require.Equal(t, subAgentFailed, ui.subAgents[0].Status)
	require.Equal(t, "failed", ui.subAgents[0].LastStatus)
	require.Equal(t, subAgentDone, ui.subAgents[1].Status)
	require.Equal(t, "done", ui.subAgents[1].LastStatus)
}

func TestAgentFinishedCancelsDanglingAgentTools(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	danglingItem := newTestAgentToolItem(t, ui, "tool-dangling", nil)
	completedItem := newTestAgentToolItem(t, ui, "tool-completed", &message.ToolResult{
		ToolCallID: "tool-completed",
		Content:    "done",
	})
	ui.chat.SetMessages(danglingItem, completedItem)

	_ = ui.handleAgentNotification(notify.Notification{
		Type: notify.TypeAgentFinished,
	})

	require.Equal(t, uichat.ToolStatusCanceled, danglingItem.Status())
	require.Equal(t, uichat.ToolStatusRunning, completedItem.Status())
	require.True(t, completedItem.HasResult())
}

func TestRuntimeStatusLineUsesGeminiFamilyContextWindowFallback(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("antigravity", config.ProviderConfig{
		ID:   "antigravity",
		Type: "antigravity",
		Models: []catwalk.Model{
			{ID: "gemini-2.5-pro"},
		},
	})
	cfg := &config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeBrain: {
				Provider: "antigravity",
				Model:    "gemini-2.5-pro",
			},
		},
		Providers: providers,
		Agents: map[string]config.Agent{
			config.AgentBrain: {Model: config.SelectedModelTypeBrain},
		},
	}
	ui := newTestUIWithConfig(t, cfg)
	ui.session = &session.Session{
		PromptTokens:     200_000,
		CompletionTokens: 3_800,
	}

	require.Equal(t, "ctx 19% 203.8K/1M auto@70%", ui.runtimeStatusLine())
}

func TestRuntimeStatusLineShowsDagProfiles(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.recordTaskRuntimeEvent(scheduler.Event{
		Kind:    scheduler.EventTaskStarted,
		NodeID:  "explore-1",
		Profile: scheduler.ProfileExploreAgent,
	})
	ui.recordTaskRuntimeEvent(scheduler.Event{
		Kind:    scheduler.EventTaskStarted,
		NodeID:  "worker-1",
		Profile: scheduler.ProfileWorkerAgent,
	})

	require.Equal(t, "dag 2 running/2  ·  parallel 2  ·  agents explore 1/worker 1", ui.runtimeStatusLine())
}

func TestRuntimeStatusLineShowsActiveToolParallelism(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.recordToolRuntimeTrace(agentruntime.TaskTrace{
		Kind:       agentruntime.TraceKindToolStarted,
		ToolName:   "rg",
		ToolCallID: "call-1",
	})
	ui.recordToolRuntimeTrace(agentruntime.TaskTrace{
		Kind:       agentruntime.TraceKindToolStarted,
		ToolName:   "view",
		ToolCallID: "call-2",
	})

	require.Equal(t, "tools 2 running  ·  tool-parallel 2  ·  active rg/view", ui.runtimeStatusLine())

	ui.recordToolRuntimeTrace(agentruntime.TaskTrace{
		Kind:       agentruntime.TraceKindToolFinished,
		ToolName:   "rg",
		ToolCallID: "call-1",
	})

	require.Equal(t, "tools 1 running  ·  tool-parallel 1  ·  active view", ui.runtimeStatusLine())
}

func TestRuntimeTraceAddsCompactionActivity(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.session = &session.Session{ID: "session-1"}
	startedAt := time.Now().Add(-12 * time.Second)

	_, _ = ui.Update(pubsub.Event[agentruntime.TaskTrace]{
		Type: pubsub.CreatedEvent,
		Payload: agentruntime.TaskTrace{
			Kind:                          agentruntime.TraceKindConversationCompactionStarted,
			ConversationSessionID:         "session-1",
			StartedAt:                     startedAt,
			ProviderID:                    "openai",
			ModelID:                       "gpt-test",
			ContextMessageCount:           9,
			PreflightEstimatedInputTokens: 58_000,
		},
	})

	item, ok := ui.chat.MessageItem(compactionActivityID("session-1")).(*uichat.RuntimeActivityItem)
	require.True(t, ok)
	require.Equal(t, uichat.RuntimeActivityRunning, item.Snapshot().Status)
	require.Contains(t, ui.runtimeStatusLine(), "compacting")
	require.Contains(t, ui.runtimeStatusLine(), "~58K tokens")

	_, _ = ui.Update(pubsub.Event[agentruntime.TaskTrace]{
		Type: pubsub.UpdatedEvent,
		Payload: agentruntime.TaskTrace{
			Kind:                  agentruntime.TraceKindConversationCompactionFinished,
			ConversationSessionID: "session-1",
			StartedAt:             startedAt,
			FinishedAt:            time.Now(),
			ProviderID:            "openai",
			ModelID:               "gpt-test",
			TotalTokens:           61_500,
		},
	})

	require.NotContains(t, ui.runtimeStatusLine(), "compacting")
	require.Equal(t, uichat.RuntimeActivityDone, item.Snapshot().Status)
	require.Equal(t, int64(61_500), item.Snapshot().Tokens)
	require.True(t, item.Snapshot().TokensAreExact)
}

func TestRuntimeTraceAddsMemoryActivity(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.session = &session.Session{ID: "session-1"}
	startedAt := time.Now().Add(-2 * time.Second)

	_, _ = ui.Update(pubsub.Event[agentruntime.TaskTrace]{
		Type: pubsub.CreatedEvent,
		Payload: agentruntime.TaskTrace{
			Kind:                  agentruntime.TraceKindMemoryRecallStarted,
			ConversationSessionID: "session-1",
			StartedAt:             startedAt,
		},
	})

	item, ok := ui.chat.MessageItem(memoryActivityID("session-1", uichat.RuntimeActivityMemoryRecall)).(*uichat.RuntimeActivityItem)
	require.True(t, ok)
	require.Equal(t, uichat.RuntimeActivityRunning, item.Snapshot().Status)
	require.Contains(t, ui.runtimeStatusLine(), "memory recall")

	_, _ = ui.Update(pubsub.Event[agentruntime.TaskTrace]{
		Type: pubsub.UpdatedEvent,
		Payload: agentruntime.TaskTrace{
			Kind:                  agentruntime.TraceKindMemoryRecallFinished,
			ConversationSessionID: "session-1",
			StartedAt:             startedAt,
			FinishedAt:            time.Now(),
			FileCount:             2,
		},
	})

	require.Equal(t, uichat.RuntimeActivityDone, item.Snapshot().Status)
	require.Equal(t, "Memory recalled", item.Snapshot().Title)
	require.Equal(t, "2 memories", item.Snapshot().Detail)
	require.NotContains(t, ui.runtimeStatusLine(), "memory recall")
}

func TestBackgroundMonitorEventStaysOutOfChat(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.session = &session.Session{ID: "session-1"}
	ws := ui.com.Workspace.(*testWorkspace)
	ws.shellStats = shell.BackgroundShellStats{ActiveMonitors: 1}

	_, _ = ui.Update(pubsub.Event[shell.BackgroundJobEvent]{
		Type: pubsub.CreatedEvent,
		Payload: shell.BackgroundJobEvent{
			Kind:      shell.BackgroundKindMonitorLine,
			ID:        "001",
			SessionID: "session-1",
			MatchLine: "sample 50 still running",
		},
	})

	require.Nil(t, ui.chat.MessageItem("runtime:monitor:session-1:001"))
	require.Contains(t, ui.runtimeStatusLine(), "monitor 1")

	_, _ = ui.Update(pubsub.Event[shell.BackgroundJobEvent]{
		Type: pubsub.UpdatedEvent,
		Payload: shell.BackgroundJobEvent{
			Kind:      shell.BackgroundKindMonitorHit,
			ID:        "001",
			SessionID: "session-1",
			Pattern:   "done",
			MatchLine: "done in 42s",
		},
	})

	require.Nil(t, ui.chat.MessageItem("runtime:monitor:session-1:001"))
	require.Contains(t, ui.runtimeStatusLine(), "monitor 1")
}

func TestRuntimeStatusLineUsesLatestLLMTraceContextUsage(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("test-provider", config.ProviderConfig{
		ID: "test-provider",
		Models: []catwalk.Model{
			{ID: "test-model", ContextWindow: 1_000_000},
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
	ui.session = &session.Session{ID: "session-1"}

	ui.recordLLMContextTrace(agentruntime.TaskTrace{
		Kind:                          agentruntime.TraceKindLLMStarted,
		ConversationSessionID:         "session-1",
		Sequence:                      1,
		PreflightEstimatedInputTokens: 58_000,
		ContextWindowTokens:           1_000_000,
	})

	require.Equal(t, "ctx 5% 58K/1M auto@70%", ui.runtimeStatusLine())

	ui.recordLLMContextTrace(agentruntime.TaskTrace{
		Kind:                  agentruntime.TraceKindLLMFinished,
		ConversationSessionID: "session-1",
		Sequence:              2,
		TotalTokens:           118_275,
		ContextWindowTokens:   1_000_000,
	})

	require.Equal(t, "ctx 11% 118.3K/1M auto@70%", ui.runtimeStatusLine())
}

func TestContextUsagePercentWithoutSession(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, &config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Agents: map[string]config.Agent{
			config.AgentBrain: {Model: config.SelectedModelTypeBrain},
		},
	})

	require.Empty(t, ui.contextUsagePercent())
}

func TestRuntimeStatusLineShowsIdleContext(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("test-provider", config.ProviderConfig{
		ID: "test-provider",
		Models: []catwalk.Model{
			{ID: "test-model", ContextWindow: 200_000},
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
	ui.session = &session.Session{
		PromptTokens:     140_000,
		CompletionTokens: 1_000,
	}

	require.Equal(t, "ctx 70% 141K/200K auto@70% compact", ui.runtimeStatusLine())
}

func TestRuntimeStatusLineUsesRuntimeAgentContextWindowFallback(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("test-provider", config.ProviderConfig{
		ID: "test-provider",
		Models: []catwalk.Model{
			{ID: "test-model"},
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
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ws.agentModel = workspace.AgentModel{
		CatwalkCfg: catwalk.Model{ContextWindow: 120_000},
	}
	ui.session = &session.Session{
		PromptTokens:     12_000,
		CompletionTokens: 6_000,
	}

	require.Equal(t, "ctx 15% 18K/120K auto@70%", ui.runtimeStatusLine())
}

func TestStatusClearUsesMessageID(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	firstID := ui.status.SetInfoMsg(util.NewInfoMsg("old"))
	secondID := ui.status.SetInfoMsg(util.NewErrorMsg(errors.New("assert error")))

	ui.status.ClearInfoMsg(firstID)
	require.Equal(t, secondID, ui.status.msgID)
	require.Equal(t, "assert error", ui.status.msg.Msg)

	ui.status.ClearInfoMsg(secondID)
	require.Empty(t, ui.status.msg.Msg)
}

func TestStatusMessageTTLSeverityMinimums(t *testing.T) {
	t.Parallel()

	require.Equal(t, DefaultStatusTTL, statusMessageTTL(util.NewInfoMsg("info")))
	require.Equal(t, DefaultWarnStatusTTL, statusMessageTTL(util.NewWarnMsg("warn")))
	require.Equal(t, DefaultErrorStatusTTL, statusMessageTTL(util.NewErrorMsg(errors.New("assert error"))))
	require.Equal(t, 2*time.Minute, statusMessageTTL(util.InfoMsg{
		Type: util.InfoTypeError,
		Msg:  "long",
		TTL:  2 * time.Minute,
	}))
}

func TestStatusDrawKeepsRuntimeLineDuringNotification(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.status.SetStatusLine("model running  ·  ctx -- 18K/unknown auto@70%")
	ui.status.SetInfoMsg(util.NewInfoMsg("Task started: inspect repository"))
	scr := uv.NewScreenBuffer(80, 1)

	ui.status.Draw(scr, uv.Rect(0, 0, 80, 1))

	rendered := scr.Render()
	require.Contains(t, rendered, "Task started")
	require.Contains(t, rendered, "ctx")
	require.Contains(t, rendered, "auto@70%")
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
	setScrollableChatItems(ui, 6, 3)
	ui.chat.ForceScrollToBottom()
	ui.layout.main = uv.Rect(0, 5, 20, 3)

	startIdx, startLine := ui.chat.list.ScrollOffset()
	handled, _ := ui.chat.HandleMouseDown(ansi.MouseButton1, 2, 1)
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

func TestStreamingTickDoesNotOverrideUnlockedScroll(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ws.agentBusy = true
	setScrollableChatItems(ui, 12, 3)

	ui.chat.ForceScrollToBottom()
	require.True(t, ui.chat.Follow())
	ui.chat.ScrollBy(-1)
	require.False(t, ui.chat.Follow())
	require.False(t, ui.chat.AtBottom())

	_, _ = ui.Update(uichat.StepMsg{ID: "not-visible"})

	require.False(t, ui.chat.Follow())
	require.False(t, ui.chat.AtBottom())
}

func TestMouseWheelDownToBottomReengagesFollow(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	setScrollableChatItems(ui, 12, 3)

	ui.chat.ForceScrollToBottom()
	ui.chat.ScrollBy(-6)
	require.False(t, ui.chat.Follow())
	require.False(t, ui.chat.AtBottom())

	for range 4 {
		_, _ = ui.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}

	require.True(t, ui.chat.AtBottom())
	require.True(t, ui.chat.Follow())
}

func TestDoubleClickUserInputBlockCopiesWholeMessage(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.chat.SetSize(80, 10)
	msg := &message.Message{
		ID:   "user-copy",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "copy this whole input block"},
		},
	}
	renderer := attachments.NewRenderer(
		ui.com.Styles.Attachments.Normal,
		ui.com.Styles.Attachments.Deleting,
		ui.com.Styles.Attachments.Image,
		ui.com.Styles.Attachments.Text,
	)
	item := uichat.NewUserMessageItem(ui.com.Styles, msg, renderer)
	ui.chat.SetMessages(item)

	handled, firstCmd := ui.chat.HandleMouseDown(ansi.MouseButton1, 2, 0)
	require.True(t, handled)
	require.NotNil(t, firstCmd)

	handled, secondCmd := ui.chat.HandleMouseDown(ansi.MouseButton1, 2, 0)
	require.True(t, handled)
	require.NotNil(t, secondCmd)
	require.False(t, ui.chat.HasHighlight())
}

func TestEscapeStopsMouseAutoScroll(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	setScrollableChatItems(ui, 6, 3)
	ui.chat.ForceScrollToBottom()
	ui.layout.main = uv.Rect(0, 5, 20, 3)

	handled, _ := ui.chat.HandleMouseDown(ansi.MouseButton1, 2, 1)
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

func setScrollableChatItems(ui *UI, itemCount int, height int) {
	items := make([]list.Item, 0, itemCount)
	for i := range itemCount {
		items = append(items, newDragScrollTestItem(string(rune('a'+i))))
	}
	ui.chat.list.SetGap(0)
	ui.chat.list.SetItems(items...)
	ui.chat.SetSize(20, height)
	ui.state = uiChat
	ui.focus = uiFocusMain
	ui.layout.main = uv.Rect(0, 0, 20, height)
}

func TestEscapeCancelsAgentAndShowsInterruptDivider(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ws.agentBusy = true
	ws.cancelWasRunning = true
	ws.cancelPrompts = []string{"queued follow-up"}
	ui.session = &session.Session{ID: "session-a", Mode: session.ModeExecute}
	ui.textarea.SetValue("draft")

	cmd := ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	require.Equal(t, "session-a", ws.cancelSessionID)
	require.Contains(t, ui.textarea.Value(), "draft")
	require.Contains(t, ui.textarea.Value(), "queued follow-up")
	require.NotNil(t, ui.chat.MessageItem(uichat.InterruptDividerID("esc-cancel-session-a")))

	runTestCmd(cmd)
	require.Equal(t, "session-a", ws.repairedSessionID)
}

func TestInterruptDividerUsesUnfinishedMessageID(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.session = &session.Session{ID: "session-a", Mode: session.ModeExecute}
	ui.chat.SetMessages(&unfinishedMessageItem{
		Versioned: list.NewVersioned(),
		id:        "assistant-a",
	})

	ui.appendInterruptDivider()

	require.NotNil(t, ui.chat.MessageItem(uichat.InterruptDividerID("assistant-a")))
}

func TestLoadPromptHistoryUsesCurrentSessionMessages(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ui.session = &session.Session{ID: "session-a", Mode: session.ModeExecute}
	ws.userMessages = []message.Message{{
		ID:        "message-a",
		SessionID: "session-a",
		Role:      message.User,
		Parts:     []message.ContentPart{message.TextContent{Text: "previous prompt"}},
	}}

	msg := ui.loadPromptHistory()()
	loaded := msg.(promptHistoryLoadedMsg)

	require.Equal(t, "session-a", ws.userMessagesSessionID)
	require.False(t, ws.allUserMessagesCalled)
	require.Len(t, loaded.messages, 1)
	require.Equal(t, "previous prompt", loaded.messages[0])
}

func TestLoadPromptHistorySkipsInternalSummaryPrompt(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ui.session = &session.Session{ID: "session-a", Mode: session.ModeExecute}
	ws.userMessages = []message.Message{
		{
			ID:        "summary-prompt",
			SessionID: "session-a",
			Role:      message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Compress the conversation into durable memory for the next agent.\nPreserve the minimum state needed to resume."},
			},
		},
		{
			ID:        "message-a",
			SessionID: "session-a",
			Role:      message.User,
			Parts:     []message.ContentPart{message.TextContent{Text: "real user prompt"}},
		},
	}

	msg := ui.loadPromptHistory()()
	loaded := msg.(promptHistoryLoadedMsg)

	require.Equal(t, []string{"real user prompt"}, loaded.messages)
}

func newTestAgentToolItem(
	t *testing.T,
	ui *UI,
	toolCallID string,
	result *message.ToolResult,
) *uichat.AgentToolMessageItem {
	t.Helper()

	input, err := json.Marshal(agent.AgentParams{
		Prompt: "inspect state",
		Role:   config.AgentExplore,
	})
	require.NoError(t, err)

	return uichat.NewAgentToolMessageItem(ui.com.Styles, message.ToolCall{
		ID:    toolCallID,
		Name:  agent.AgentToolName,
		Input: string(input),
	}, result, false)
}

// testWorkspace is a minimal [workspace.Workspace] stub for unit tests.
type testWorkspace struct {
	workspace.Workspace
	cfg                   *config.Config
	agentReady            bool
	agentModel            workspace.AgentModel
	agentBusy             bool
	lastCreateSessionMode session.Mode
	lastCreatedSession    session.Session
	lastSavedSession      session.Session
	lastAgentRunPlanMode  bool
	lastAgentRunPrompt    string
	lastAgentRunSessionID string
	userMessages          []message.Message
	userMessagesSessionID string
	allUserMessagesCalled bool
	repairedSessionID     string
	cancelSessionID       string
	cancelPrompts         []string
	cancelWasRunning      bool
	shellStats            shell.BackgroundShellStats
}

func (w *testWorkspace) Config() *config.Config {
	return w.cfg
}

func (w *testWorkspace) WorkingDir() string {
	return "/tmp/crush-ui-test"
}

func (w *testWorkspace) ProjectNeedsInitialization() (bool, error) {
	return false, nil
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

func (w *testWorkspace) ListUserMessages(_ context.Context, sessionID string) ([]message.Message, error) {
	w.userMessagesSessionID = sessionID
	return w.userMessages, nil
}

func (w *testWorkspace) ListAllUserMessages(context.Context) ([]message.Message, error) {
	w.allUserMessagesCalled = true
	return w.userMessages, nil
}

func (w *testWorkspace) RepairSessionMessages(_ context.Context, sessionID string) error {
	w.repairedSessionID = sessionID
	return nil
}

func (w *testWorkspace) ListSessionHistory(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (w *testWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (w *testWorkspace) LSPGetStates() map[string]workspace.LSPClientInfo {
	return nil
}

func (w *testWorkspace) LSPGetDiagnosticCounts(string) lsp.DiagnosticCounts {
	return lsp.DiagnosticCounts{}
}

func (w *testWorkspace) MCPGetStates() map[string]mcptools.ClientInfo {
	return nil
}

func (w *testWorkspace) AgentIsReady() bool {
	return w.agentReady
}

func (w *testWorkspace) AgentModel() workspace.AgentModel {
	return w.agentModel
}

func (w *testWorkspace) AgentIsBusy() bool {
	return w.agentBusy
}

func (w *testWorkspace) AgentQueuedPrompts(string) int {
	return 0
}

func (w *testWorkspace) BackgroundShellStats() shell.BackgroundShellStats {
	return w.shellStats
}

func (w *testWorkspace) AgentClearQueue(string) {}

func (w *testWorkspace) AgentCancel(string) {}

func (w *testWorkspace) AgentCancelAndFlush(sessionID string) ([]string, bool) {
	w.cancelSessionID = sessionID
	return w.cancelPrompts, w.cancelWasRunning
}

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
			// Recurse: nested batches (e.g. the `/accept` slash command
			// returns Batch(toast, sendMessage(...)) and sendMessage itself
			// returns another Batch) need to be drained too — otherwise the
			// inner AgentRun closure never fires and the workspace stub
			// records nothing.
			runTestCmd(child)
		}
	}
}

// submitEnter wires up the bare minimum UI state for an Enter keypress in the
// editor to take the SendMessage code path, then fires the keypress and runs
// any returned tea.Cmd. Returns the Cmd unchanged so callers can re-assert.
func submitEnter(t *testing.T, ui *UI) tea.Cmd {
	t.Helper()
	ui.state = uiChat
	ui.focus = uiFocusEditor
	cmd := ui.handleKeyPressMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	runTestCmd(cmd)
	return cmd
}

func TestPlanModeAcceptCommandFlipsToExecuteAndSendsMessage(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ui.session = &session.Session{ID: "session-a", Mode: session.ModePlan}
	ui.textarea.SetValue("/accept")

	submitEnter(t, ui)

	// After /accept: mode flips to execute, an implementation prompt is sent,
	// and the saved session reflects the new mode.
	require.Equal(t, session.ModeExecute, ui.currentSessionMode(), "/accept must flip mode")
	require.Equal(t, session.ModeExecute, ws.lastSavedSession.Mode, "session must be persisted with execute mode")
	require.NotEmpty(t, ws.lastAgentRunPrompt, "an implementation prompt must be dispatched")
	require.False(t, ws.lastAgentRunPlanMode, "AgentRun must run as execute, not plan")
	require.Contains(t, ws.lastAgentRunPrompt, "Implement", "prompt should ask brain to implement")
}

func TestPlanModeCancelCommandFlipsToExecuteWithoutSending(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ui.session = &session.Session{ID: "session-a", Mode: session.ModePlan}
	ui.textarea.SetValue("/cancel-plan")

	submitEnter(t, ui)

	require.Equal(t, session.ModeExecute, ui.currentSessionMode(), "/cancel-plan flips mode")
	require.Equal(t, session.ModeExecute, ws.lastSavedSession.Mode)
	require.Empty(t, ws.lastAgentRunPrompt, "/cancel-plan must NOT dispatch any prompt")
}

func TestPlanModeAcceptOutsidePlanModeIsRejected(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ws := ui.com.Workspace.(*testWorkspace)
	ws.agentReady = true
	ui.session = &session.Session{ID: "session-a", Mode: session.ModeExecute}
	ui.textarea.SetValue("/accept")

	submitEnter(t, ui)

	require.Empty(t, ws.lastAgentRunPrompt, "/accept outside plan mode must be a no-op")
	require.Equal(t, session.ModeExecute, ui.currentSessionMode())
}

func TestInputAnyChangeScrollsToBottom(t *testing.T) {
	t.Parallel()

	ui := newTestUIWithConfig(t, nil)
	ui.state = uiChat
	ui.focus = uiFocusEditor
	ui.session = &session.Session{ID: "test-session"}

	// Simulate some chat history
	setScrollableChatItems(ui, 20, 10)
	ui.chat.ScrollToTop()
	require.False(t, ui.chat.AtBottom())
	require.False(t, ui.chat.Follow())

	// Verify the logic directly
	ui.handleTextareaHeightChange(ui.textarea.Height())
	require.True(t, ui.chat.Follow(), "Any input change should lock scroll to bottom")
}
