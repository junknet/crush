package model

import (
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestTUIAgentFinishedCleansDanglingAgentVisualState(t *testing.T) {
	if os.Getenv("CRUSH_TUI_E2E_AGENT_FINISH") != "1" {
		t.Skip("set CRUSH_TUI_E2E_AGENT_FINISH=1 to run the tmux TUI e2e harness")
	}

	ui, ws := newAgentFinishE2EUI(t)
	program := tea.NewProgram(ui)

	go func() {
		time.Sleep(3 * time.Second)
		ws.agentBusy = false
		program.Send(pubsub.Event[notify.Notification]{
			Type: pubsub.CreatedEvent,
			Payload: notify.Notification{
				Type:         notify.TypeAgentFinished,
				SessionID:    "session-a",
				SessionTitle: "E2E Session",
			},
		})
		time.Sleep(10 * time.Second)
		program.Quit()
	}()

	_, err := program.Run()
	require.NoError(t, err)
}

func TestTUIMemoryRecallRendersInternalActivityOnly(t *testing.T) {
	if os.Getenv("CRUSH_TUI_E2E_MEMORY_RECALL") != "1" {
		t.Skip("set CRUSH_TUI_E2E_MEMORY_RECALL=1 to run the tmux TUI e2e harness")
	}

	ui := newMemoryRecallE2EUI(t)
	program := tea.NewProgram(ui)
	startedAt := time.Now()

	go func() {
		time.Sleep(500 * time.Millisecond)
		program.Send(pubsub.Event[agentruntime.TaskTrace]{
			Type: pubsub.CreatedEvent,
			Payload: agentruntime.TaskTrace{
				Kind:                  agentruntime.TraceKindMemoryRecallStarted,
				ConversationSessionID: "session-a",
				StartedAt:             startedAt,
			},
		})
		time.Sleep(3 * time.Second)
		program.Send(pubsub.Event[agentruntime.TaskTrace]{
			Type: pubsub.UpdatedEvent,
			Payload: agentruntime.TaskTrace{
				Kind:                  agentruntime.TraceKindMemoryRecallFinished,
				ConversationSessionID: "session-a",
				StartedAt:             startedAt,
				FinishedAt:            time.Now(),
				FileCount:             1,
			},
		})
		time.Sleep(8 * time.Second)
		program.Quit()
	}()

	_, err := program.Run()
	require.NoError(t, err)
}

func newMemoryRecallE2EUI(t *testing.T) *UI {
	t.Helper()

	cfg := agentFinishE2EConfig()
	ws := &testWorkspace{cfg: cfg}
	ws.agentReady = true
	ws.agentBusy = false
	ws.agentModel = workspace.AgentModel{
		CatwalkCfg: catwalk.Model{ID: "e2e-model", Name: "E2E Model", ContextWindow: 10_000},
		ModelCfg:   config.SelectedModel{Provider: "e2e-provider", Model: "e2e-model"},
	}
	com := common.DefaultCommon(ws)
	com.Workspace = ws
	ui := New(com, "", false)
	ui.width = 140
	ui.height = 36
	ui.session = &session.Session{ID: "session-a", Title: "E2E Session", Mode: session.ModeExecute}
	ui.setState(uiChat, uiFocusEditor)
	ui.isCompact = true
	ui.chat.SetMessages()
	ui.chat.ForceScrollToBottom()
	return ui
}

func newAgentFinishE2EUI(t *testing.T) (*UI, *testWorkspace) {
	t.Helper()

	cfg := agentFinishE2EConfig()
	ws := &testWorkspace{cfg: cfg}
	ws.agentReady = true
	ws.agentBusy = true
	ws.agentModel = workspace.AgentModel{
		CatwalkCfg: catwalk.Model{ID: "e2e-model", Name: "E2E Model", ContextWindow: 10_000},
		ModelCfg:   config.SelectedModel{Provider: "e2e-provider", Model: "e2e-model"},
	}
	com := common.DefaultCommon(ws)
	com.Workspace = ws
	ui := New(com, "", false)

	ui.width = 140
	ui.height = 36
	ui.session = &session.Session{ID: "session-a", Title: "E2E Session", Mode: session.ModeExecute}
	ui.setState(uiChat, uiFocusEditor)
	ui.isCompact = true
	ui.subAgents = recordSubAgentEvent(nil, notify.Notification{
		Type:               notify.TypeSubAgentStarted,
		SessionID:          "child-a",
		SubAgentToolCallID: "tool-dangling",
		SubAgentPrompt:     "inspect dangling explore state",
		SubAgentProfile:    string(scheduler.ProfileExploreAgent),
	})

	danglingItem := newTestAgentToolItem(t, ui, "tool-dangling", nil)
	completedItem := newTestAgentToolItem(t, ui, "tool-completed", &message.ToolResult{
		ToolCallID: "tool-completed",
		Content:    "completed result",
	})
	ui.chat.SetMessages(danglingItem, completedItem)
	return ui, ws
}

func agentFinishE2EConfig() *config.Config {
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("e2e-provider", config.ProviderConfig{
		ID:   "e2e-provider",
		Name: "E2E Provider",
		Models: []catwalk.Model{
			{ID: "e2e-model", Name: "E2E Model", ContextWindow: 10_000},
		},
	})

	return &config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeBrain: {
				Provider: "e2e-provider",
				Model:    "e2e-model",
			},
		},
		Providers: providers,
		Options: &config.Options{
			TUI: &config.TUIOptions{CompactMode: true},
		},
		Agents: map[string]config.Agent{
			config.AgentBrain: {Model: config.SelectedModelTypeBrain},
		},
	}
}
