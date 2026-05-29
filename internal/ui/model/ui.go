package model

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/relay"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/completions"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	fimage "github.com/charmbracelet/crush/internal/ui/image"
	"github.com/charmbracelet/crush/internal/ui/logo"
	"github.com/charmbracelet/crush/internal/ui/notification"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"
	"github.com/charmbracelet/ultraviolet/screen"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/editor"
	xstrings "github.com/charmbracelet/x/exp/strings"
)

// MouseScrollThreshold defines how many lines to scroll the chat when a mouse
// wheel event occurs.
const MouseScrollThreshold = 5

// mouseAutoScrollInterval is the delay between drag-to-scroll ticks at the
// top or bottom edge of the chat viewport.
const mouseAutoScrollInterval = 50 * time.Millisecond

// mouseAutoScrollStep defines how many lines each drag-to-scroll tick moves.
const mouseAutoScrollStep = 1

// Compact mode breakpoints.
const (
	compactModeWidthBreakpoint  = 120
	compactModeHeightBreakpoint = 30
)

const autoCompactContextPercent = 70

// If pasted text has more than 10 newlines, treat it as a file attachment.
const pasteLinesThreshold = 10

// If pasted text has more than 1000 columns, treat it as a file attachment.
const pasteColsThreshold = 1000

// Session details panel max height.
const sessionDetailsMaxHeight = 20

// TextareaMaxHeight is the maximum height of the prompt textarea.
const TextareaMaxHeight = 15

// editorHeightMargin is the vertical margin added to the textarea height to
// account for the attachments row (top) and bottom margin.
const editorHeightMargin = 2

// TextareaMinHeight is the minimum height of the prompt textarea.
// One row so the `:::` continuation prompts only appear when the user
// is actually typing a multi-line message — single-line prompts sit
// flush against the editor's `>` glyph with no empty `:::` filler.
const TextareaMinHeight = 1

// uiFocusState represents the current focus state of the UI.
type uiFocusState uint8

// Possible uiFocusState values.
const (
	uiFocusNone uiFocusState = iota
	uiFocusEditor
	uiFocusMain
)

type uiState uint8

// Possible uiState values.
const (
	uiOnboarding uiState = iota
	uiInitialize
	uiLanding
	uiChat
)

type openEditorMsg struct {
	Text string
}

type mouseAutoScrollMsg struct {
	Token     int
	Direction int
}

type (
	// ctrlCTimerExpiredMsg is sent when the Ctrl+C quit arm expires.
	ctrlCTimerExpiredMsg struct{}
	// userCommandsLoadedMsg is sent when user commands are loaded.
	userCommandsLoadedMsg struct {
		Commands []commands.CustomCommand
	}
	// mcpPromptsLoadedMsg is sent when mcp prompts are loaded.
	mcpPromptsLoadedMsg struct {
		Prompts []commands.MCPPrompt
	}
	// mcpStateChangedMsg is sent when there is a change in MCP client states.
	mcpStateChangedMsg struct {
		states map[string]mcp.ClientInfo
	}
	// sendMessageMsg is sent to send a message.
	// currently only used for mcp prompts.
	sendMessageMsg struct {
		Content     string
		Attachments []message.Attachment
	}

	// closeDialogMsg is sent to close the current dialog.
	closeDialogMsg struct{}

	restoreCanceledPromptMsg struct {
		sessionID string
		text      string
	}

	// hyperRefreshDoneMsg is sent after a silent Hyper OAuth refresh
	// finishes. It carries the original model-selection action so the
	// selection can be resumed.
	hyperRefreshDoneMsg struct {
		action dialog.ActionSelectModel
	}

	// copyChatHighlightMsg is sent to copy the current chat highlight to clipboard.
	copyChatHighlightMsg struct{}

	// sessionFilesUpdatesMsg is sent when the files for this session have been updated
	sessionFilesUpdatesMsg struct {
		sessionFiles []SessionFile
	}
	// creditsUpdatedMsg is sent when the remaining Hyper credits have been
	// fetched from the API.
	creditsUpdatedMsg struct {
		credits int
	}
	// todoRecentlyCompletedMsg is sent when a task completed-at TTL expires.
	todoRecentlyCompletedMsg struct{}
)

// UI represents the main user interface model.
type UI struct {
	com          *common.Common
	session      *session.Session
	sessionFiles []SessionFile

	// keeps track of read files while we don't have a session id
	sessionFileReads []string

	// initialSessionID is set when loading a specific session on startup.
	initialSessionID string
	// continueLastSession is set to continue the most recent session on startup.
	continueLastSession bool

	lastUserMessageTime int64

	// The width and height of the terminal in cells.
	width  int
	height int
	layout uiLayout

	isTransparent bool

	focus uiFocusState
	state uiState

	keyMap KeyMap
	keyenh tea.KeyboardEnhancementsMsg

	dialog *dialog.Overlay
	status *Status

	// ctrlCArmed tracks whether a second Ctrl+C should quit the app.
	ctrlCArmed bool
	// ctrlCArmedAt records when the quit arm was last activated.
	ctrlCArmedAt time.Time

	header *header

	headerAnimFrame   int
	headerAnimTicking bool

	// sendProgressBar instructs the TUI to send progress bar updates to the
	// terminal.
	sendProgressBar    bool
	progressBarEnabled bool

	// caps hold different terminal capabilities that we query for.
	caps common.Capabilities

	// Editor components
	textarea textarea.Model

	// Attachment list
	attachments *attachments.Attachments

	readyPlaceholder   string
	workingPlaceholder string

	// Completions state
	completions              *completions.Completions
	completionsOpen          bool
	completionsStartIndex    int
	completionsQuery         string
	completionsPositionStart image.Point // x,y where user typed '@'

	// Chat components
	chat *Chat

	// onboarding state
	onboarding struct {
		yesInitializeSelected bool
	}

	// lsp
	lspStates map[string]app.LSPClientInfo

	// mcp
	mcpStates map[string]mcp.ClientInfo

	// skills
	skillStates []*skills.SkillState

	// subAgents holds recent and active sub-agent events for the sidebar.
	// Capped to subAgentHistoryMax entries; oldest gets evicted.
	subAgents []subAgentEntry
	// taskRuntimeEvents holds current task DAG lifecycle events for the bottom
	// runtime line. It is updated only from scheduler pub/sub messages.
	taskRuntimeEvents map[string]scheduler.Event
	// toolRuntimeEvents holds current tool lifecycle traces for the bottom
	// runtime line. It is updated from runtime trace pub/sub messages.
	toolRuntimeEvents map[string]agentruntime.TaskTrace
	// latestLLMContextTrace holds the newest LLM request trace with context
	// sizing data for the current session. It drives the bottom context line
	// while the request is still running, before session token totals persist.
	latestLLMContextTrace agentruntime.TaskTrace
	// runtimeActivities are updatable chat rows for non-message runtime work
	// such as compaction and monitor watches.
	runtimeActivities map[string]*chat.RuntimeActivityItem

	// sidebarLogo keeps a cached version of the sidebar sidebarLogo.
	sidebarLogo string

	// Notification state
	notifyBackend       notification.Backend
	notifyWindowFocused bool
	// custom commands & mcp commands
	customCommands []commands.CustomCommand
	mcpPrompts     []commands.MCPPrompt

	// forceCompactMode tracks whether compact mode is forced by user toggle
	forceCompactMode bool

	// pendingSessionMode is the mode used for the next brand-new session.
	pendingSessionMode session.Mode

	// isCompact tracks whether we're currently in compact layout mode (either
	// by user toggle or auto-switch based on window size)
	isCompact bool

	// detailsOpen tracks whether the runtime activity panel is open.
	detailsOpen bool

	// pills state
	pillsExpanded      bool
	focusedPillSection pillSection
	promptQueue        int
	pillsView          string

	// Todo spinner
	todoSpinner    spinner.Model
	todoIsSpinning bool

	// pendingModelSwitch holds a model selection that arrived while the agent
	// was busy. It is applied automatically once the agent goes idle, instead
	// of being silently dropped with a transient warning.
	pendingModelSwitch *dialog.ActionSelectModel

	// mouse highlighting related state
	lastClickTime time.Time
	// mouseAutoScroll tracks drag-to-scroll state while the pointer is pinned
	// to the top or bottom edge of the chat viewport.
	mouseAutoScrollToken     int
	mouseAutoScrollDirection int
	mouseAutoScrollPending   bool

	// hyperCredits is the remaining Hyper credits, updated after each prompt.
	hyperCredits *int

	// Prompt history for up/down navigation through previous messages.
	promptHistory struct {
		messages []string
		index    int
		draft    string
	}
}

// New creates a new instance of the [UI] model.
func New(com *common.Common, initialSessionID string, continueLast bool) *UI {
	// Editor components
	ta := textarea.New()
	ta.SetStyles(com.Styles.Editor.Textarea)
	ta.ShowLineNumbers = false
	ta.CharLimit = -1
	ta.SetVirtualCursor(false)
	ta.DynamicHeight = true
	ta.MinHeight = TextareaMinHeight
	ta.MaxHeight = TextareaMaxHeight
	ta.Focus()

	ch := NewChat(com)

	keyMap := DefaultKeyMap()

	// Completions component
	comp := completions.New(
		com.Styles.Completions.Normal,
		com.Styles.Completions.Focused,
		com.Styles.Completions.Match,
	)

	todoSpinner := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(com.Styles.Pills.TodoSpinner),
	)

	// Attachments component
	attachments := attachments.New(
		attachments.NewRenderer(
			com.Styles.Attachments.Normal,
			com.Styles.Attachments.Deleting,
			com.Styles.Attachments.Image,
			com.Styles.Attachments.Text,
		),
		attachments.Keymap{
			DeleteMode: keyMap.Editor.AttachmentDeleteMode,
			DeleteAll:  keyMap.Editor.DeleteAllAttachments,
			Escape:     keyMap.Editor.Escape,
		},
	)

	header := newHeader(com)

	ui := &UI{
		com:                 com,
		dialog:              dialog.NewOverlay(),
		keyMap:              keyMap,
		textarea:            ta,
		chat:                ch,
		header:              header,
		completions:         comp,
		attachments:         attachments,
		todoSpinner:         todoSpinner,
		lspStates:           make(map[string]app.LSPClientInfo),
		mcpStates:           make(map[string]mcp.ClientInfo),
		pendingSessionMode:  session.ModeExecute,
		notifyBackend:       notification.NoopBackend{},
		notifyWindowFocused: true,
		initialSessionID:    initialSessionID,
		continueLastSession: continueLast,
		skillStates:         skills.GetLatestStates(),
		pillsExpanded:       true,
	}

	status := NewStatus(com, ui)

	ui.setEditorPrompt()
	ui.randomizePlaceholders()
	ui.textarea.Placeholder = ui.readyPlaceholder
	ui.status = status

	// Initialize compact mode from config
	ui.forceCompactMode = com.Config().Options.TUI.CompactMode

	// set onboarding state defaults
	ui.onboarding.yesInitializeSelected = true

	desiredState := uiLanding
	desiredFocus := uiFocusEditor
	if !com.Config().IsConfigured() {
		desiredState = uiOnboarding
	} else if n, _ := com.Workspace.ProjectNeedsInitialization(); n {
		desiredState = uiInitialize
	}

	// set initial state
	ui.setState(desiredState, desiredFocus)

	opts := com.Config().Options

	// disable indeterminate progress bar
	ui.progressBarEnabled = opts.Progress == nil || *opts.Progress
	// enable transparent mode
	ui.isTransparent = opts.TUI.Transparent != nil && *opts.TUI.Transparent

	return ui
}

// Init initializes the UI model.
func (m *UI) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.state == uiOnboarding {
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// load the user commands async
	cmds = append(cmds, m.loadCustomCommands())
	// load prompt history async
	cmds = append(cmds, m.loadPromptHistory())
	// load initial session if specified
	if cmd := m.loadInitialSession(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.com.IsHyper() {
		cmds = append(cmds, m.fetchHyperCredits())
	}
	return tea.Batch(cmds...)
}

// loadInitialSession loads the initial session if one was specified on startup.
func (m *UI) loadInitialSession() tea.Cmd {
	switch {
	case m.state != uiLanding:
		// Only load if we're in landing state (i.e., fully configured)
		return nil
	case m.initialSessionID != "":
		return m.loadSession(m.initialSessionID)
	case m.continueLastSession:
		return func() tea.Msg {
			sessions, err := m.com.Workspace.ListSessions(context.Background())
			if err != nil || len(sessions) == 0 {
				return nil
			}
			return m.loadSession(sessions[0].ID)()
		}
	default:
		return func() tea.Msg {
			sessions, err := m.com.Workspace.ListSessions(context.Background())
			if err != nil || len(sessions) == 0 {
				return nil
			}
			// Touch the latest session to trigger updated_at trigger for mobile sync
			if _, err := m.com.Workspace.SaveSession(context.Background(), sessions[0]); err != nil {
				slog.Error("Failed to touch session updated_at on startup", "error", err)
			}
			return nil
		}
	}
}

// sendNotification returns a command that sends a notification if allowed by policy.
func (m *UI) sendNotification(n notification.Notification) tea.Cmd {
	if !m.shouldSendNotification() {
		return nil
	}

	backend := m.notifyBackend
	return func() tea.Msg {
		if err := backend.Send(n); err != nil {
			slog.Error("Failed to send notification", "error", err)
		}
		return nil
	}
}

// shouldSendNotification returns true if notifications should be sent based on
// current state. Focus reporting must be supported, window must not focused,
// and notifications must not be disabled in config.
func (m *UI) shouldSendNotification() bool {
	cfg := m.com.Config()
	if cfg != nil && cfg.Options != nil && cfg.Options.DisableNotifications {
		return false
	}
	return m.caps.ReportFocusEvents && !m.notifyWindowFocused
}

// setState changes the UI state and focus.
func (m *UI) setState(state uiState, focus uiFocusState) {
	if state == uiLanding {
		// Always turn off compact mode when going to landing
		m.isCompact = false
	}
	m.state = state
	m.focus = focus
	// Changing the state may change layout, so update it.
	m.updateLayoutAndSize()
}

// focusEditor moves keyboard focus back to the prompt editor.
func (m *UI) focusEditor() tea.Cmd {
	if m.focus == uiFocusEditor {
		if m.textarea.Focused() {
			return nil
		}
		m.textarea.Focus()
		return nil
	}

	m.focus = uiFocusEditor
	m.textarea.Focus()
	if m.chat != nil {
		m.chat.Blur()
		if cmd := m.chat.ForceScrollToBottomAndAnimate(); cmd != nil {
			return cmd
		}
	}
	return nil
}

// stopMouseAutoScroll cancels any pending drag-to-scroll tick.
func (m *UI) stopMouseAutoScroll() {
	m.mouseAutoScrollToken++
	m.mouseAutoScrollDirection = 0
	m.mouseAutoScrollPending = false
}

// scheduleMouseAutoScroll arms the next drag-to-scroll tick for the given
// direction. It keeps only one pending timer at a time.
func (m *UI) scheduleMouseAutoScroll(direction int) tea.Cmd {
	if direction == 0 || m.chat == nil || !m.chat.mouseDown {
		return nil
	}
	if m.mouseAutoScrollPending && m.mouseAutoScrollDirection == direction {
		return nil
	}
	if m.mouseAutoScrollDirection != direction {
		m.mouseAutoScrollToken++
	}
	m.mouseAutoScrollDirection = direction
	m.mouseAutoScrollPending = true
	token := m.mouseAutoScrollToken
	return tea.Tick(mouseAutoScrollInterval, func(time.Time) tea.Msg {
		return mouseAutoScrollMsg{
			Token:     token,
			Direction: direction,
		}
	})
}

// runMouseAutoScrollStep scrolls the chat one line in the given direction and
// schedules the next tick if the drag is still pinned to the edge.
func (m *UI) runMouseAutoScrollStep(direction int) []tea.Cmd {
	if m.chat == nil || !m.chat.mouseDown || direction == 0 {
		m.stopMouseAutoScroll()
		return nil
	}

	lines := mouseAutoScrollStep
	if direction < 0 {
		lines = -lines
	}

	beforeStartIdx, beforeLineOffset := m.chat.list.ScrollOffset()
	m.chat.ScrollBy(lines)
	afterStartIdx, afterLineOffset := m.chat.list.ScrollOffset()
	if beforeStartIdx == afterStartIdx && beforeLineOffset == afterLineOffset {
		m.stopMouseAutoScroll()
		return nil
	}

	var cmds []tea.Cmd
	if cmd := m.chat.RestartPausedVisibleAnimations(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	m.chat.HandleMouseDrag(m.chat.mouseDragViewportX, m.chat.mouseDragViewportY)
	if cmd := m.scheduleMouseAutoScroll(direction); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// loadCustomCommands loads the custom commands asynchronously.
func (m *UI) loadCustomCommands() tea.Cmd {
	return func() tea.Msg {
		customCommands, err := commands.LoadCustomCommands(m.com.Config())
		if err != nil {
			slog.Error("Failed to load custom commands", "error", err)
		}
		return userCommandsLoadedMsg{Commands: customCommands}
	}
}

// loadMCPrompts loads the MCP prompts asynchronously.
func (m *UI) loadMCPrompts() tea.Msg {
	prompts, err := commands.LoadMCPPrompts()
	if err != nil {
		slog.Error("Failed to load MCP prompts", "error", err)
	}
	if prompts == nil {
		// flag them as loaded even if there is none or an error
		prompts = []commands.MCPPrompt{}
	}
	return mcpPromptsLoadedMsg{Prompts: prompts}
}

// Update handles updates to the UI model.
type headerAnimTickMsg struct{}

func tickHeaderAnim() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return headerAnimTickMsg{}
	})
}

func (m *UI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.hasSession() && m.isAgentBusy() {
		queueSize := m.com.Workspace.AgentQueuedPrompts(m.session.ID)
		if queueSize != m.promptQueue {
			m.promptQueue = queueSize
			m.updateLayoutAndSize()
		}
	}
	// Update terminal capabilities
	m.caps.Update(msg)
	switch msg := msg.(type) {
	case headerAnimTickMsg:
		m.headerAnimFrame++
		m.headerAnimTicking = false
	case tea.EnvMsg:
		// Is this Windows Terminal?
		if !m.sendProgressBar {
			m.sendProgressBar = slices.Contains(msg, "WT_SESSION")
		}
		cmds = append(cmds, common.QueryCmd(uv.Environ(msg)))
	case tea.ModeReportMsg:
		if m.caps.ReportFocusEvents {
			m.notifyBackend = notification.NewNativeBackend(notification.Icon)
		}
	case tea.FocusMsg:
		m.notifyWindowFocused = true
	case tea.BlurMsg:
		m.notifyWindowFocused = false
	case pubsub.Event[notify.Notification]:
		if cmd := m.handleAgentNotification(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case loadSessionMsg:
		if m.forceCompactMode {
			m.isCompact = true
		}
		m.setState(uiChat, m.focus)
		m.session = msg.session
		m.sessionFiles = msg.files
		cmds = append(cmds, m.startLSPs(msg.lspFilePaths()))
		if err := m.com.Workspace.RepairSessionMessages(context.Background(), m.session.ID); err != nil {
			slog.Error("Failed to repair session messages", "error", err)
		}
		msgs, err := m.com.Workspace.ListMessages(context.Background(), m.session.ID)
		if err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		if cmd := m.setSessionMessages(msgs); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if hasInProgressTodo(m.session.Todos) {
			// only start spinner if there is an in-progress todo
			if m.isAgentBusy() {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
			}
			m.updateLayoutAndSize()
		}
		// Reload prompt history for the new session.
		m.historyReset()
		cmds = append(cmds, m.loadPromptHistory())
		m.updateLayoutAndSize()

	case sessionFilesUpdatesMsg:
		m.sessionFiles = msg.sessionFiles
		var paths []string
		for _, f := range msg.sessionFiles {
			paths = append(paths, f.LatestVersion.Path)
		}
		cmds = append(cmds, m.startLSPs(paths))

	case sendMessageMsg:
		// Keyboard submit is an explicit user action: force-snap to bottom
		// and re-engage follow mode regardless of where the user previously
		// scrolled. free-code/REPL pins this in a useEffect so a buffered
		// wheel event between submit-fire and commit can't yank it back —
		// in Bubble Tea the equivalent is to use the Force variant here so
		// the next stream tick can't race the regular ScrollToBottom.
		m.chat.ForceScrollToBottom()
		cmds = append(cmds, m.sendMessage(msg.Content, msg.Attachments...))

	case relay.RelayPromptMsg:
		m.chat.ForceScrollToBottom()
		cmds = append(cmds, m.sendMessage(msg.Text))

	case relay.RelayCancelMsg:
		if m.isAgentBusy() {
			if cmd := m.cancelAgent(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else if m.hasSession() && m.hasUnfinishedChatItem() {
			cmds = append(cmds, m.repairInterruptedSession())
		}

	case relay.RelayModelUpdateMsg:
		_ = m.com.Workspace.UpdateAgentModel(context.TODO())

	case userCommandsLoadedMsg:
		m.customCommands = msg.Commands
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetCustomCommands(m.customCommands)
		}

	case mcpStateChangedMsg:
		m.mcpStates = msg.states
	case mcpPromptsLoadedMsg:
		m.mcpPrompts = msg.Prompts
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetMCPPrompts(m.mcpPrompts)
		}

	case promptHistoryLoadedMsg:
		m.promptHistory.messages = msg.messages
		m.promptHistory.index = -1
		m.promptHistory.draft = ""

	case restoreCanceledPromptMsg:
		if m.session != nil && m.session.ID == msg.sessionID && strings.TrimSpace(m.textarea.Value()) == "" && msg.text != "" {
			prevHeight := m.textarea.Height()
			m.textarea.SetValue(msg.text)
			m.textarea.MoveToEnd()
			m.promptHistory.draft = msg.text
			m.promptHistory.index = -1
			cmds = append(cmds, m.updateTextareaWithPrevHeight(nil, prevHeight))
		}

	case closeDialogMsg:
		m.dialog.CloseFrontDialog()

	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.DeletedEvent {
			if m.session != nil && m.session.ID == msg.Payload.ID {
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			break
		}
		if m.session != nil && msg.Payload.ID == m.session.ID {
			prevHasInProgress := hasInProgressTodo(m.session.Todos)
			m.session = &msg.Payload
			m.renderPills()
			if !prevHasInProgress && hasInProgressTodo(m.session.Todos) {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
				m.updateLayoutAndSize()
				if m.state == uiChat {
					if m.chat.Follow() {
						if cmd := m.chat.ForceScrollToBottomAndAnimate(); cmd != nil {
							cmds = append(cmds, cmd)
						}
					} else if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}
			// Schedule a re-render to handle recently completed TTL
			cmds = append(cmds, tea.Tick(chat.TodoRecentlyCompletedTTL, func(time.Time) tea.Msg {
				return todoRecentlyCompletedMsg{}
			}))
		}
	case pubsub.Event[message.Message]:
		// Check if this is a child session message for an agent tool.
		if m.session == nil {
			break
		}
		if msg.Payload.SessionID != m.session.ID {
			// This might be a child session message from an agent tool.
			if cmd := m.handleChildSessionMessage(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			break
		}
		switch msg.Type {
		case pubsub.CreatedEvent:
			cmds = append(cmds, m.appendSessionMessage(msg.Payload))
		case pubsub.UpdatedEvent:
			cmds = append(cmds, m.updateSessionMessage(msg.Payload))
		case pubsub.DeletedEvent:
			m.chat.RemoveMessage(msg.Payload.ID)
		}
		// start the spinner if there is a new message
		if hasInProgressTodo(m.session.Todos) && m.isAgentBusy() && !m.todoIsSpinning {
			m.todoIsSpinning = true
			cmds = append(cmds, m.todoSpinner.Tick)
		}
		// stop the spinner if the agent is not busy anymore
		if m.todoIsSpinning && !m.isAgentBusy() {
			m.todoIsSpinning = false
		}
		// apply a model switch that was queued while the agent was busy
		if m.pendingModelSwitch != nil && !m.isAgentBusy() {
			pending := *m.pendingModelSwitch
			m.pendingModelSwitch = nil
			if cmd := m.handleSelectModel(pending); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		// there is a number of things that could change the pills here so we want to re-render
		m.renderPills()
	case pubsub.Event[history.File]:
		cmds = append(cmds, m.handleFileEvent(msg.Payload))
	case pubsub.Event[app.LSPEvent]:
		m.lspStates = app.GetLSPStates()
	case pubsub.Event[skills.Event]:
		m.skillStates = msg.Payload.States
	case pubsub.Event[mcp.Event]:
		switch msg.Payload.Type {
		case mcp.EventStateChanged:
			return m, tea.Batch(
				m.handleStateChanged(),
				m.loadMCPrompts,
			)
		case mcp.EventPromptsListChanged:
			return m, handleMCPPromptsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventToolsListChanged:
			return m, handleMCPToolsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventResourcesListChanged:
			return m, handleMCPResourcesEvent(m.com.Workspace, msg.Payload.Name)
		}
	case pubsub.Event[scheduler.Event]:
		if m.shouldHandleSchedulerEvent(msg.Payload) {
			m.recordTaskRuntimeEvent(msg.Payload)
			m.subAgents = recordSubAgentTaskEvent(m.subAgents, msg.Payload)
			if cmd := m.handleSchedulerEvent(msg.Payload); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	case pubsub.Event[agentruntime.TaskTrace]:
		if m.shouldHandleRuntimeTrace(msg.Payload) {
			m.recordLLMContextTrace(msg.Payload)
			m.recordToolRuntimeTrace(msg.Payload)
			if cmd := m.handleRuntimeActivityTrace(msg.Payload); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	case pubsub.Event[shell.BackgroundJobEvent]:
		// Background job and monitor output is runtime status, not
		// conversation content. Receiving the event is enough to trigger a
		// redraw; runtimeStatusLine reads the current workspace counters.
		if !m.shouldHandleBackgroundJobEvent(msg.Payload) {
			break
		}
	case pubsub.Event[permission.PermissionRequest]:
		if cmd := m.openPermissionsDialog(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.sendNotification(notification.Notification{
			Title:   "Crush is waiting...",
			Message: fmt.Sprintf("Permission required to execute \"%s\"", msg.Payload.ToolName),
		}); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[permission.PermissionNotification]:
		m.handlePermissionNotification(msg.Payload)
	case todoRecentlyCompletedMsg:
		m.renderPills()
	case ctrlCTimerExpiredMsg:
		m.ctrlCArmed = false
		m.ctrlCArmedAt = time.Time{}
	case tea.TerminalVersionMsg:
		termVersion := strings.ToLower(msg.Name)
		// Only enable progress bar for the following terminals.
		if !m.sendProgressBar {
			m.sendProgressBar = xstrings.ContainsAnyOf(termVersion, "ghostty", "iterm2", "rio")
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.updateLayoutAndSize()
		if m.state == uiChat {
			if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	case tea.KeyboardEnhancementsMsg:
		m.keyenh = msg
		if msg.SupportsKeyDisambiguation() {
			m.keyMap.Models.SetHelp("ctrl+m", "models")
			m.keyMap.Editor.Newline.SetHelp("shift+enter", "newline")
		}
	case copyChatHighlightMsg:
		cmds = append(cmds, m.copyChatHighlight())
	case DelayedClickMsg:
		// Handle delayed single-click action (e.g., expansion).
		m.chat.HandleDelayedClick(msg)
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseMiddle {
			return m, m.pasteImageFromClipboard
		}
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		m.stopMouseAutoScroll()
		if cmd := m.handleClickFocus(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

		switch m.state {
		case uiChat:
			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if !image.Pt(msg.X, msg.Y).In(m.layout.sidebar) {
				if handled, cmd := m.chat.HandleMouseDown(ansi.MouseButton(msg.Button), x, y); handled {
					m.lastClickTime = time.Now()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}
		}

	case tea.MouseMotionMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			height := m.chat.Height()

			if m.chat.mouseDown && height > 0 {
				switch {
				case y <= 0:
					m.chat.HandleMouseDrag(x, 0)
					if stepCmds := m.runMouseAutoScrollStep(-1); len(stepCmds) > 0 {
						cmds = append(cmds, stepCmds...)
					}
				case y >= height-1:
					m.chat.HandleMouseDrag(x, height-1)
					if stepCmds := m.runMouseAutoScrollStep(1); len(stepCmds) > 0 {
						cmds = append(cmds, stepCmds...)
					}
				default:
					m.stopMouseAutoScroll()
					m.chat.HandleMouseDrag(x, y)
				}
			} else {
				m.stopMouseAutoScroll()
				m.chat.HandleMouseDrag(x, y)
			}
		}

	case tea.MouseReleaseMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			m.stopMouseAutoScroll()
			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if m.chat.HandleMouseUp(ansi.MouseButton(msg.Button), x, y) && m.chat.HasHighlight() {
				cmds = append(cmds, tea.Tick(doubleClickThreshold, func(t time.Time) tea.Msg {
					if time.Since(m.lastClickTime) >= doubleClickThreshold {
						return copyChatHighlightMsg{}
					}
					return nil
				}))
			}
		}
	case mouseAutoScrollMsg:
		if m.state != uiChat || m.chat == nil {
			m.stopMouseAutoScroll()
			break
		}
		if msg.Token != m.mouseAutoScrollToken || !m.chat.mouseDown || m.mouseAutoScrollDirection != msg.Direction {
			m.stopMouseAutoScroll()
			break
		}
		m.mouseAutoScrollPending = false
		if cmds := m.runMouseAutoScrollStep(msg.Direction); len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
	case tea.MouseWheelMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		// Otherwise handle mouse wheel for chat.
		switch m.state {
		case uiChat:
			switch msg.Button {
			case tea.MouseWheelUp:
				if cmd := m.chat.ScrollByAndAnimate(-MouseScrollThreshold); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.chat.SelectFirstInView()
			case tea.MouseWheelDown:
				if cmd := m.chat.ScrollByAndAnimate(MouseScrollThreshold); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if m.chat.AtBottom() {
					// Re-engage follow mode: ScrollToSelected below can flip it off.
					if cmd := m.chat.ForceScrollToBottomAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
					m.chat.SelectLast()
				} else {
					m.chat.SelectLastInView()
				}
			}
		}
	case chat.StepMsg:
		if m.state == uiChat {
			if cmd := m.chat.Animate(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			// Streaming ticks are passive output. They must honor follow
			// mode so mouse-wheel up can keep the viewport unlocked.
			if m.chat.Follow() {
				if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	case spinner.TickMsg:
		if m.dialog.HasDialogs() {
			// route to dialog
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.state == uiChat && m.hasSession() && hasInProgressTodo(m.session.Todos) && m.todoIsSpinning {
			var cmd tea.Cmd
			m.todoSpinner, cmd = m.todoSpinner.Update(msg)
			if cmd != nil {
				m.renderPills()
				cmds = append(cmds, cmd)
			}
		}

	case tea.KeyPressMsg:
		if cmd := m.handleKeyPressMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tea.PasteMsg:
		if cmd := m.handlePasteMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case openEditorMsg:
		prevHeight := m.textarea.Height()
		m.textarea.SetValue(msg.Text)
		m.textarea.MoveToEnd()
		cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
	case hyperRefreshDoneMsg:
		if cmd := m.handleSelectModel(msg.action); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case creditsUpdatedMsg:
		m.hyperCredits = &msg.credits
	case util.InfoMsg:
		if msg.Type == util.InfoTypeError {
			slog.Error("Error reported", "error", msg.Msg)
		}
		msgID := m.status.SetInfoMsg(msg)
		cmds = append(cmds, clearInfoMsgCmd(msgID, statusMessageTTL(msg)))
	case util.ClearStatusMsg:
		m.status.ClearInfoMsg(msg.ID)
	case completions.CompletionItemsLoadedMsg:
		if m.completionsOpen {
			m.completions.SetItems(msg.Files, msg.Resources)
		}
	case uv.KittyGraphicsEvent:
		if !bytes.HasPrefix(msg.Payload, []byte("OK")) {
			slog.Warn("Unexpected Kitty graphics response",
				"response", string(msg.Payload),
				"options", msg.Options)
		}
	default:
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	// Textarea placeholder logic
	if m.isAgentBusy() {
		m.textarea.Placeholder = m.activeEditorBusyPlaceholder()
	} else {
		m.textarea.Placeholder = m.readyPlaceholder
	}

	// at this point this can only handle [message.Attachment] message, and we
	// should return all cmds anyway.
	_ = m.attachments.Update(msg)

	if m.isAgentBusy() && !m.headerAnimTicking {
		m.headerAnimTicking = true
		cmds = append(cmds, tickHeaderAnim())
	}

	return m, tea.Batch(cmds...)
}

// setSessionMessages sets the messages for the current session in the chat
func (m *UI) setSessionMessages(msgs []message.Message) tea.Cmd {
	var cmds []tea.Cmd
	// Brain tool result map to link tool calls with their results
	msgPtrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		msgPtrs[i] = &msgs[i]
	}
	toolResultMap := chat.BuildToolResultMap(msgPtrs)
	if len(msgPtrs) > 0 {
		m.lastUserMessageTime = msgPtrs[0].CreatedAt
	}

	// Add messages to chat with linked tool results
	items := make([]chat.MessageItem, 0, len(msgs)*2)
	for _, msg := range msgPtrs {
		switch msg.Role {
		case message.User:
			m.lastUserMessageTime = msg.CreatedAt
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
		case message.Assistant:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
			if msg.FinishPart() != nil && msg.FinishPart().Reason == message.FinishReasonEndTurn {
				infoItem := chat.NewAssistantInfoItem(m.com.Styles, msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
				items = append(items, infoItem)
			}
			if msg.FinishReason() == message.FinishReasonCanceled {
				items = append(items, chat.NewInterruptDividerItem(m.com.Styles, msg.ID))
			}
		default:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
		}
	}

	// Load nested tool calls for agent/agentic_fetch tools.
	m.loadNestedToolCalls(items)

	// If the user switches between sessions while the agent is working we want
	// to make sure the animations are shown.
	for _, item := range items {
		if animatable, ok := item.(chat.Animatable); ok {
			if cmd := animatable.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	type shellIDer interface {
		ShellID() string
	}

	filteredItems := make([]chat.MessageItem, 0, len(items))
	lastJobOutputIndex := make(map[string]int)

	for _, item := range items {
		if sIDer, ok := item.(shellIDer); ok {
			shellID := sIDer.ShellID()
			if shellID != "" {
				if prevIdx, exists := lastJobOutputIndex[shellID]; exists {
					filteredItems[prevIdx] = item
					continue
				} else {
					lastJobOutputIndex[shellID] = len(filteredItems)
					filteredItems = append(filteredItems, item)
				}
			} else {
				filteredItems = append(filteredItems, item)
			}
		} else {
			filteredItems = append(filteredItems, item)
		}
	}
	items = filteredItems

	m.chat.SetMessages(items...)
	if cmd := m.chat.ForceScrollToBottomAndAnimate(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.chat.SelectLast()
	return tea.Sequence(cmds...)
}

// loadNestedToolCalls recursively loads nested tool calls for agent/agentic_fetch tools.
func (m *UI) loadNestedToolCalls(items []chat.MessageItem) {
	for _, item := range items {
		nestedContainer, ok := item.(chat.NestedToolContainer)
		if !ok {
			continue
		}
		toolItem, ok := item.(chat.ToolMessageItem)
		if !ok {
			continue
		}

		tc := toolItem.ToolCall()
		messageID := toolItem.MessageID()

		// Get the agent tool session ID.
		agentSessionID := m.com.Workspace.CreateAgentToolSessionID(messageID, tc.ID)

		// Fetch nested messages.
		nestedMsgs, err := m.com.Workspace.ListMessages(context.Background(), agentSessionID)
		if err != nil || len(nestedMsgs) == 0 {
			continue
		}

		// Brain tool result map for nested messages.
		nestedMsgPtrs := make([]*message.Message, len(nestedMsgs))
		for i := range nestedMsgs {
			nestedMsgPtrs[i] = &nestedMsgs[i]
		}
		nestedToolResultMap := chat.BuildToolResultMap(nestedMsgPtrs)

		// Extract nested tool items.
		var nestedTools []chat.ToolMessageItem
		for _, nestedMsg := range nestedMsgPtrs {
			nestedItems := chat.ExtractMessageItems(m.com.Styles, nestedMsg, nestedToolResultMap)
			for _, nestedItem := range nestedItems {
				if nestedToolItem, ok := nestedItem.(chat.ToolMessageItem); ok {
					// Mark nested tools as simple (compact) rendering.
					if simplifiable, ok := nestedToolItem.(chat.Compactable); ok {
						simplifiable.SetCompact(true)
					}
					nestedTools = append(nestedTools, nestedToolItem)
				}
			}
		}

		// Recursively load nested tool calls for any agent tools within.
		nestedMessageItems := make([]chat.MessageItem, len(nestedTools))
		for i, nt := range nestedTools {
			nestedMessageItems[i] = nt
		}
		m.loadNestedToolCalls(nestedMessageItems)

		// Set nested tools on the parent.
		nestedContainer.SetNestedTools(nestedTools)
	}
}

// appendSessionMessage appends a new message to the current session in the chat
// if the message is a tool result it will update the corresponding tool call message
func (m *UI) appendSessionMessage(msg message.Message) tea.Cmd {
	var cmds []tea.Cmd

	existing := m.chat.MessageItem(msg.ID)
	if existing != nil {
		// message already exists, skip
		return nil
	}

	switch msg.Role {
	case message.User:
		m.lastUserMessageTime = msg.CreatedAt
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil)
		for _, item := range items {
			if animatable, ok := item.(chat.Animatable); ok {
				if cmd := animatable.StartAnimation(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		m.chat.AppendMessages(items...)
		if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case message.Assistant:
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil)
		for _, item := range items {
			if animatable, ok := item.(chat.Animatable); ok {
				if cmd := animatable.StartAnimation(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		m.chat.AppendMessages(items...)
		if m.chat.Follow() {
			cmds = append(cmds, m.chat.ForceScrollToBottomAndAnimate())
		} else if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if msg.FinishPart() != nil && msg.FinishPart().Reason == message.FinishReasonEndTurn {
			infoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			m.chat.AppendMessages(infoItem)
			if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			// Plan-mode closed loop: when the plan agent finishes its turn,
			// auto-pre-fill `/accept` in the composer so the user only has
			// to hit Enter to execute, and surface a success-styled toast
			// with the alternatives. Avoids forcing modal dialogs while
			// keeping the UX one keystroke away from go.
			if m.currentSessionMode().IsPlan() && m.textarea.Value() == "" {
				m.textarea.SetValue("/accept")
				m.textarea.MoveToEnd()
				cmds = append(cmds, util.CmdHandler(util.InfoMsg{
					Type: util.InfoTypeSuccess,
					Msg:  `Plan ready · press Enter to run /accept · edit textarea for /cancel-plan instead`,
					TTL:  15 * time.Second,
				}))
			}
		}
	case message.Tool:
		for _, tr := range msg.ToolResults() {
			toolItem := m.chat.MessageItem(tr.ToolCallID)
			if toolItem == nil {
				// we should have an item!
				continue
			}
			if toolMsgItem, ok := toolItem.(chat.ToolMessageItem); ok {
				toolMsgItem.SetResult(&tr)
				if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	}
	return tea.Sequence(cmds...)
}

func (m *UI) handleClickFocus(msg tea.MouseClickMsg) (cmd tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	switch {
	case m.state != uiChat:
		return nil
	case image.Pt(msg.X, msg.Y).In(m.layout.sidebar):
		return nil
	case m.focus != uiFocusEditor && image.Pt(msg.X, msg.Y).In(m.layout.editor):
		m.focus = uiFocusEditor
		cmd = m.textarea.Focus()
		m.chat.Blur()
	case m.focus != uiFocusMain && image.Pt(msg.X, msg.Y).In(m.layout.main):
		m.focus = uiFocusMain
		m.textarea.Blur()
		m.chat.Focus()
	}
	return cmd
}

// updateSessionMessage updates an existing message in the current session in
// the chat when an assistant message is updated it may include updated tool
// calls as well that is why we need to handle creating/updating each tool call
// message too.
func (m *UI) updateSessionMessage(msg message.Message) tea.Cmd {
	wasAtBottom := m.chat.AtBottom()
	var cmds []tea.Cmd
	existingItem := m.chat.MessageItem(msg.ID)

	if existingItem != nil {
		if assistantItem, ok := existingItem.(*chat.AssistantMessageItem); ok {
			assistantItem.SetMessage(&msg)
		}
	}

	shouldRenderAssistant := chat.ShouldRenderAssistantMessage(&msg)
	isEndTurn := msg.FinishPart() != nil && msg.FinishPart().Reason == message.FinishReasonEndTurn
	// If the message of the assistant does not have any response just tool
	// calls we need to remove it, but keep the info item for end-of-turn
	// renders so the footer (model/provider/duration) remains visible when,
	// for example, a hook halts the turn.
	if !shouldRenderAssistant && len(msg.ToolCalls()) > 0 && existingItem != nil {
		m.chat.RemoveMessage(msg.ID)
		if !isEndTurn {
			if infoItem := m.chat.MessageItem(chat.AssistantInfoID(msg.ID)); infoItem != nil {
				m.chat.RemoveMessage(chat.AssistantInfoID(msg.ID))
			}
		}
	}

	if isEndTurn {
		if infoItem := m.chat.MessageItem(chat.AssistantInfoID(msg.ID)); infoItem == nil {
			newInfoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			m.chat.AppendMessages(newInfoItem)
		}
	}

	var items []chat.MessageItem
	for _, tc := range msg.ToolCalls() {
		existingToolItem := m.chat.MessageItem(tc.ID)
		if toolItem, ok := existingToolItem.(chat.ToolMessageItem); ok {
			existingToolCall := toolItem.ToolCall()
			// only update if finished state changed or input changed
			// to avoid clearing the cache
			if (tc.Finished && !existingToolCall.Finished) || tc.Input != existingToolCall.Input {
				toolItem.SetToolCall(tc)
			}
		}
		if existingToolItem == nil {
			items = append(items, chat.NewToolMessageItem(m.com.Styles, msg.ID, tc, nil, false))
		}
	}

	for _, item := range items {
		if animatable, ok := item.(chat.Animatable); ok {
			if cmd := animatable.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	m.chat.AppendMessages(items...)
	if m.chat.Follow() || wasAtBottom {
		cmds = append(cmds, m.chat.ForceScrollToBottomAndAnimate())
	} else if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.chat.SelectLast()

	return tea.Sequence(cmds...)
}

// handleChildSessionMessage handles messages from child sessions (agent tools).
func (m *UI) handleChildSessionMessage(event pubsub.Event[message.Message]) tea.Cmd {
	var cmds []tea.Cmd

	// Only process messages with tool calls or results.
	if len(event.Payload.ToolCalls()) == 0 && len(event.Payload.ToolResults()) == 0 {
		return nil
	}

	// Check if this is an agent tool session and parse it.
	childSessionID := event.Payload.SessionID
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(childSessionID)
	if !ok {
		return nil
	}

	// Find the parent agent tool item.
	var agentItem chat.NestedToolContainer
	for i := 0; i < m.chat.Len(); i++ {
		item := m.chat.MessageItem(toolCallID)
		if item == nil {
			continue
		}
		if agent, ok := item.(chat.NestedToolContainer); ok {
			if toolMessageItem, ok := item.(chat.ToolMessageItem); ok {
				if toolMessageItem.ToolCall().ID == toolCallID {
					// Verify this agent belongs to the correct parent message.
					// We can't directly check parentMessageID on the item, so we trust the session parsing.
					agentItem = agent
					break
				}
			}
		}
	}

	if agentItem == nil {
		return nil
	}

	// Get existing nested tools.
	nestedTools := agentItem.NestedTools()

	// Update or create nested tool calls.
	for _, tc := range event.Payload.ToolCalls() {
		found := false
		for _, existingTool := range nestedTools {
			if existingTool.ToolCall().ID == tc.ID {
				existingTool.SetToolCall(tc)
				found = true
				break
			}
		}
		if !found {
			// Create a new nested tool item.
			nestedItem := chat.NewToolMessageItem(m.com.Styles, event.Payload.ID, tc, nil, false)
			if simplifiable, ok := nestedItem.(chat.Compactable); ok {
				simplifiable.SetCompact(true)
			}
			if animatable, ok := nestedItem.(chat.Animatable); ok {
				if cmd := animatable.StartAnimation(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			nestedTools = append(nestedTools, nestedItem)
		}
	}

	// Update nested tool results.
	for _, tr := range event.Payload.ToolResults() {
		for _, nestedTool := range nestedTools {
			if nestedTool.ToolCall().ID == tr.ToolCallID {
				nestedTool.SetResult(&tr)
				break
			}
		}
	}

	// Update the agent item with the new nested tools.
	agentItem.SetNestedTools(nestedTools)

	// Update the chat so it updates the index map for animations to work as expected
	m.chat.UpdateNestedToolIDs(toolCallID)

	if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.chat.SelectLast()

	return tea.Sequence(cmds...)
}

func (m *UI) handleDialogMsg(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	action := m.dialog.Update(msg)
	if action == nil {
		return tea.Batch(cmds...)
	}

	isOnboarding := m.state == uiOnboarding

	switch msg := action.(type) {
	// Generic dialog messages
	case dialog.ActionClose:
		if isOnboarding && m.dialog.ContainsDialog(dialog.ModelsID) {
			break
		}

		if m.dialog.ContainsDialog(dialog.FilePickerID) {
			defer fimage.ResetCache()
		}

		m.dialog.CloseFrontDialog()

		if isOnboarding {
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		if m.focus == uiFocusEditor {
			cmds = append(cmds, m.textarea.Focus())
		}
	case dialog.ActionCmd:
		if msg.Cmd != nil {
			cmds = append(cmds, msg.Cmd)
		}

	// Session dialog messages.
	case dialog.ActionSelectSession:
		m.dialog.CloseDialog(dialog.SessionsID)
		cmds = append(cmds, m.loadSession(msg.Session.ID))

	// Open dialog message.
	case dialog.ActionOpenDialog:
		m.dialog.CloseDialog(dialog.CommandsID)
		if cmd := m.openDialog(msg.DialogID); cmd != nil {
			cmds = append(cmds, cmd)
		}

	// Command dialog messages.
	case dialog.ActionToggleNotifications:
		cfg := m.com.Config()
		if cfg != nil && cfg.Options != nil {
			disabled := !cfg.Options.DisableNotifications
			cfg.Options.DisableNotifications = disabled
			if err := m.com.Workspace.SetConfigField("options.disable_notifications", disabled); err != nil {
				cmds = append(cmds, util.ReportError(err))
			} else {
				status := "enabled"
				if disabled {
					status = "disabled"
				}
				cmds = append(cmds, util.CmdHandler(util.NewInfoMsg("Notifications "+status)))
			}
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionNewSession:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
			break
		}
		if cmd := m.newSession(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionSummarize:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, func() tea.Msg {
			err := m.com.Workspace.AgentSummarize(context.Background(), msg.SessionID)
			if err != nil {
				return util.ReportError(err)()
			}
			return nil
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleHelp:
		m.status.ToggleHelp()
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionExternalEditor:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
			break
		}
		cmds = append(cmds, m.openEditor(m.textarea.Value()))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleCompactMode:
		cmds = append(cmds, m.toggleCompactMode())
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionTogglePills:
		if cmd := m.togglePillsExpanded(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleThinking:
		cmds = append(cmds, func() tea.Msg {
			cfg := m.com.Config()
			if cfg == nil {
				return util.ReportError(errors.New("configuration not found"))()
			}

			agentCfg, ok := cfg.Agents[config.AgentBrain]
			if !ok {
				return util.ReportError(errors.New("agent configuration not found"))()
			}

			currentModel := cfg.Models[agentCfg.Model]
			currentModel.Think = !currentModel.Think
			if err := m.com.Workspace.UpdatePreferredModel(agentCfg.Model, currentModel); err != nil {
				return util.ReportError(err)()
			}
			m.com.Workspace.UpdateAgentModel(context.TODO())
			status := "disabled"
			if currentModel.Think {
				status = "enabled"
			}
			return util.NewInfoMsg("Thinking mode " + status)
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleTransparentBackground:
		cmds = append(cmds, func() tea.Msg {
			cfg := m.com.Config()
			if cfg == nil {
				return util.ReportError(errors.New("configuration not found"))()
			}

			isTransparent := cfg.Options != nil && cfg.Options.TUI.Transparent != nil && *cfg.Options.TUI.Transparent
			newValue := !isTransparent
			if err := m.com.Workspace.SetConfigField("options.tui.transparent", newValue); err != nil {
				return util.ReportError(err)()
			}
			m.isTransparent = newValue

			status := "disabled"
			if newValue {
				status = "enabled"
			}
			return util.NewInfoMsg("Transparent background " + status)
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionQuit:
		cmds = append(cmds, tea.Quit)
	case dialog.ActionEnableDockerMCP:
		m.dialog.CloseDialog(dialog.CommandsID)
		cmds = append(cmds, m.enableDockerMCP)
	case dialog.ActionDisableDockerMCP:
		m.dialog.CloseDialog(dialog.CommandsID)
		cmds = append(cmds, m.disableDockerMCP)
	case dialog.ActionInitializeProject:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, m.initializeProject())
		m.dialog.CloseDialog(dialog.CommandsID)

	case dialog.ActionSelectModel:
		if cmd := m.handleSelectModel(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ActionSelectReasoningEffort:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait..."))
			break
		}

		cfg := m.com.Config()
		if cfg == nil {
			cmds = append(cmds, util.ReportError(errors.New("configuration not found")))
			break
		}

		agentCfg, ok := cfg.Agents[config.AgentBrain]
		if !ok {
			cmds = append(cmds, util.ReportError(errors.New("agent configuration not found")))
			break
		}

		currentModel := cfg.Models[agentCfg.Model]
		currentModel.ReasoningEffort = msg.Effort
		if err := m.com.Workspace.UpdatePreferredModel(agentCfg.Model, currentModel); err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}

		cmds = append(cmds, func() tea.Msg {
			m.com.Workspace.UpdateAgentModel(context.TODO())
			return util.NewInfoMsg("Reasoning effort set to " + msg.Effort)
		})
		m.dialog.CloseDialog(dialog.ReasoningID)
	case dialog.ActionPermissionResponse:
		m.dialog.CloseDialog(dialog.PermissionsID)
		switch msg.Action {
		case dialog.PermissionAllow:
			m.com.Workspace.PermissionGrant(msg.Permission)
		case dialog.PermissionAllowForSession:
			m.com.Workspace.PermissionGrantPersistent(msg.Permission)
		case dialog.PermissionDeny:
			m.com.Workspace.PermissionDeny(msg.Permission)
		}

	case dialog.ActionFilePickerSelected:
		cmds = append(cmds, tea.Sequence(
			msg.Cmd(),
			func() tea.Msg {
				m.dialog.CloseDialog(dialog.FilePickerID)
				return nil
			},
			func() tea.Msg {
				fimage.ResetCache()
				return nil
			},
		))

	case dialog.ActionRunCustomCommand:
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			argsDialog := dialog.NewArguments(
				m.com,
				"Custom Command Arguments",
				"",
				msg.Arguments,
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		content := msg.Content
		if msg.Args != nil {
			content = substituteArgs(content, msg.Args)
		}
		cmds = append(cmds, m.sendMessage(content))
		m.dialog.CloseFrontDialog()
	case dialog.ActionRunMCPPrompt:
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			title := cmp.Or(msg.Title, "MCP Prompt Arguments")
			argsDialog := dialog.NewArguments(
				m.com,
				title,
				msg.Description,
				msg.Arguments,
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		cmds = append(cmds, m.runMCPPrompt(msg.ClientID, msg.PromptID, msg.Args))
	default:
		cmds = append(cmds, util.CmdHandler(msg))
	}

	return tea.Batch(cmds...)
}

// substituteArgs replaces $ARG_NAME placeholders in content with actual values.
func substituteArgs(content string, args map[string]string) string {
	for name, value := range args {
		placeholder := "$" + name
		content = strings.ReplaceAll(content, placeholder, value)
	}
	return content
}

// refreshHyperAndRetrySelect returns a command that silently refreshes
// the Hyper OAuth token and then re-runs the model selection. If the
// refresh fails, the selection resumes with ReAuthenticate set so the
// OAuth dialog opens.
func (m *UI) refreshHyperAndRetrySelect(msg dialog.ActionSelectModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.com.Workspace.RefreshOAuthToken(ctx, "hyper"); err != nil {
			slog.Warn("Hyper OAuth refresh failed, requesting re-auth", "error", err)
			msg.ReAuthenticate = true
		}
		return hyperRefreshDoneMsg{action: msg}
	}
}

// fetchHyperCredits returns a command that asynchronously fetches the
// remaining Hyper credits from the API.
func (m *UI) fetchHyperCredits() tea.Cmd {
	return func() tea.Msg {
		cfg := m.com.Config()
		if cfg == nil {
			return nil
		}
		providerCfg, ok := cfg.Providers.Get(hyper.Name)
		if !ok {
			return nil
		}
		apiKey, err := m.com.Workspace.Resolver().ResolveValue(providerCfg.APIKey)
		if err != nil || apiKey == "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		credits, err := hyper.FetchCredits(ctx, apiKey)
		if err != nil {
			slog.Error("Failed to fetch Hyper credits", "error", err)
			return nil
		}
		return creditsUpdatedMsg{credits: credits}
	}
}

// handleSelectModel performs the model selection after any provider
// pre-checks (such as a silent Hyper OAuth refresh) have completed.
func (m *UI) handleSelectModel(msg dialog.ActionSelectModel) tea.Cmd {
	var cmds []tea.Cmd

	// we ignore dialogs with the oauth id as they need to be able to be dismissed
	if m.isAgentBusy() && !m.dialog.ContainsDialog(dialog.OAuthID) {
		// Queue the switch instead of dropping it; it is applied automatically
		// once the agent goes idle (see the message-event idle transition).
		pending := msg
		m.pendingModelSwitch = &pending
		m.dialog.CloseDialog(dialog.ModelsID)
		slog.Debug("Model switch queued while agent busy",
			"model_type", msg.ModelType, "provider", msg.Model.Provider, "model", msg.Model.Model)
		return util.ReportInfo(fmt.Sprintf("Agent busy — %s will switch to %s when idle", msg.ModelType, msg.Model.Model))
	}

	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	var (
		providerID   = msg.Model.Provider
		isCopilot    = providerID == string(catwalk.InferenceProviderCopilot)
		isConfigured = func() bool { _, ok := cfg.Providers.Get(providerID); return ok }
		isOnboarding = m.state == uiOnboarding
	)

	// For Hyper, if the stored OAuth token is expired, try a silent
	// refresh before deciding whether the provider is configured. Keeps
	// users from hitting a 401 on their first message after the
	// short-lived access token ages out.
	if !msg.ReAuthenticate && providerID == "hyper" {
		if pc, ok := cfg.Providers.Get(providerID); ok && pc.OAuthToken != nil && pc.OAuthToken.IsExpired() {
			return m.refreshHyperAndRetrySelect(msg)
		}
	}

	// Attempt to import GitHub Copilot tokens from VSCode if available.
	if isCopilot && !isConfigured() && !msg.ReAuthenticate {
		m.com.Workspace.ImportCopilot()
	}

	if !isConfigured() || msg.ReAuthenticate {
		m.dialog.CloseDialog(dialog.ModelsID)
		if cmd := m.openAuthenticationDialog(msg.Provider, msg.Model, msg.ModelType); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return tea.Batch(cmds...)
	}

	if err := m.com.Workspace.UpdatePreferredModel(msg.ModelType, msg.Model); err != nil {
		cmds = append(cmds, util.ReportError(err))
	} else {
		if msg.ModelType == config.SelectedModelTypeBrain {
			// Swap the theme live based on the newly selected brain
			// model's provider.
			m.applyTheme(styles.ThemeForProvider(providerID))
		}
		if _, ok := cfg.Models[config.SelectedModelTypeExplore]; !ok {
			// Ensure the explore model is set if it is unset.
			exploreModel := m.com.Workspace.GetDefaultExploreModel(providerID)
			if err := m.com.Workspace.UpdatePreferredModel(config.SelectedModelTypeExplore, exploreModel); err != nil {
				cmds = append(cmds, util.ReportError(err))
			}
		}
	}

	cmds = append(cmds, func() tea.Msg {
		if err := m.com.Workspace.UpdateAgentModel(context.TODO()); err != nil {
			return util.ReportError(err)
		}

		slog.Debug("Model switch applied",
			"model_type", msg.ModelType, "provider", msg.Model.Provider, "model", msg.Model.Model)
		modelMsg := fmt.Sprintf("%s model changed to %s", msg.ModelType, msg.Model.Model)

		return util.NewInfoMsg(modelMsg)
	})

	m.dialog.CloseDialog(dialog.APIKeyInputID)
	m.dialog.CloseDialog(dialog.OAuthID)
	m.dialog.CloseDialog(dialog.ModelsID)

	if isOnboarding {
		m.setState(uiLanding, uiFocusEditor)
		m.com.Config().SetupAgents()
		if err := m.com.Workspace.InitBrainAgent(context.TODO()); err != nil {
			cmds = append(cmds, util.ReportError(err))
		}
	} else if m.com.IsHyper() {
		cmds = append(cmds, m.fetchHyperCredits())
	}

	return tea.Batch(cmds...)
}

func (m *UI) openAuthenticationDialog(provider catwalk.Provider, model config.SelectedModel, modelType config.SelectedModelType) tea.Cmd {
	var (
		dlg dialog.Dialog
		cmd tea.Cmd

		isOnboarding = m.state == uiOnboarding
	)

	switch provider.ID {
	case "hyper":
		dlg, cmd = dialog.NewOAuthHyper(m.com, isOnboarding, provider, model, modelType)
	case catwalk.InferenceProviderCopilot:
		dlg, cmd = dialog.NewOAuthCopilot(m.com, isOnboarding, provider, model, modelType)
	default:
		dlg, cmd = dialog.NewAPIKeyInput(m.com, isOnboarding, provider, model, modelType)
	}

	if m.dialog.ContainsDialog(dlg.ID()) {
		m.dialog.BringToFront(dlg.ID())
		return nil
	}

	m.dialog.OpenDialog(dlg)
	return cmd
}

func (m *UI) handleKeyPressMsg(msg tea.KeyPressMsg) tea.Cmd {
	var cmds []tea.Cmd

	handleGlobalKeys := func(msg tea.KeyPressMsg) bool {
		switch {
		case key.Matches(msg, m.keyMap.Help):
			m.status.ToggleHelp()
			m.updateLayoutAndSize()
			return true
		case key.Matches(msg, m.keyMap.Commands):
			if cmd := m.openCommandsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Models):
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.PlanMode):
			if cmd := m.toggleSessionMode(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Sessions):
			if cmd := m.openSessionsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Chat.Details):
			if m.state == uiChat && m.hasSession() {
				m.detailsOpen = !m.detailsOpen
				m.updateLayoutAndSize()
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.TogglePills):
			if m.state == uiChat && m.hasSession() {
				if cmd := m.togglePillsExpanded(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.PillLeft):
			if m.state == uiChat && m.hasSession() && m.pillsExpanded && m.focus != uiFocusEditor {
				if cmd := m.switchPillSection(-1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.PillRight):
			if m.state == uiChat && m.hasSession() && m.pillsExpanded && m.focus != uiFocusEditor {
				if cmd := m.switchPillSection(1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Suspend):
			if m.isAgentBusy() {
				cmds = append(cmds, util.ReportWarn("Agent is busy, please wait..."))
				return true
			}
			cmds = append(cmds, tea.Suspend)
			return true
		}
		return false
	}

	// Route all messages to dialog if one is open.
	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	if key.Matches(msg, m.keyMap.Quit) {
		if m.ctrlCArmed && !m.ctrlCArmedAt.IsZero() && time.Since(m.ctrlCArmedAt) <= ctrlCTimerDuration {
			m.ctrlCArmed = false
			m.ctrlCArmedAt = time.Time{}
			return tea.Quit
		}

		if cmd := m.clearComposer(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.ctrlCArmed = true
		m.ctrlCArmedAt = time.Now()
		cmds = append(cmds, util.ReportWarn("Press ctrl+c again to quit"), ctrlCTimerCmd())
		return tea.Batch(cmds...)
	}

	// Handle cancel key when agent is busy.
	isEscapeKey := msg.String() == "esc" || msg.String() == "alt+esc"
	if isEscapeKey || key.Matches(msg, m.keyMap.Chat.Cancel) {
		if m.isAgentBusy() {
			if cmd := m.cancelAgent(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return tea.Batch(cmds...)
		}
		if isEscapeKey && m.hasSession() && m.hasUnfinishedChatItem() {
			cmds = append(cmds, m.repairInterruptedSession())
			return tea.Batch(cmds...)
		}
		if (m.state == uiChat || m.state == uiLanding) &&
			(m.focus == uiFocusMain || m.mouseAutoScrollPending || (m.chat != nil && m.chat.mouseDown)) {
			m.stopMouseAutoScroll()
			if m.chat != nil {
				m.chat.ClearMouse()
			}
			if m.focus == uiFocusMain {
				if cmd := m.focusEditor(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			return tea.Batch(cmds...)
		}
	}

	// Any unmodified keyboard input should snap back to the editor so the
	// prompt buffer always stays the primary interaction surface.
	if (m.state == uiChat || m.state == uiLanding) && m.focus == uiFocusMain && msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		if cmd := m.focusEditor(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if key.Matches(msg, m.keyMap.Tab) {
			return tea.Batch(cmds...)
		}
	}

	switch m.state {
	case uiOnboarding:
		return tea.Batch(cmds...)
	case uiInitialize:
		cmds = append(cmds, m.updateInitializeView(msg)...)
		return tea.Batch(cmds...)
	case uiChat, uiLanding:
		// Focus drift recovery: a mouse click can move focus to the chat
		// (uiFocusMain). The moment the user starts TYPING (printable
		// rune without Ctrl/Alt), snap focus back to the editor, blur
		// the chat, and pull the chat to the bottom so the new input
		// is visible. Navigation keys (arrows, PageUp, etc.) and
		// modified combos still work in chat-focus mode as before.
		if m.focus == uiFocusMain && msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
			m.focus = uiFocusEditor
			cmds = append(cmds, m.textarea.Focus())
			m.chat.Blur()
			if cmd := m.chat.ForceScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		switch m.focus {
		case uiFocusEditor:
			prevHeight := m.textarea.Height()

			// Handle completions if open.
			if m.completionsOpen {
				if msg, ok := m.completions.Update(msg); ok {
					switch msg := msg.(type) {
					case completions.SelectionMsg[completions.FileCompletionValue]:
						cmds = append(cmds, m.insertFileCompletion(msg.Value.Path))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.SelectionMsg[completions.ResourceCompletionValue]:
						cmds = append(cmds, m.insertMCPResourceCompletion(msg.Value))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.ClosedMsg:
						m.completionsOpen = false
					}
					return tea.Batch(cmds...)
				}
			}

			if ok := m.attachments.Update(msg); ok {
				return tea.Batch(cmds...)
			}

			switch {
			case msg.String() == "backspace" && m.textarea.Value() == "" && len(m.attachments.List()) > 0:
				// Backspace on empty input removes the most-recent attachment.
				list := m.attachments.List()
				m.attachments.SetList(list[:len(list)-1])

			case key.Matches(msg, m.keyMap.Editor.AddImage):
				if !m.currentModelSupportsImages() {
					break
				}
				if cmd := m.openFilesDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}

			case key.Matches(msg, m.keyMap.Editor.PasteImage):
				if !m.currentModelSupportsImages() {
					break
				}
				cmds = append(cmds, m.pasteImageFromClipboard)

			case key.Matches(msg, m.keyMap.Editor.SendMessage):
				prevHeight := m.textarea.Height()
				value := m.textarea.Value()
				if before, ok := strings.CutSuffix(value, "\\"); ok {
					// If the last character is a backslash, remove it and add a newline.
					m.textarea.SetValue(before)
					if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
						cmds = append(cmds, cmd)
					}
					break
				}

				// Otherwise, send the message
				m.textarea.Reset()
				if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
					cmds = append(cmds, cmd)
				}

				value = strings.TrimSpace(value)
				if value == "exit" || value == "quit" {
					return m.openQuitDialog()
				}

				// Text slash command: /clear opens a new session, matching
				// Claude Code muscle memory. The keystroke-driven path
				// (ctrl+n / command palette) still works.
				if value == "/clear" {
					if !m.hasSession() {
						return nil
					}
					if m.isAgentBusy() {
						return util.ReportWarn("Agent is busy, please wait before starting a new session...")
					}
					return m.newSession()
				}

				// Plan-mode closed loop: `/accept` (or `/run`) commits the
				// most-recent plan — flip the session back to execute mode
				// and tell brain to implement what it just designed in the
				// same session, so the plan text stays in context and we
				// don't have to copy-paste it. `/cancel-plan` is the
				// chicken-out path: leave plan mode without acting.
				if value == "/accept" || value == "/run" {
					if !m.hasSession() {
						return nil
					}
					if !m.currentSessionMode().IsPlan() {
						return util.ReportWarn("Not in plan mode")
					}
					if m.isAgentBusy() {
						return util.ReportWarn("Agent is busy, please wait...")
					}
					m.setCurrentSessionMode(session.ModeExecute)
					innerCmds := []tea.Cmd{
						util.CmdHandler(util.InfoMsg{
							Type: util.InfoTypeInfo,
							Msg:  "Plan accepted · mode → execute · implementing now",
							TTL:  4 * time.Second,
						}),
						m.sendMessage("Implement the plan you produced above. Use worker sub-agents for any non-trivial edits and keep me in the loop with concise progress."),
					}
					if saveCmd := m.saveCurrentSessionMode(); saveCmd != nil {
						innerCmds = append(innerCmds, saveCmd)
					}
					m.historyReset()
					return tea.Batch(innerCmds...)
				}
				if value == "/cancel-plan" || value == "/exit-plan" {
					if !m.currentSessionMode().IsPlan() {
						return util.ReportWarn("Not in plan mode")
					}
					m.setCurrentSessionMode(session.ModeExecute)
					innerCmds := []tea.Cmd{
						util.CmdHandler(util.InfoMsg{
							Type: util.InfoTypeInfo,
							Msg:  "Plan mode disabled",
							TTL:  3 * time.Second,
						}),
					}
					if saveCmd := m.saveCurrentSessionMode(); saveCmd != nil {
						innerCmds = append(innerCmds, saveCmd)
					}
					return tea.Batch(innerCmds...)
				}

				attachments := m.attachments.List()
				m.attachments.Reset()
				if len(value) == 0 && !message.ContainsTextAttachment(attachments) {
					return nil
				}

				m.randomizePlaceholders()
				m.historyReset()

				return tea.Batch(m.sendMessage(value, attachments...), m.loadPromptHistory())
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Tab):
				if m.state != uiLanding {
					m.setState(m.state, uiFocusMain)
					m.textarea.Blur()
					m.chat.Focus()
					m.chat.SetSelected(m.chat.Len() - 1)
				}
			case key.Matches(msg, m.keyMap.Editor.OpenEditor):
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
					break
				}
				cmds = append(cmds, m.openEditor(m.textarea.Value()))
			case key.Matches(msg, m.keyMap.Editor.Newline):
				m.textarea.InsertRune('\n')
				m.closeCompletions()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
			case key.Matches(msg, m.keyMap.Editor.HistoryPrev):
				cmd := m.handleHistoryUp(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.HistoryNext):
				cmd := m.handleHistoryDown(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.WordLeft):
				// Translate ctrl+left to alt+b for word backward
				msg = tea.KeyPressMsg(tea.Key{Code: 'b', Text: "b", Mod: tea.ModAlt})
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, m.textarea.Height()))
			case key.Matches(msg, m.keyMap.Editor.WordRight):
				// Translate ctrl+right to alt+f for word forward
				msg = tea.KeyPressMsg(tea.Key{Code: 'f', Text: "f", Mod: tea.ModAlt})
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, m.textarea.Height()))
			case key.Matches(msg, m.keyMap.Editor.Escape):
				cmd := m.handleHistoryEscape(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Commands) && m.textarea.Value() == "":
				if cmd := m.openCommandsDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Tab):
				// Keep keyboard focus on the editor; Tab should not move the user
				// into the chat list.
				if cmd := m.focusEditor(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return tea.Batch(cmds...)
			default:
				if handleGlobalKeys(msg) {
					// Handle global keys first before passing to textarea.
					break
				}

				// Check for @ trigger before passing to textarea.
				curValue := m.textarea.Value()
				curIdx := len(curValue)

				// Trigger completions on @.
				if msg.String() == "@" && !m.completionsOpen {
					// Only show if beginning of prompt or after whitespace.
					if curIdx == 0 || (curIdx > 0 && isWhitespace(curValue[curIdx-1])) {
						m.completionsOpen = true
						m.completionsQuery = ""
						m.completionsStartIndex = curIdx
						m.completionsPositionStart = m.completionsPosition()
						depth, limit := m.com.Config().Options.TUI.Completions.Limits()
						cmds = append(cmds, m.completions.Open(depth, limit))
					}
				}

				// remove the details if they are open when user starts typing
				if m.detailsOpen {
					m.detailsOpen = false
					m.updateLayoutAndSize()
				}

				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))

				// Any text modification becomes the current draft.
				m.updateHistoryDraft(curValue)

				// After updating textarea, check if we need to filter completions.
				// Skip filtering on the initial @ keystroke since items are loading async.
				if m.completionsOpen && msg.String() != "@" {
					newValue := m.textarea.Value()
					newIdx := len(newValue)

					// Close completions if cursor moved before start.
					if newIdx <= m.completionsStartIndex {
						m.closeCompletions()
					} else if msg.String() == "space" {
						// Close on space.
						m.closeCompletions()
					} else {
						// Extract current word and filter.
						word := m.textareaWord()
						if strings.HasPrefix(word, "@") {
							m.completionsQuery = word[1:]
							m.completions.Filter(m.completionsQuery)
						} else if m.completionsOpen {
							m.closeCompletions()
						}
					}
				}
			}
		case uiFocusMain:
			switch {
			case key.Matches(msg, m.keyMap.Tab):
				if cmd := m.focusEditor(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				m.focus = uiFocusEditor
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.Expand):
				m.chat.ToggleExpandedSelectedItem()
			case key.Matches(msg, m.keyMap.Chat.Up):
				if cmd := m.chat.ScrollByAndAnimate(-1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.chat.SelectedItemInView() {
					m.chat.SelectPrev()
					if cmd := m.chat.ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case key.Matches(msg, m.keyMap.Chat.Down):
				if cmd := m.chat.ScrollByAndAnimate(1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.chat.SelectedItemInView() {
					m.chat.SelectNext()
					if cmd := m.chat.ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case key.Matches(msg, m.keyMap.Chat.UpOneItem):
				m.chat.SelectPrev()
				if cmd := m.chat.ScrollToSelectedAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.DownOneItem):
				m.chat.SelectNext()
				if cmd := m.chat.ScrollToSelectedAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.HalfPageUp):
				if cmd := m.chat.ScrollByAndAnimate(-m.chat.Height() / 2); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.chat.SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.HalfPageDown):
				if cmd := m.chat.ScrollByAndAnimate(m.chat.Height() / 2); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.chat.SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.PageUp):
				if cmd := m.chat.ScrollByAndAnimate(-m.chat.Height()); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.chat.SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.PageDown):
				if cmd := m.chat.ScrollByAndAnimate(m.chat.Height()); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.chat.SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.Home):
				if cmd := m.chat.ScrollToTopAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.chat.SelectFirst()
			case key.Matches(msg, m.keyMap.Chat.End):
				if cmd := m.chat.ForceScrollToBottomAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.chat.SelectLast()
			default:
				if ok, cmd := m.chat.HandleKeyMsg(msg); ok {
					cmds = append(cmds, cmd)
				} else {
					handleGlobalKeys(msg)
				}
			}
		default:
			handleGlobalKeys(msg)
		}
	default:
		handleGlobalKeys(msg)
	}

	return tea.Sequence(cmds...)
}

// drawHeader draws the header section of the UI.
func (m *UI) drawHeader(scr uv.Screen, area uv.Rectangle) {
	m.header.drawHeader(
		scr,
		area,
		m.session,
		m.currentSessionMode(),
		m.isCompact,
		m.detailsOpen,
		area.Dx(),
		m.hyperCredits,
		m.isAgentBusy(),
		m.activeRunStatusText(),
		m.headerAnimFrame,
	)
}

// Draw implements [uv.Drawable] and draws the UI model.
func (m *UI) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	isOnboarding := m.state == uiOnboarding
	m.status.SetHideHelp(isOnboarding)
	m.status.SetStatusLine(m.runtimeStatusLine())

	layout := m.generateLayout(area.Dx(), area.Dy())

	if m.layout != layout {
		m.layout = layout
		m.updateSize()
	}

	// Clear the screen first
	screen.Clear(scr)

	switch m.state {
	case uiOnboarding:
		m.drawHeader(scr, layout.header)

		// NOTE: Onboarding flow will be rendered as dialogs below, but
		// positioned at the bottom left of the screen.

	case uiInitialize:
		m.drawHeader(scr, layout.header)

		main := uv.NewStyledString(m.initializeView())
		main.Draw(scr, layout.main)

	case uiLanding:
		m.drawHeader(scr, layout.header)
		main := uv.NewStyledString(m.landingView())
		main.Draw(scr, layout.main)

		editor := uv.NewStyledString(m.renderEditorView(scr.Bounds().Dx()))
		editor.Draw(scr, layout.editor)

	case uiChat:
		if m.isCompact {
			m.drawHeader(scr, layout.header)
		} else if m.detailsOpen {
			m.drawDagActivity(scr, layout.sidebar)
		} else {
			m.drawSidebar(scr, layout.sidebar)
		}

		m.chat.Draw(scr, layout.main)
		if layout.pills.Dy() > 0 && m.pillsView != "" {
			uv.NewStyledString(m.pillsView).Draw(scr, layout.pills)
		}

		editorWidth := scr.Bounds().Dx()
		if !m.isCompact {
			editorWidth -= layout.sidebar.Dx()
		}
		editor := uv.NewStyledString(m.renderEditorView(editorWidth))
		editor.Draw(scr, layout.editor)

		// Draw activity overlay in compact mode when open.
		if m.isCompact && m.detailsOpen {
			m.drawDagActivity(scr, layout.sessionDetails)
		}
	}

	// Add status and help layer
	m.status.Draw(scr, layout.status)

	// Draw completions popup if open
	if !isOnboarding && m.completionsOpen && m.completions.HasItems() {
		w, h := m.completions.Size()
		x := m.completionsPositionStart.X
		y := m.completionsPositionStart.Y - h

		screenW := area.Dx()
		if x+w > screenW {
			x = screenW - w
		}
		x = max(0, x)
		y = max(0, y+1) // Offset for attachments row

		completionsView := uv.NewStyledString(m.completions.Render())
		completionsView.Draw(scr, image.Rectangle{
			Min: image.Pt(x, y),
			Max: image.Pt(x+w, y+h),
		})
	}

	// Debugging rendering (visually see when the tui rerenders)
	if os.Getenv("CRUSH_UI_DEBUG") == "true" {
		debugView := lipgloss.NewStyle().Background(lipgloss.ANSIColor(rand.Intn(256))).Width(4).Height(2)
		debug := uv.NewStyledString(debugView.String())
		debug.Draw(scr, image.Rectangle{
			Min: image.Pt(4, 1),
			Max: image.Pt(8, 3),
		})
	}

	// This needs to come last to overlay on top of everything. We always pass
	// the full screen bounds because the dialogs will position themselves
	// accordingly.
	if m.dialog.HasDialogs() {
		return m.dialog.Draw(scr, scr.Bounds())
	}

	if m.shouldAnchorTerminalCursorToEditor() {
		return m.terminalEditorCursor()
	}
	return nil
}

func (m *UI) shouldAnchorTerminalCursorToEditor() bool {
	if m.state != uiLanding && m.state != uiChat {
		return false
	}
	if m.layout.editor.Dy() <= 0 {
		return false
	}
	if m.detailsOpen && m.isCompact {
		return false
	}
	return true
}

func (m *UI) terminalEditorCursor() *tea.Cursor {
	if m.textarea.Focused() {
		return m.offsetEditorCursor(m.textarea.Cursor())
	}

	// IME candidate windows follow the terminal cursor, not the rendered
	// prompt glyph. Keep the real cursor anchored to the editor even when
	// keyboard focus is on chat/main so CJK preedit does not jump to the
	// renderer's last write position.
	textArea := m.textarea
	textArea.Focus()
	return m.offsetEditorCursor(textArea.Cursor())
}

func (m *UI) offsetEditorCursor(cur *tea.Cursor) *tea.Cursor {
	if cur == nil {
		return nil
	}
	cur.X++ // Adjust for app margins.
	if len(m.attachments.List()) > 0 {
		cur.Y += m.layout.editor.Min.Y + 1 // Offset for attachments row.
	} else {
		cur.Y += m.layout.editor.Min.Y
	}
	return cur
}

// View renders the UI model's view.
func (m *UI) View() tea.View {
	var v tea.View
	v.AltScreen = true
	if !m.isTransparent {
		v.BackgroundColor = m.com.Styles.Background
	}
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = m.caps.ReportFocusEvents
	v.WindowTitle = "crush " + home.Short(m.com.Workspace.WorkingDir())

	canvas := uv.NewScreenBuffer(m.width, m.height)
	v.Cursor = m.Draw(canvas, canvas.Bounds())

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n") // normalize newlines
	contentLines := strings.Split(content, "\n")
	for i, line := range contentLines {
		// Trim trailing spaces for concise rendering
		contentLines[i] = strings.TrimRight(line, " ")
	}

	content = strings.Join(contentLines, "\n")

	v.Content = content
	if m.progressBarEnabled && m.sendProgressBar && m.isAgentBusy() {
		// HACK: use a random percentage to prevent ghostty from hiding it
		// after a timeout.
		v.ProgressBar = tea.NewProgressBar(tea.ProgressBarIndeterminate, rand.Intn(100))
	}

	return v
}

// ShortHelp implements [help.KeyMap].
func (m *UI) ShortHelp() []key.Binding {
	var binds []key.Binding
	k := &m.keyMap
	tab := k.Tab
	commands := k.Commands
	if m.focus == uiFocusEditor && m.textarea.Value() == "" {
		commands.SetHelp("/ or ctrl+p", "commands")
	}

	switch m.state {
	case uiInitialize:
		binds = append(binds, k.Quit)
	case uiChat:
		// Show cancel binding if agent is busy.
		if m.isAgentBusy() {
			cancelBinding := k.Chat.Cancel
			if m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 {
				cancelBinding.SetHelp("esc", "clear queue")
			}
			binds = append(binds, cancelBinding)
		}

		if m.focus == uiFocusEditor {
			tab.SetHelp("tab", "focus chat")
		} else {
			tab.SetHelp("tab", "focus editor")
		}

		binds = append(
			binds,
			tab,
			commands,
			k.Models,
			k.PlanMode,
		)

		switch m.focus {
		case uiFocusEditor:
			binds = append(
				binds,
				k.Editor.Newline,
			)
		case uiFocusMain:
			binds = append(
				binds,
				k.Chat.UpDown,
				k.Chat.UpDownOneItem,
				k.Chat.PageUp,
				k.Chat.PageDown,
				k.Chat.Copy,
			)
			if m.pillsExpanded && hasIncompleteTodos(m.session.Todos) && m.promptQueue > 0 {
				binds = append(binds, k.Chat.PillLeft)
			}
		}
	default:
		// TODO: other states
		// if m.session == nil {
		// no session selected
		binds = append(
			binds,
			commands,
			k.Models,
			k.PlanMode,
			k.Editor.Newline,
		)
	}

	binds = append(
		binds,
		k.Quit,
		k.Help,
	)

	return binds
}

// FullHelp implements [help.KeyMap].
func (m *UI) FullHelp() [][]key.Binding {
	var binds [][]key.Binding
	k := &m.keyMap
	help := k.Help
	help.SetHelp("ctrl+g", "less")
	hasAttachments := len(m.attachments.List()) > 0
	hasSession := m.hasSession()
	commands := k.Commands
	if m.focus == uiFocusEditor && m.textarea.Value() == "" {
		commands.SetHelp("/ or ctrl+p", "commands")
	}

	switch m.state {
	case uiInitialize:
		binds = append(binds,
			[]key.Binding{
				k.Quit,
			})
	case uiChat:
		// Show cancel binding if agent is busy.
		if m.isAgentBusy() {
			cancelBinding := k.Chat.Cancel
			if m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 {
				cancelBinding.SetHelp("esc", "clear queue")
			}
			binds = append(binds, []key.Binding{cancelBinding})
		}

		mainBinds := []key.Binding{}
		tab := k.Tab
		if m.focus == uiFocusEditor {
			tab.SetHelp("tab", "focus chat")
		} else {
			tab.SetHelp("tab", "focus editor")
		}

		mainBinds = append(
			mainBinds,
			tab,
			commands,
			k.Models,
			k.PlanMode,
			k.Sessions,
		)
		if hasSession {
			mainBinds = append(mainBinds, k.Chat.NewSession)
		}

		binds = append(binds, mainBinds)

		switch m.focus {
		case uiFocusEditor:
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage, k.Editor.PasteImage)
			}
			binds = append(binds, editorBinds)
			if hasAttachments {
				binds = append(
					binds,
					[]key.Binding{
						k.Editor.AttachmentDeleteMode,
						k.Editor.DeleteAllAttachments,
						k.Editor.Escape,
					},
				)
			}
		case uiFocusMain:
			binds = append(
				binds,
				[]key.Binding{
					k.Chat.UpDown,
					k.Chat.UpDownOneItem,
					k.Chat.PageUp,
					k.Chat.PageDown,
				},
				[]key.Binding{
					k.Chat.HalfPageUp,
					k.Chat.HalfPageDown,
					k.Chat.Home,
					k.Chat.End,
				},
				[]key.Binding{
					k.Chat.Copy,
					k.Chat.ClearHighlight,
				},
			)
			if m.pillsExpanded && hasIncompleteTodos(m.session.Todos) && m.promptQueue > 0 {
				binds = append(binds, []key.Binding{k.Chat.PillLeft})
			}
		}
	default:
		if m.session == nil {
			// no session selected
			binds = append(
				binds,
				[]key.Binding{
					commands,
					k.Models,
					k.PlanMode,
					k.Sessions,
				},
			)
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage, k.Editor.PasteImage)
			}
			binds = append(binds, editorBinds)
			if hasAttachments {
				binds = append(
					binds,
					[]key.Binding{
						k.Editor.AttachmentDeleteMode,
						k.Editor.DeleteAllAttachments,
						k.Editor.Escape,
					},
				)
			}
		}
	}

	binds = append(
		binds,
		[]key.Binding{
			help,
			k.Quit,
		},
	)

	return binds
}

func (m *UI) currentModelSupportsImages() bool {
	cfg := m.com.Config()
	if cfg == nil {
		return false
	}
	agentCfg, ok := cfg.Agents[config.AgentBrain]
	if !ok {
		return false
	}
	model := cfg.GetModelByType(agentCfg.Model)
	return model != nil && model.SupportsImages
}

// toggleCompactMode toggles compact mode between uiChat and uiChatCompact states.
func (m *UI) toggleCompactMode() tea.Cmd {
	m.forceCompactMode = !m.forceCompactMode

	err := m.com.Workspace.SetCompactMode(m.forceCompactMode)
	if err != nil {
		return util.ReportError(err)
	}

	m.updateLayoutAndSize()

	return nil
}

// updateLayoutAndSize updates the layout and sizes of UI components.
func (m *UI) updateLayoutAndSize() {
	// Determine if we should be in compact mode
	if m.state == uiChat {
		if m.forceCompactMode {
			m.isCompact = true
		} else if m.width < compactModeWidthBreakpoint || m.height < compactModeHeightBreakpoint {
			m.isCompact = true
		} else {
			m.isCompact = false
		}
	}

	// First pass sizes components from the current textarea height.
	m.layout = m.generateLayout(m.width, m.height)
	prevHeight := m.textarea.Height()
	m.updateSize()

	// SetWidth can change textarea height due to soft-wrap recalculation.
	// If that happens, run one reconciliation pass with the new height.
	if m.textarea.Height() != prevHeight {
		m.layout = m.generateLayout(m.width, m.height)
		m.updateSize()
	}
}

// handleTextareaHeightChange checks whether the textarea height changed and,
// if so, recalculates the layout. When the chat is in follow mode it keeps
// the view scrolled to the bottom. The returned command, if non-nil, must be
// batched by the caller.
func (m *UI) handleTextareaHeightChange(prevHeight int) tea.Cmd {
	if m.state == uiChat {
		if m.textarea.Height() != prevHeight {
			m.updateLayoutAndSize()
		}
		// User requested that any input change pulls TUI to bottom and locks it.
		// ForceScrollToBottomAndAnimate both scrolls and sets follow=true.
		return m.chat.ForceScrollToBottomAndAnimate()
	}

	if m.textarea.Height() == prevHeight {
		return nil
	}
	m.updateLayoutAndSize()
	if m.state == uiChat {
		return m.chat.ScrollToBottomAndAnimate()
	}
	return nil
}

// updateTextarea updates the textarea for msg and then reconciles layout if
// the textarea height changed as a result.
func (m *UI) updateTextarea(msg tea.Msg) tea.Cmd {
	return m.updateTextareaWithPrevHeight(msg, m.textarea.Height())
}

// updateTextareaWithPrevHeight is for cases when the height of the layout may
// have changed.
//
// Particularly, it's for cases where the textarea changes before
// textarea.Update is called (for example, SetValue, Reset, and InsertRune). We
// pass the height from before those changes took place so we can compare
// "before" vs "after" sizing and recalculate the layout if the textarea grew
// or shrank.
func (m *UI) updateTextareaWithPrevHeight(msg tea.Msg, prevHeight int) tea.Cmd {
	ta, cmd := m.textarea.Update(msg)
	m.textarea = ta
	return tea.Batch(cmd, m.handleTextareaHeightChange(prevHeight))
}

// updateSize updates the sizes of UI components based on the current layout.
func (m *UI) updateSize() {
	// Set status width
	m.status.SetWidth(m.layout.status.Dx())

	m.chat.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
	m.textarea.MaxHeight = TextareaMaxHeight
	m.textarea.SetWidth(m.layout.editor.Dx())
	m.renderPills()

	// Handle different app states
	switch m.state {
	case uiChat:
		if !m.isCompact {
			m.cacheSidebarLogo(m.layout.sidebar.Dx())
		}
	}
}

// generateLayout calculates the layout rectangles for all UI components based
// on the current UI state and terminal dimensions.
func (m *UI) generateLayout(w, h int) uiLayout {
	// The screen area we're working with
	area := image.Rect(0, 0, w, h)

	helpHeight := 0
	if m.status != nil && m.status.HasContent() {
		helpHeight = 1
	}
	// The editor height: textarea height + margin for attachments and bottom spacing.
	editorHeight := m.textarea.Height() + editorHeightMargin
	// The sidebar width
	sidebarWidth := 30
	if m.state == uiChat && !m.isCompact && m.detailsOpen {
		sidebarWidth = min(56, max(36, area.Dx()/3))
		if area.Dx()-sidebarWidth < 64 {
			sidebarWidth = max(30, area.Dx()-64)
		}
	}
	// The header height
	const landingHeaderHeight = 4

	var helpKeyMap help.KeyMap = m
	if helpHeight > 0 && m.status != nil && m.status.ShowingAll() {
		for _, row := range helpKeyMap.FullHelp() {
			helpHeight = max(helpHeight, len(row))
		}
	}

	// Add app margins
	var appRect, helpRect image.Rectangle
	if helpHeight > 0 {
		layout.Vertical(
			layout.Len(area.Dy()-helpHeight),
			layout.Fill(1),
		).Split(area).Assign(&appRect, &helpRect)
	} else {
		appRect = area
		helpRect = image.Rect(0, area.Max.Y, area.Dx(), area.Max.Y)
	}
	appRect.Min.Y += 1
	appRect.Max.Y -= 1
	helpRect.Min.Y -= 1
	appRect.Min.X += 1
	appRect.Max.X -= 1

	if slices.Contains([]uiState{uiOnboarding, uiInitialize, uiLanding}, m.state) {
		// extra padding on left and right for these states
		appRect.Min.X += 1
		appRect.Max.X -= 1
	}

	uiLayout := uiLayout{
		area:   area,
		status: helpRect,
	}

	// Handle different app states
	switch m.state {
	case uiOnboarding, uiInitialize:
		// Layout
		//
		// header
		// ------
		// main
		// ------
		// help

		var headerRect, mainRect image.Rectangle
		layout.Vertical(
			layout.Len(landingHeaderHeight),
			layout.Fill(1),
		).Split(appRect).Assign(&headerRect, &mainRect)
		uiLayout.header = headerRect
		uiLayout.main = mainRect

	case uiLanding:
		// Layout
		//
		// header
		// ------
		// main
		// ------
		// editor
		// ------
		// help
		var headerRect, mainRect image.Rectangle
		layout.Vertical(
			layout.Len(landingHeaderHeight),
			layout.Fill(1),
		).Split(appRect).Assign(&headerRect, &mainRect)
		var editorRect image.Rectangle
		layout.Vertical(
			layout.Len(mainRect.Dy()-editorHeight),
			layout.Fill(1),
		).Split(mainRect).Assign(&mainRect, &editorRect)
		// Remove extra padding from editor (but keep it for header and main)
		editorRect.Min.X -= 1
		editorRect.Max.X += 1
		uiLayout.header = headerRect
		uiLayout.main = mainRect
		uiLayout.editor = editorRect

	case uiChat:
		if m.isCompact {
			// Layout
			//
			// compact-header
			// ------
			// main
			// ------
			// editor
			// ------
			// help
			const compactHeaderHeight = 1
			var headerRect, mainRect image.Rectangle
			layout.Vertical(
				layout.Len(compactHeaderHeight),
				layout.Fill(1),
			).Split(appRect).Assign(&headerRect, &mainRect)
			detailsHeight := min(sessionDetailsMaxHeight, area.Dy()-1) // One row for the header
			var sessionDetailsArea image.Rectangle
			layout.Vertical(
				layout.Len(detailsHeight),
				layout.Fill(1),
			).Split(appRect).Assign(&sessionDetailsArea, new(image.Rectangle))
			uiLayout.sessionDetails = sessionDetailsArea
			uiLayout.sessionDetails.Min.Y += compactHeaderHeight // adjust for header
			// Add one line gap between header and main content
			mainRect.Min.Y += 1
			var editorRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-editorHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&mainRect, &editorRect)
			mainRect.Max.X -= 1 // Add padding right
			uiLayout.header = headerRect
			pillsHeight := m.pillsAreaHeight()
			if pillsHeight > 0 {
				pillsHeight = min(pillsHeight, mainRect.Dy())
				var chatRect, pillsRect image.Rectangle
				layout.Vertical(
					layout.Len(mainRect.Dy()-pillsHeight),
					layout.Fill(1),
				).Split(mainRect).Assign(&chatRect, &pillsRect)
				uiLayout.main = chatRect
				uiLayout.pills = pillsRect
			} else {
				uiLayout.main = mainRect
			}
			// Add bottom margin to main
			uiLayout.main.Max.Y -= 1
			uiLayout.editor = editorRect
		} else {
			// Layout
			//
			// ------|---
			// main  |
			// ------| side
			// editor|
			// ----------
			// help

			var mainRect, sideRect image.Rectangle
			layout.Horizontal(
				layout.Len(appRect.Dx()-sidebarWidth),
				layout.Fill(1),
			).Split(appRect).Assign(&mainRect, &sideRect)
			// Add padding left
			sideRect.Min.X += 1
			var editorRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-editorHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&mainRect, &editorRect)
			mainRect.Max.X -= 1 // Add padding right
			uiLayout.sidebar = sideRect
			pillsHeight := m.pillsAreaHeight()
			if pillsHeight > 0 {
				pillsHeight = min(pillsHeight, mainRect.Dy())
				var chatRect, pillsRect image.Rectangle
				layout.Vertical(
					layout.Len(mainRect.Dy()-pillsHeight),
					layout.Fill(1),
				).Split(mainRect).Assign(&chatRect, &pillsRect)
				uiLayout.main = chatRect
				uiLayout.pills = pillsRect
			} else {
				uiLayout.main = mainRect
			}
			// Add bottom margin to main
			uiLayout.main.Max.Y -= 1
			uiLayout.editor = editorRect
		}
	}

	return uiLayout
}

// uiLayout defines the positioning of UI elements.
type uiLayout struct {
	// area is the overall available area.
	area uv.Rectangle

	// header is the header shown in special cases
	// e.x when the sidebar is collapsed
	// or when in the landing page
	// or in init/config
	header uv.Rectangle

	// main is the area for the main pane. (e.x chat, configure, landing)
	main uv.Rectangle

	// pills is the area for the pills panel.
	pills uv.Rectangle

	// editor is the area for the editor pane.
	editor uv.Rectangle

	// sidebar is the area for the sidebar.
	sidebar uv.Rectangle

	// status is the area for the status view.
	status uv.Rectangle

	// session details is the area for the session details overlay in compact mode.
	sessionDetails uv.Rectangle
}

func (m *UI) openEditor(value string) tea.Cmd {
	tmpfile, err := os.CreateTemp("", "msg_*.md")
	if err != nil {
		return util.ReportError(err)
	}
	tmpPath := tmpfile.Name()
	defer tmpfile.Close() //nolint:errcheck
	if _, err := tmpfile.WriteString(value); err != nil {
		return util.ReportError(err)
	}
	cmd, err := editor.Command(
		"crush",
		tmpPath,
		editor.AtPosition(
			m.textarea.Line()+1,
			m.textarea.Column()+1,
		),
	)
	if err != nil {
		return util.ReportError(err)
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer func() {
			_ = os.Remove(tmpPath)
		}()

		if err != nil {
			return util.ReportError(err)
		}
		content, err := os.ReadFile(tmpPath)
		if err != nil {
			return util.ReportError(err)
		}
		if len(content) == 0 {
			return util.ReportWarn("Message is empty")
		}
		return openEditorMsg{
			Text: strings.TrimSpace(string(content)),
		}
	})
}

// setEditorPrompt configures the textarea prompt function.
func (m *UI) setEditorPrompt() {
	m.textarea.SetPromptFunc(4, m.normalPromptFunc)
}

// normalPromptFunc returns the editor prompt style ("  > " on first line,
// "::: " on subsequent lines).
func (m *UI) normalPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return "  > "
		}
		return "::: "
	}
	if info.Focused {
		return t.Editor.PromptNormalFocused.Render()
	}
	return t.Editor.PromptNormalBlurred.Render()
}

// closeCompletions closes the completions popup and resets state.
func (m *UI) closeCompletions() {
	m.completionsOpen = false
	m.completionsQuery = ""
	m.completionsStartIndex = 0
	m.completions.Close()
}

// insertCompletionText replaces the @query in the textarea with the given text.
// Returns false if the replacement cannot be performed.
func (m *UI) insertCompletionText(text string) bool {
	value := m.textarea.Value()
	if m.completionsStartIndex > len(value) {
		return false
	}

	word := m.textareaWord()
	endIdx := min(m.completionsStartIndex+len(word), len(value))
	newValue := value[:m.completionsStartIndex] + text + value[endIdx:]
	m.textarea.SetValue(newValue)
	m.textarea.MoveToEnd()
	m.textarea.InsertRune(' ')
	return true
}

// insertFileCompletion inserts the selected file path into the textarea,
// replacing the @query, and adds the file as an attachment.
func (m *UI) insertFileCompletion(path string) tea.Cmd {
	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(path) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	fileCmd := func() tea.Msg {
		absPath, _ := filepath.Abs(path)

		if m.hasSession() {
			// Skip attachment if file was already read and hasn't been modified.
			lastRead := m.com.Workspace.FileTrackerLastReadTime(context.Background(), m.session.ID, absPath)
			if !lastRead.IsZero() {
				if info, err := os.Stat(path); err == nil && !info.ModTime().After(lastRead) {
					return nil
				}
			}
		} else if slices.Contains(m.sessionFileReads, absPath) {
			return nil
		}

		m.sessionFileReads = append(m.sessionFileReads, absPath)

		// Add file as attachment.
		content, err := os.ReadFile(path)
		if err != nil {
			// If it fails, let the LLM handle it later.
			return nil
		}

		return message.Attachment{
			FilePath: path,
			FileName: filepath.Base(path),
			MimeType: mimeOf(content),
			Content:  content,
		}
	}
	return tea.Batch(heightCmd, fileCmd)
}

// insertMCPResourceCompletion inserts the selected resource into the textarea,
// replacing the @query, and adds the resource as an attachment.
func (m *UI) insertMCPResourceCompletion(item completions.ResourceCompletionValue) tea.Cmd {
	displayText := cmp.Or(item.Title, item.URI)

	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(displayText) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	resourceCmd := func() tea.Msg {
		contents, err := m.com.Workspace.ReadMCPResource(
			context.Background(),
			item.MCPName,
			item.URI,
		)
		if err != nil {
			slog.Warn("Failed to read MCP resource", "uri", item.URI, "error", err)
			return nil
		}
		if len(contents) == 0 {
			return nil
		}

		content := contents[0]
		var data []byte
		if content.Text != "" {
			data = []byte(content.Text)
		} else if len(content.Blob) > 0 {
			data = content.Blob
		}
		if len(data) == 0 {
			return nil
		}

		mimeType := item.MIMEType
		if mimeType == "" && content.MIMEType != "" {
			mimeType = content.MIMEType
		}
		if mimeType == "" {
			mimeType = "text/plain"
		}

		return message.Attachment{
			FilePath: item.URI,
			FileName: displayText,
			MimeType: mimeType,
			Content:  data,
		}
	}
	return tea.Batch(heightCmd, resourceCmd)
}

// completionsPosition returns the X and Y position for the completions popup.
func (m *UI) completionsPosition() image.Point {
	cur := m.textarea.Cursor()
	if cur == nil {
		return image.Point{
			X: m.layout.editor.Min.X,
			Y: m.layout.editor.Min.Y,
		}
	}
	return image.Point{
		X: cur.X + m.layout.editor.Min.X,
		Y: m.layout.editor.Min.Y + cur.Y,
	}
}

// textareaWord returns the current word at the cursor position.
func (m *UI) textareaWord() string {
	return m.textarea.Word()
}

// isWhitespace returns true if the byte is a whitespace character.
func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// isAgentBusy returns true if the agent coordinator exists and is currently
// busy processing a request.
func (m *UI) isAgentBusy() bool {
	return m.com.Workspace.AgentIsReady() &&
		m.com.Workspace.AgentIsBusy()
}

// hasSession returns true if there is an active session with a valid ID.
func (m *UI) hasSession() bool {
	return m.session != nil && m.session.ID != ""
}

// mimeOf detects the MIME type of the given content.
func mimeOf(content []byte) string {
	mimeBufferSize := min(512, len(content))
	return http.DetectContentType(content[:mimeBufferSize])
}

func supportedImageMimeOf(content []byte) (string, bool) {
	if len(content) == 0 {
		return "", false
	}
	mimeType := mimeOf(content)
	if i := strings.IndexByte(mimeType, ';'); i >= 0 {
		mimeType = strings.TrimSpace(mimeType[:i])
	}
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return mimeType, true
	default:
		return "", false
	}
}

var readyPlaceholders = [...]string{
	"Ready!",
	"Ready...",
	"Ready?",
	"Ready for instructions",
}

var workingPlaceholders = [...]string{
	"Agent running - Esc cancels",
}

// randomizePlaceholders selects random placeholder text for the textarea's
// ready and working states.
func (m *UI) randomizePlaceholders() {
	m.workingPlaceholder = workingPlaceholders[rand.Intn(len(workingPlaceholders))]
	m.readyPlaceholder = readyPlaceholders[rand.Intn(len(readyPlaceholders))]
}

func (m *UI) activeEditorBusyPlaceholder() string {
	status := m.activeRunStatusText()
	if status == "" {
		return "Agent running - Esc cancels"
	}
	return status + " - Esc cancels"
}

func (m *UI) runtimeStatusLine() string {
	if m == nil || m.com == nil || m.com.Workspace == nil {
		return ""
	}
	stats := m.com.Workspace.BackgroundShellStats()
	parts := make([]string, 0, 6)
	if activityStatus := m.activeRuntimeActivityStatusPart(); activityStatus != "" {
		parts = append(parts, activityStatus)
	} else if busyStatus := m.activeRunStatusText(); busyStatus != "" {
		parts = append(parts, busyStatus)
	} else if m.isAgentBusy() {
		parts = append(parts, "model running")
	}
	if stats.Running > 0 {
		parts = append(parts, fmt.Sprintf("jobs %d", stats.Running))
	}
	if stats.ActiveMonitors > 0 {
		parts = append(parts, fmt.Sprintf("monitor %d", stats.ActiveMonitors))
	}
	parts = append(parts, m.toolRuntimeStatusParts()...)
	parts = append(parts, m.taskRuntimeStatusParts()...)
	if contextStatus := m.contextUsageStatus(); contextStatus != "" {
		parts = append(parts, contextStatus)
	}
	return strings.Join(parts, "  ·  ")
}

func (m *UI) activeRuntimeActivityStatusPart() string {
	if status := m.compactionRuntimeStatusPart(); status != "" {
		return status
	}
	if status := m.memoryRuntimeStatusPart(chat.RuntimeActivityMemoryRecall, "memory recall"); status != "" {
		return status
	}
	return m.memoryRuntimeStatusPart(chat.RuntimeActivityMemorySave, "memory save")
}

func (m *UI) compactionRuntimeStatusPart() string {
	if m == nil || m.session == nil {
		return ""
	}
	item := m.runtimeActivities[compactionActivityID(m.session.ID)]
	if item == nil {
		return ""
	}
	snapshot := item.Snapshot()
	if snapshot.Kind != chat.RuntimeActivityConversationCompaction ||
		snapshot.Status != chat.RuntimeActivityRunning {
		return ""
	}
	parts := []string{"compacting"}
	if !snapshot.StartedAt.IsZero() {
		parts = append(parts, formatRuntimeStatusDuration(time.Since(snapshot.StartedAt)))
	}
	if snapshot.Tokens > 0 {
		tokens := formatStatusTokenCount(snapshot.Tokens) + " tokens"
		if !snapshot.TokensAreExact {
			tokens = "~" + tokens
		}
		parts = append(parts, tokens)
	}
	return strings.Join(parts, " · ")
}

func (m *UI) memoryRuntimeStatusPart(kind chat.RuntimeActivityKind, label string) string {
	if m == nil || m.session == nil {
		return ""
	}
	item := m.runtimeActivities[memoryActivityID(m.session.ID, kind)]
	if item == nil {
		return ""
	}
	snapshot := item.Snapshot()
	if snapshot.Kind != kind || snapshot.Status != chat.RuntimeActivityRunning {
		return ""
	}
	parts := []string{label}
	if !snapshot.StartedAt.IsZero() {
		parts = append(parts, formatRuntimeStatusDuration(time.Since(snapshot.StartedAt)))
	}
	return strings.Join(parts, " · ")
}

func formatRuntimeStatusDuration(duration time.Duration) string {
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

func (m *UI) contextUsagePercent() string {
	usedTokens, contextWindow, hasUsage, hasContextWindow := m.contextUsage()
	if !hasUsage || !hasContextWindow {
		return ""
	}
	percentage := (float64(usedTokens) / float64(contextWindow)) * 100
	return fmt.Sprintf("%d%%", int(percentage))
}

func (m *UI) recordTaskRuntimeEvent(ev scheduler.Event) {
	if ev.Kind == "" {
		return
	}
	key := taskRuntimeEventKey(ev)
	if key == "" {
		return
	}
	if m.taskRuntimeEvents == nil {
		m.taskRuntimeEvents = make(map[string]scheduler.Event)
	}
	m.taskRuntimeEvents[key] = ev
	pruneTaskRuntimeEvents(m.taskRuntimeEvents)
}

func (m *UI) shouldHandleRuntimeTrace(trace agentruntime.TaskTrace) bool {
	if m == nil || m.session == nil {
		return true
	}
	if trace.ConversationSessionID == "" {
		return true
	}
	return trace.ConversationSessionID == m.session.ID
}

func (m *UI) shouldHandleBackgroundJobEvent(ev shell.BackgroundJobEvent) bool {
	if m == nil || m.session == nil {
		return true
	}
	if ev.SessionID == "" {
		return true
	}
	return ev.SessionID == m.session.ID
}

func (m *UI) handleRuntimeActivityTrace(trace agentruntime.TaskTrace) tea.Cmd {
	sessionID := trace.ConversationSessionID
	if sessionID == "" {
		sessionID = trace.SessionID
	}
	if sessionID == "" && m.session != nil {
		sessionID = m.session.ID
	}
	if sessionID == "" {
		return nil
	}
	switch {
	case isConversationCompactionTrace(trace.Kind):
		return m.upsertRuntimeActivity(compactionActivitySnapshot(sessionID, trace))
	case isMemoryRuntimeTrace(trace.Kind):
		return m.upsertRuntimeActivity(memoryActivitySnapshot(sessionID, trace))
	default:
		return nil
	}
}

func isConversationCompactionTrace(kind agentruntime.TraceKind) bool {
	return kind == agentruntime.TraceKindConversationCompactionStarted ||
		kind == agentruntime.TraceKindConversationCompactionProgress ||
		kind == agentruntime.TraceKindConversationCompactionFinished ||
		kind == agentruntime.TraceKindConversationCompactionFailed
}

func isMemoryRuntimeTrace(kind agentruntime.TraceKind) bool {
	return kind == agentruntime.TraceKindMemoryRecallStarted ||
		kind == agentruntime.TraceKindMemoryRecallFinished ||
		kind == agentruntime.TraceKindMemoryRecallFailed ||
		kind == agentruntime.TraceKindMemorySaveStarted ||
		kind == agentruntime.TraceKindMemorySaveFinished ||
		kind == agentruntime.TraceKindMemorySaveFailed
}

func compactionActivitySnapshot(sessionID string, trace agentruntime.TaskTrace) chat.RuntimeActivitySnapshot {
	status := chat.RuntimeActivityRunning
	title := "Compacting conversation"
	detail := compactionTraceDetail(trace)
	finishedAt := trace.FinishedAt
	switch trace.Kind {
	case agentruntime.TraceKindConversationCompactionFinished:
		status = chat.RuntimeActivityDone
		title = "Compacted conversation"
	case agentruntime.TraceKindConversationCompactionFailed:
		status = chat.RuntimeActivityFailed
		title = "Compaction failed"
		if trace.Error != "" {
			detail = trace.Error
		}
	}
	tokens, exact := tokensFromTraceForRuntimeActivity(trace)
	return chat.RuntimeActivitySnapshot{
		ID:              compactionActivityID(sessionID),
		Kind:            chat.RuntimeActivityConversationCompaction,
		Status:          status,
		Title:           title,
		Detail:          detail,
		StartedAt:       trace.StartedAt,
		FinishedAt:      finishedAt,
		Tokens:          tokens,
		TokensAreExact:  exact,
		ProgressPercent: -1,
	}
}

func memoryActivitySnapshot(sessionID string, trace agentruntime.TaskTrace) chat.RuntimeActivitySnapshot {
	status := chat.RuntimeActivityRunning
	title := "Memory"
	kind := chat.RuntimeActivityMemoryRecall
	finishedAt := trace.FinishedAt
	switch trace.Kind {
	case agentruntime.TraceKindMemoryRecallStarted:
		title = "Recalling memory"
	case agentruntime.TraceKindMemoryRecallFinished:
		status = chat.RuntimeActivityDone
		title = "Memory recalled"
	case agentruntime.TraceKindMemoryRecallFailed:
		status = chat.RuntimeActivityFailed
		title = "Memory recall failed"
	case agentruntime.TraceKindMemorySaveStarted:
		kind = chat.RuntimeActivityMemorySave
		title = "Saving memory"
	case agentruntime.TraceKindMemorySaveFinished:
		kind = chat.RuntimeActivityMemorySave
		status = chat.RuntimeActivityDone
		title = "Memory saved"
	case agentruntime.TraceKindMemorySaveFailed:
		kind = chat.RuntimeActivityMemorySave
		status = chat.RuntimeActivityFailed
		title = "Memory save failed"
	}
	detail := ""
	if trace.FileCount > 0 {
		detail = fmt.Sprintf("%d memories", trace.FileCount)
	}
	if trace.Error != "" {
		detail = trace.Error
	}
	return chat.RuntimeActivitySnapshot{
		ID:              memoryActivityID(sessionID, kind),
		Kind:            kind,
		Status:          status,
		Title:           title,
		Detail:          detail,
		StartedAt:       trace.StartedAt,
		FinishedAt:      finishedAt,
		ProgressPercent: -1,
	}
}

func compactionTraceDetail(trace agentruntime.TaskTrace) string {
	parts := make([]string, 0, 2)
	if trace.ProviderID != "" || trace.ModelID != "" {
		model := trace.ModelID
		if model == "" {
			model = "model"
		}
		if trace.ProviderID != "" {
			model = trace.ProviderID + "/" + model
		}
		parts = append(parts, model)
	}
	if trace.ContextMessageCount > 0 {
		parts = append(parts, fmt.Sprintf("%d context messages", trace.ContextMessageCount))
	}
	return strings.Join(parts, " · ")
}

func tokensFromTraceForRuntimeActivity(trace agentruntime.TaskTrace) (int64, bool) {
	switch {
	case trace.TotalTokens > 0:
		return trace.TotalTokens, true
	case trace.InputTokens+trace.OutputTokens > 0:
		return trace.InputTokens + trace.OutputTokens, true
	case trace.PreflightEstimatedInputTokens > 0:
		return trace.PreflightEstimatedInputTokens, false
	default:
		return 0, false
	}
}

func (m *UI) upsertRuntimeActivity(snapshot chat.RuntimeActivitySnapshot) tea.Cmd {
	if m == nil || m.chat == nil || snapshot.ID == "" {
		return nil
	}
	if m.runtimeActivities == nil {
		m.runtimeActivities = make(map[string]*chat.RuntimeActivityItem)
	}
	item := m.runtimeActivities[snapshot.ID]
	if item == nil {
		item = chat.NewRuntimeActivityItem(m.com.Styles, snapshot)
		m.runtimeActivities[snapshot.ID] = item
	}
	return m.chat.UpsertRuntimeActivity(item, snapshot)
}

func compactionActivityID(sessionID string) string {
	return "runtime:compaction:" + sessionID
}

func memoryActivityID(sessionID string, kind chat.RuntimeActivityKind) string {
	return "runtime:" + string(kind) + ":" + sessionID
}

func (m *UI) recordToolRuntimeTrace(trace agentruntime.TaskTrace) {
	if !isToolRuntimeTrace(trace.Kind) {
		return
	}
	key := toolRuntimeTraceKey(trace)
	if key == "" {
		return
	}
	if m.toolRuntimeEvents == nil {
		m.toolRuntimeEvents = make(map[string]agentruntime.TaskTrace)
	}
	m.toolRuntimeEvents[key] = trace
	pruneToolRuntimeTraces(m.toolRuntimeEvents)
}

func (m *UI) recordLLMContextTrace(trace agentruntime.TaskTrace) {
	if !isLLMContextTrace(trace.Kind) {
		return
	}
	if trace.PreflightEstimatedInputTokens <= 0 &&
		trace.TotalTokens <= 0 &&
		trace.InputTokens+trace.OutputTokens <= 0 {
		return
	}
	if m.latestLLMContextTrace.Sequence > trace.Sequence && trace.Sequence > 0 {
		return
	}
	m.latestLLMContextTrace = trace
}

func isLLMContextTrace(kind agentruntime.TraceKind) bool {
	return kind == agentruntime.TraceKindLLMStarted ||
		kind == agentruntime.TraceKindLLMFirstEvent ||
		kind == agentruntime.TraceKindLLMFirstText ||
		kind == agentruntime.TraceKindLLMRetry ||
		kind == agentruntime.TraceKindLLMFinished ||
		kind == agentruntime.TraceKindLLMFailed
}

func isToolRuntimeTrace(kind agentruntime.TraceKind) bool {
	return kind == agentruntime.TraceKindToolStarted ||
		kind == agentruntime.TraceKindToolFinished ||
		kind == agentruntime.TraceKindToolFailed
}

func toolRuntimeTraceKey(trace agentruntime.TaskTrace) string {
	if trace.ToolCallID != "" {
		return trace.ToolCallID
	}
	if trace.ToolName != "" && trace.Sequence > 0 {
		return fmt.Sprintf("%s:%d", trace.ToolName, trace.Sequence)
	}
	if trace.TraceKey() != "" {
		return trace.TraceKey()
	}
	return ""
}

func pruneToolRuntimeTraces(events map[string]agentruntime.TaskTrace) {
	const maxToolRuntimeEvents = 128
	if len(events) <= maxToolRuntimeEvents {
		return
	}
	type keyedTrace struct {
		key   string
		trace agentruntime.TaskTrace
	}
	entries := make([]keyedTrace, 0, len(events))
	for key, trace := range events {
		entries = append(entries, keyedTrace{key: key, trace: trace})
	}
	slices.SortFunc(entries, func(left, right keyedTrace) int {
		return left.trace.RecordedAt.Compare(right.trace.RecordedAt)
	})
	for len(events) > maxToolRuntimeEvents && len(entries) > 0 {
		entry := entries[0]
		entries = entries[1:]
		if isTerminalToolTrace(entry.trace.Kind) {
			delete(events, entry.key)
		}
	}
	for len(events) > maxToolRuntimeEvents && len(entries) > 0 {
		entry := entries[0]
		entries = entries[1:]
		delete(events, entry.key)
	}
}

func (m *UI) toolRuntimeStatusParts() []string {
	if m == nil || len(m.toolRuntimeEvents) == 0 {
		return nil
	}
	summary := summarizeToolRuntimeTraces(m.toolRuntimeEvents)
	if summary.running == 0 && summary.failed == 0 {
		return nil
	}
	parts := make([]string, 0, 2)
	if summary.running > 0 {
		parts = append(parts, fmt.Sprintf("tools %d running", summary.running))
		parts = append(parts, fmt.Sprintf("tool-parallel %d", summary.running))
	}
	if summary.failed > 0 {
		parts = append(parts, fmt.Sprintf("tool-failed %d", summary.failed))
	}
	if names := summary.activeToolNames(); names != "" {
		parts = append(parts, names)
	}
	return parts
}

type toolRuntimeSummary struct {
	running int
	failed  int
	names   map[string]int
}

func summarizeToolRuntimeTraces(events map[string]agentruntime.TaskTrace) toolRuntimeSummary {
	summary := toolRuntimeSummary{names: make(map[string]int)}
	for _, trace := range events {
		switch trace.Kind {
		case agentruntime.TraceKindToolStarted:
			summary.running++
			name := trace.ToolName
			if name == "" {
				name = "tool"
			}
			summary.names[name]++
		case agentruntime.TraceKindToolFailed:
			summary.failed++
		}
	}
	return summary
}

func (s toolRuntimeSummary) activeToolNames() string {
	if len(s.names) == 0 {
		return ""
	}
	type namedCount struct {
		name  string
		count int
	}
	entries := make([]namedCount, 0, len(s.names))
	for name, count := range s.names {
		entries = append(entries, namedCount{name: name, count: count})
	}
	slices.SortFunc(entries, func(left, right namedCount) int {
		if left.count != right.count {
			return right.count - left.count
		}
		return strings.Compare(left.name, right.name)
	})
	const maxToolNames = 3
	if len(entries) > maxToolNames {
		entries = entries[:maxToolNames]
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.count > 1 {
			parts = append(parts, fmt.Sprintf("%s %d", entry.name, entry.count))
		} else {
			parts = append(parts, entry.name)
		}
	}
	return "active " + strings.Join(parts, "/")
}

func taskRuntimeEventKey(ev scheduler.Event) string {
	switch {
	case ev.NodeID != "":
		return ev.NodeID
	case ev.SessionID != "":
		return ev.SessionID
	case ev.TraceID != "":
		return ev.TraceID
	case ev.Goal != "":
		return string(ev.Profile) + ":" + ev.Goal
	default:
		return ""
	}
}

func pruneTaskRuntimeEvents(events map[string]scheduler.Event) {
	const maxTaskRuntimeEvents = 64
	if len(events) <= maxTaskRuntimeEvents {
		return
	}
	type keyedEvent struct {
		key string
		ev  scheduler.Event
	}
	entries := make([]keyedEvent, 0, len(events))
	for key, ev := range events {
		entries = append(entries, keyedEvent{key: key, ev: ev})
	}
	slices.SortFunc(entries, func(left, right keyedEvent) int {
		return left.ev.RecordedAt.Compare(right.ev.RecordedAt)
	})
	for len(events) > maxTaskRuntimeEvents && len(entries) > 0 {
		entry := entries[0]
		entries = entries[1:]
		if isTerminalTaskEvent(entry.ev.Kind) {
			delete(events, entry.key)
		}
	}
	for len(events) > maxTaskRuntimeEvents && len(entries) > 0 {
		entry := entries[0]
		entries = entries[1:]
		delete(events, entry.key)
	}
}

func (m *UI) taskRuntimeStatusParts() []string {
	if m == nil || len(m.taskRuntimeEvents) == 0 {
		return nil
	}
	summary := summarizeTaskRuntimeEvents(m.taskRuntimeEvents)
	if summary.total == 0 {
		return nil
	}

	parts := make([]string, 0, 3)
	switch {
	case summary.running > 0:
		parts = append(parts, fmt.Sprintf("dag %d running/%d", summary.running, summary.total))
	case summary.planned > 0:
		parts = append(parts, fmt.Sprintf("dag %d planned/%d", summary.planned, summary.total))
	case summary.failed > 0:
		parts = append(parts, fmt.Sprintf("dag %d failed/%d", summary.failed, summary.total))
	default:
		parts = append(parts, fmt.Sprintf("dag %d done", summary.done))
	}
	if summary.running > 0 {
		parts = append(parts, fmt.Sprintf("parallel %d", summary.running))
	}
	if profileStatus := summary.profileStatus(); profileStatus != "" {
		parts = append(parts, profileStatus)
	}
	return parts
}

type taskRuntimeSummary struct {
	total   int
	planned int
	running int
	done    int
	failed  int
	brain   int
	plan    int
	explore int
	worker  int
	auditor int
}

func summarizeTaskRuntimeEvents(events map[string]scheduler.Event) taskRuntimeSummary {
	var summary taskRuntimeSummary
	for _, ev := range events {
		if ev.Kind == "" {
			continue
		}
		summary.total++
		switch ev.Kind {
		case scheduler.EventTaskPlanned:
			summary.planned++
		case scheduler.EventTaskStarted, scheduler.EventTaskProgress:
			summary.running++
			summary.countActiveProfile(ev.Profile)
		case scheduler.EventTaskFinished:
			summary.done++
		case scheduler.EventTaskFailed:
			summary.failed++
		}
	}
	return summary
}

func (s *taskRuntimeSummary) countActiveProfile(profile scheduler.WorkerProfile) {
	switch profile {
	case scheduler.ProfileBrainAgent:
		s.brain++
	case scheduler.ProfilePlanAgent:
		s.plan++
	case scheduler.ProfileExploreAgent:
		s.explore++
	case scheduler.ProfileWorkerAgent:
		s.worker++
	case scheduler.ProfileAuditorAgent:
		s.auditor++
	}
}

func (s taskRuntimeSummary) profileStatus() string {
	if s.brain+s.plan+s.explore+s.worker+s.auditor == 0 {
		return ""
	}
	parts := make([]string, 0, 5)
	if s.running > 0 {
		parts = append(parts, fmt.Sprintf("explore %d", s.explore))
	}
	if s.worker > 0 {
		parts = append(parts, fmt.Sprintf("worker %d", s.worker))
	}
	if s.plan > 0 {
		parts = append(parts, fmt.Sprintf("plan %d", s.plan))
	}
	if s.auditor > 0 {
		parts = append(parts, fmt.Sprintf("auditor %d", s.auditor))
	}
	if s.brain > 0 {
		parts = append(parts, fmt.Sprintf("brain %d", s.brain))
	}
	return "agents " + strings.Join(parts, "/")
}

func isTerminalTaskEvent(kind scheduler.EventKind) bool {
	return kind == scheduler.EventTaskFinished || kind == scheduler.EventTaskFailed
}

func isTerminalToolTrace(kind agentruntime.TraceKind) bool {
	return kind == agentruntime.TraceKindToolFinished || kind == agentruntime.TraceKindToolFailed
}

func (m *UI) contextUsageStatus() string {
	usedTokens, contextWindow, hasUsage, hasContextWindow := m.contextUsage()
	if !hasUsage {
		return ""
	}
	if !hasContextWindow {
		return fmt.Sprintf(
			"ctx -- %s/unknown auto@%d%%",
			formatStatusTokenCount(usedTokens),
			autoCompactContextPercent,
		)
	}
	percentage := (float64(usedTokens) / float64(contextWindow)) * 100
	status := fmt.Sprintf(
		"ctx %d%% %s/%s auto@%d%%",
		int(percentage),
		formatStatusTokenCount(usedTokens),
		formatStatusTokenCount(contextWindow),
		autoCompactContextPercent,
	)
	if int(percentage) >= autoCompactContextPercent {
		status += " compact"
	}
	return status
}

func (m *UI) contextUsage() (usedTokens int64, contextWindow int64, hasUsage bool, hasContextWindow bool) {
	if m == nil || m.com == nil || m.session == nil {
		return 0, 0, false, false
	}
	contextWindow = contextWindowForBrain(m.com)
	usedTokens = m.session.CompletionTokens + m.session.PromptTokens
	if trace, ok := m.latestLLMContextTraceForSession(); ok {
		if trace.ContextWindowTokens > 0 {
			contextWindow = trace.ContextWindowTokens
		}
		if traceUsedTokens := contextUsedTokensFromTrace(trace); traceUsedTokens > usedTokens {
			usedTokens = traceUsedTokens
		}
	}
	return usedTokens, contextWindow, true, contextWindow > 0
}

func (m *UI) latestLLMContextTraceForSession() (agentruntime.TaskTrace, bool) {
	if m == nil || m.session == nil {
		return agentruntime.TaskTrace{}, false
	}
	trace := m.latestLLMContextTrace
	if trace.Kind == "" {
		return agentruntime.TaskTrace{}, false
	}
	if trace.ConversationSessionID != "" && trace.ConversationSessionID != m.session.ID {
		return agentruntime.TaskTrace{}, false
	}
	return trace, true
}

func contextUsedTokensFromTrace(trace agentruntime.TaskTrace) int64 {
	switch {
	case trace.TotalTokens > 0:
		return trace.TotalTokens
	case trace.InputTokens+trace.OutputTokens > 0:
		return trace.InputTokens + trace.OutputTokens
	case trace.PreflightEstimatedInputTokens > 0:
		return trace.PreflightEstimatedInputTokens
	default:
		return 0
	}
}

func contextWindowForBrain(com *common.Common) int64 {
	if com == nil {
		return 0
	}
	if com.Workspace != nil && com.Workspace.AgentIsReady() {
		model := com.Workspace.AgentModel()
		if contextWindow := config.ResolveModelContextWindow(
			int64(model.CatwalkCfg.ContextWindow),
			model.ModelCfg.Provider,
			"",
			model.ModelCfg.Model,
		); contextWindow > 0 {
			return contextWindow
		}
	}
	cfg := com.Config()
	if cfg == nil {
		return 0
	}
	agentCfg, ok := cfg.Agents[config.AgentBrain]
	if !ok {
		return 0
	}
	model := cfg.GetModelByType(agentCfg.Model)
	if model != nil && model.ContextWindow > 0 {
		return model.ContextWindow
	}
	selectedModel, ok := cfg.Models[agentCfg.Model]
	if !ok {
		return 0
	}
	providerType := ""
	if providerCfg, ok := cfg.Providers.Get(selectedModel.Provider); ok {
		providerType = string(providerCfg.Type)
	}
	return config.ResolveModelContextWindow(0, selectedModel.Provider, providerType, selectedModel.Model)
}

func formatStatusTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return trimTokenUnit(fmt.Sprintf("%.1fM", float64(tokens)/1_000_000))
	case tokens >= 1_000:
		return trimTokenUnit(fmt.Sprintf("%.1fK", float64(tokens)/1_000))
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func trimTokenUnit(value string) string {
	value = strings.Replace(value, ".0K", "K", 1)
	value = strings.Replace(value, ".0M", "M", 1)
	return value
}

func statusMessageTTL(msg util.InfoMsg) time.Duration {
	ttl := msg.TTL
	if ttl <= 0 {
		ttl = DefaultStatusTTL
	}
	switch msg.Type {
	case util.InfoTypeError:
		return maxDuration(ttl, DefaultErrorStatusTTL)
	case util.InfoTypeWarn:
		return maxDuration(ttl, DefaultWarnStatusTTL)
	default:
		return ttl
	}
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

// renderEditorView renders the editor view with attachments if any.
//
// Layout intent: the attachment chip strip lives **inside** the editor
// block, indented to the same column as the textarea prompt (`::: `). That
// way pasted images and file chips read as part of the user's current
// turn — same affordance free-code (Claude Code) uses where image refs
// sit inline in the composer — instead of a free-floating row above it
// that looks like static UI chrome.
func (m *UI) renderEditorView(width int) string {
	editorBody := m.textarea.View()
	if len(m.attachments.List()) == 0 {
		return strings.Join([]string{
			editorBody,
			"", // margin at bottom of editor
		}, "\n")
	}
	// Prompt width is the visual width of `::: ` (4 cells). Indent chips
	// to that column so they align with the cursor's leftmost typing
	// position — visually they become an inline continuation of input.
	const promptWidth = 4
	chipsView := m.attachments.Render(max(0, width-promptWidth))
	chipsView = lipgloss.NewStyle().PaddingLeft(promptWidth).Render(chipsView)
	return strings.Join([]string{
		chipsView,
		editorBody,
		"", // margin at bottom of editor
	}, "\n")
}

// cacheSidebarLogo renders and caches the sidebar logo at the specified width.
func (m *UI) cacheSidebarLogo(width int) {
	m.sidebarLogo = renderLogo(m.com.Styles, true, m.com.IsHyper(), width)
}

// applyTheme replaces the active styles with the given theme, drops the
// shared markdown renderer cache, and refreshes every component that
// caches style data.
func (m *UI) applyTheme(s styles.Styles) {
	*m.com.Styles = s
	common.InvalidateMarkdownRendererCache()
	m.refreshStyles()
}

// refreshStyles pushes the current *m.com.Styles into every subcomponent
// that copies or pre-renders style-dependent values at construction time.
func (m *UI) refreshStyles() {
	t := m.com.Styles
	m.header.refresh()
	if m.layout.sidebar.Dx() > 0 {
		m.cacheSidebarLogo(m.layout.sidebar.Dx())
	}
	m.textarea.SetStyles(t.Editor.Textarea)
	m.completions.SetStyles(t.Completions.Normal, t.Completions.Focused, t.Completions.Match)
	m.attachments.Renderer().SetStyles(
		t.Attachments.Normal,
		t.Attachments.Deleting,
		t.Attachments.Image,
		t.Attachments.Text,
	)
	m.todoSpinner.Style = t.Pills.TodoSpinner
	m.status.help.Styles = t.Help
	m.chat.InvalidateRenderCaches()
}

// sendMessage sends a message with the given content and attachments.
func (m *UI) sendMessage(content string, attachments ...message.Attachment) tea.Cmd {
	if !m.com.Workspace.AgentIsReady() {
		return util.ReportError(fmt.Errorf("worker agent is not initialized"))
	}

	var cmds []tea.Cmd
	if !m.hasSession() {
		mode := m.currentSessionMode()
		sessionTitle := "New Session"
		if mode.IsPlan() {
			sessionTitle = "Plan Session"
		}
		newSession, err := m.com.Workspace.CreateSession(context.Background(), sessionTitle, mode)
		if err != nil {
			return util.ReportError(err)
		}
		if m.forceCompactMode {
			m.isCompact = true
		}
		if newSession.ID != "" {
			m.session = &newSession
			m.setCurrentSessionMode(mode)
			cmds = append(cmds, m.loadSession(newSession.ID))
		}
		m.setState(uiChat, m.focus)
	}

	ctx := context.Background()
	cmds = append(cmds, func() tea.Msg {
		for _, path := range m.sessionFileReads {
			m.com.Workspace.FileTrackerRecordRead(ctx, m.session.ID, path)
			m.com.Workspace.LSPStart(ctx, path)
		}
		return nil
	})

	// Capture session ID to avoid race with main goroutine updating m.session.
	sessionID := m.session.ID
	planMode := m.currentSessionMode().IsPlan()
	cmds = append(cmds, func() tea.Msg {
		err := m.com.Workspace.AgentRun(context.Background(), sessionID, content, planMode, attachments...)
		if err != nil {
			isCancelErr := errors.Is(err, context.Canceled) || errors.Is(err, agent.ErrRequestCancelled)
			if isCancelErr {
				return nil
			}
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("%v", err),
			}
		}
		return nil
	})
	return tea.Batch(cmds...)
}

const ctrlCTimerDuration = 2 * time.Second

// ctrlCTimerCmd creates a command that expires the Ctrl+C quit arm.
func ctrlCTimerCmd() tea.Cmd {
	return tea.Tick(ctrlCTimerDuration, func(time.Time) tea.Msg {
		return ctrlCTimerExpiredMsg{}
	})
}

// cancelAgent cancels the running agent on ESC. Always clears the queue
// and cancels the in-flight run atomically — one press is enough to stop
// everything, including mid-retry loops.
func (m *UI) cancelAgent() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	if !m.com.Workspace.AgentIsReady() {
		return nil
	}

	prompts, wasRunning := m.com.Workspace.AgentCancelAndFlush(m.session.ID)
	m.todoIsSpinning = false
	m.renderPills()
	m.appendInterruptDivider()

	// Snapshot whatever the user has typed (but not yet sent) into the
	// composer as the active history draft. Without this, ↑/↓ after ESC
	// would treat the unsent text as nothing and overwrite it on the first
	// historyPrev() call, making the cancel + edit + re-send flow lose the
	// in-progress draft.
	m.promptHistory.draft = m.textarea.Value()
	m.promptHistory.index = -1

	if len(prompts) > 0 && wasRunning {
		slog.Info("Cancelled inflight, drained queued prompts to composer", "session_id", m.session.ID, "count", len(prompts))
		var mergedPrompt strings.Builder
		currentDraft := m.textarea.Value()
		if currentDraft != "" {
			mergedPrompt.WriteString(currentDraft)
			mergedPrompt.WriteString("\n\n")
		}
		for i, p := range prompts {
			if i > 0 {
				mergedPrompt.WriteString("\n\n")
			}
			mergedPrompt.WriteString(p)
		}
		m.textarea.SetValue(mergedPrompt.String())
		m.textarea.MoveToEnd()
	}
	return tea.Batch(
		m.restoreLastUserPrompt(m.session.ID),
		m.repairInterruptedSession(),
	)
}

func (m *UI) hasUnfinishedChatItem() bool {
	if m.chat == nil {
		return false
	}
	for i := 0; i < m.chat.list.Len(); i++ {
		item := m.chat.list.ItemAt(i)
		if item != nil && !item.Finished() {
			return true
		}
	}
	return false
}

func (m *UI) appendInterruptDivider() {
	if m.chat == nil || m.session == nil {
		return
	}
	id := m.interruptDividerSourceID()
	if m.chat.MessageItem(chat.InterruptDividerID(id)) != nil {
		return
	}
	m.chat.AppendMessages(chat.NewInterruptDividerItem(m.com.Styles, id))
	m.chat.ForceScrollToBottom()
}

func (m *UI) interruptDividerSourceID() string {
	for i := m.chat.list.Len() - 1; i >= 0; i-- {
		item := m.chat.list.ItemAt(i)
		if item == nil || item.Finished() {
			continue
		}
		if identifiable, ok := item.(chat.Identifiable); ok && identifiable.ID() != "" {
			return identifiable.ID()
		}
	}
	return "esc-cancel-" + m.session.ID
}

func (m *UI) repairInterruptedSession() tea.Cmd {
	if !m.hasSession() {
		return nil
	}
	m.appendInterruptDivider()
	sessionID := m.session.ID
	return func() tea.Msg {
		if err := m.com.Workspace.RepairSessionMessages(context.Background(), sessionID); err != nil {
			slog.Error("Failed to repair interrupted session messages", "session_id", sessionID, "error", err)
			return util.NewErrorMsg(err)
		}
		return m.loadSession(sessionID)()
	}
}

func (m *UI) restoreLastUserPrompt(sessionID string) tea.Cmd {
	return func() tea.Msg {
		messages, err := m.com.Workspace.ListUserMessages(context.Background(), sessionID)
		if err != nil || len(messages) == 0 {
			if err != nil {
				slog.Error("Failed to restore cancelled prompt", "session_id", sessionID, "error", err)
			}
			return nil
		}
		return restoreCanceledPromptMsg{
			sessionID: sessionID,
			text:      messages[0].Content().Text,
		}
	}
}

// clearComposer clears the current draft, attachments, and completion state.
func (m *UI) clearComposer() tea.Cmd {
	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	if m.attachments != nil {
		m.attachments.Reset()
	}
	if m.completions != nil {
		m.closeCompletions()
	} else {
		m.completionsOpen = false
		m.completionsQuery = ""
		m.completionsStartIndex = 0
	}
	m.historyReset()
	if m.status == nil || m.chat == nil {
		return nil
	}
	return m.handleTextareaHeightChange(prevHeight)
}

// openDialog opens a dialog by its ID.
func (m *UI) openDialog(id string) tea.Cmd {
	var cmds []tea.Cmd
	switch id {
	case dialog.SessionsID:
		if cmd := m.openSessionsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ModelsID:
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.CommandsID:
		if cmd := m.openCommandsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ReasoningID:
		if cmd := m.openReasoningDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.FilePickerID:
		if cmd := m.openFilesDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.QuitID:
		if cmd := m.openQuitDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	default:
		// Unknown dialog
		break
	}
	return tea.Batch(cmds...)
}

// openQuitDialog opens the quit confirmation dialog.
func (m *UI) openQuitDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.QuitID) {
		// Bring to front
		m.dialog.BringToFront(dialog.QuitID)
		return nil
	}

	quitDialog := dialog.NewQuit(m.com)
	m.dialog.OpenDialog(quitDialog)
	return nil
}

// openModelsDialog opens the models dialog.
func (m *UI) openModelsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ModelsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.ModelsID)
		return nil
	}

	isOnboarding := m.state == uiOnboarding
	modelsDialog, err := dialog.NewModels(m.com, isOnboarding)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(modelsDialog)

	return nil
}

// openCommandsDialog opens the commands dialog.
func (m *UI) openCommandsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.CommandsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.CommandsID)
		return nil
	}

	var sessionID string
	hasSession := m.session != nil
	if hasSession {
		sessionID = m.session.ID
	}
	hasTodos := hasSession && hasIncompleteTodos(m.session.Todos)
	hasQueue := m.promptQueue > 0

	commands, err := dialog.NewCommands(m.com, sessionID, hasSession, hasTodos, hasQueue, m.customCommands, m.mcpPrompts)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(commands)

	return commands.InitialCmd()
}

// openReasoningDialog opens the reasoning effort dialog.
func (m *UI) openReasoningDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ReasoningID) {
		m.dialog.BringToFront(dialog.ReasoningID)
		return nil
	}

	reasoningDialog, err := dialog.NewReasoning(m.com)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(reasoningDialog)
	return nil
}

// openSessionsDialog opens the sessions dialog. If the dialog is already open,
// it brings it to the front. Otherwise, it will list all the sessions and open
// the dialog.
func (m *UI) openSessionsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.SessionsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.SessionsID)
		return nil
	}

	selectedSessionID := ""
	if m.session != nil {
		selectedSessionID = m.session.ID
	}

	dialog, err := dialog.NewSessions(m.com, selectedSessionID)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(dialog)
	return nil
}

// openFilesDialog opens the file picker dialog.
func (m *UI) openFilesDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.FilePickerID) {
		// Bring to front
		m.dialog.BringToFront(dialog.FilePickerID)
		return nil
	}

	filePicker, cmd := dialog.NewFilePicker(m.com)
	filePicker.SetImageCapabilities(&m.caps)
	m.dialog.OpenDialog(filePicker)

	return cmd
}

// openPermissionsDialog opens the permissions dialog for a permission request.
func (m *UI) openPermissionsDialog(perm permission.PermissionRequest) tea.Cmd {
	// Close any existing permissions dialog first.
	m.dialog.CloseDialog(dialog.PermissionsID)

	// Get diff mode from config.
	var opts []dialog.PermissionsOption
	if diffMode := m.com.Config().Options.TUI.DiffMode; diffMode != "" {
		opts = append(opts, dialog.WithDiffMode(diffMode == "split"))
	}

	permDialog := dialog.NewPermissions(m.com, perm, opts...)
	m.dialog.OpenDialog(permDialog)
	return nil
}

// handlePermissionNotification updates tool items when permission state changes.
func (m *UI) handlePermissionNotification(notification permission.PermissionNotification) {
	toolItem := m.chat.MessageItem(notification.ToolCallID)
	if toolItem == nil {
		return
	}

	if permItem, ok := toolItem.(chat.ToolMessageItem); ok {
		if notification.Granted {
			permItem.SetStatus(chat.ToolStatusRunning)
		} else {
			permItem.SetStatus(chat.ToolStatusAwaitingPermission)
		}
	}
}

// handleAgentNotification translates domain agent events into desktop
// notifications using the UI notification backend.
func (m *UI) handleAgentNotification(n notify.Notification) tea.Cmd {
	switch n.Type {
	case notify.TypeAgentFinished:
		var cmds []tea.Cmd
		cmds = append(cmds, m.sendNotification(notification.Notification{
			Title:   "Crush is waiting...",
			Message: fmt.Sprintf("Agent's turn completed in \"%s\"", n.SessionTitle),
		}))
		if m.com.IsHyper() {
			cmds = append(cmds, m.fetchHyperCredits())
		}
		// Drain any sub-agent entries that are still marked running; they
		// will never receive a normal Finished/Failed event at this point.
		m.subAgents = drainRunningSubAgents(m.subAgents)
		// Cancel any agent tool calls in the chat view that never received
		// a result, so their spinners don't persist after the brain stops.
		m.chat.CancelDanglingAgentTools()
		return tea.Batch(cmds...)
	case notify.TypeReAuthenticate:
		return m.handleReAuthenticate(n.ProviderID)
	case notify.TypeSubAgentStarted, notify.TypeSubAgentFinished, notify.TypeSubAgentFailed:
		m.subAgents = recordSubAgentEvent(m.subAgents, n)
		return nil
	default:
		return nil
	}
}

func (m *UI) handleReAuthenticate(providerID string) tea.Cmd {
	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}
	providerCfg, ok := cfg.Providers.Get(providerID)
	if !ok {
		return nil
	}
	agentCfg, ok := cfg.Agents[config.AgentBrain]
	if !ok {
		return nil
	}
	return m.openAuthenticationDialog(providerCfg.ToProvider(), cfg.Models[agentCfg.Model], agentCfg.Model)
}

// newSession clears the current session state and prepares for a new session.
// The actual session creation happens when the user sends their first message.
// Returns a command to reload prompt history.
func (m *UI) newSession() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	m.pendingSessionMode = m.currentSessionMode()
	m.session = nil
	m.sessionFiles = nil
	m.sessionFileReads = nil
	m.setState(uiLanding, uiFocusEditor)
	m.textarea.Focus()
	m.chat.Blur()
	m.chat.ClearMessages()
	m.pillsExpanded = true
	m.promptQueue = 0
	m.pillsView = ""
	m.historyReset()
	return tea.Batch(
		func() tea.Msg {
			m.com.Workspace.LSPStopAll(context.Background())
			return nil
		},
		m.loadPromptHistory(),
	)
}

// handlePasteMsg handles a paste message.
func (m *UI) handlePasteMsg(msg tea.PasteMsg) tea.Cmd {
	m.ctrlCArmed = false
	// Normalize \r\n before the textarea sanitizer sees it.
	msg.Content = strings.ReplaceAll(msg.Content, "\r\n", "\n")

	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	if m.focus != uiFocusEditor {
		// Snap focus to editor so the paste lands somewhere visible.
		m.focus = uiFocusEditor
		cmds := []tea.Cmd{m.textarea.Focus()}
		m.chat.Blur()
		if cmd := m.chat.ForceScrollToBottomAndAnimate(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return tea.Batch(append(cmds, func() tea.Msg { return msg })...)
	}

	if hasPasteExceededThreshold(msg) {
		return func() tea.Msg {
			content := []byte(msg.Content)
			if int64(len(content)) > common.MaxAttachmentSize {
				return util.ReportWarn("Paste is too big (>5mb)")
			}
			name := fmt.Sprintf("paste_%d.txt", m.pasteIdx())
			mimeBufferSize := min(512, len(content))
			mimeType := http.DetectContentType(content[:mimeBufferSize])
			return message.Attachment{
				FileName: name,
				FilePath: name,
				MimeType: mimeType,
				Content:  content,
			}
		}
	}

	// Attempt to parse pasted content as file paths. If possible to parse,
	// all files exist and are valid, add as attachments.
	// Otherwise, paste as text.
	paths := fsext.ParsePastedFiles(msg.Content)
	allExistsAndValid := func() bool {
		if len(paths) == 0 {
			return false
		}
		for _, path := range paths {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return false
			}

			lowerPath := strings.ToLower(path)
			isValid := false
			for _, ext := range common.AllowedImageTypes {
				if strings.HasSuffix(lowerPath, ext) {
					isValid = true
					break
				}
			}
			if !isValid {
				return false
			}
		}
		return true
	}
	if !allExistsAndValid() {
		prevHeight := m.textarea.Height()
		return m.updateTextareaWithPrevHeight(msg, prevHeight)
	}

	var cmds []tea.Cmd
	for _, path := range paths {
		cmds = append(cmds, m.handleFilePathPaste(path))
	}
	return tea.Batch(cmds...)
}

func hasPasteExceededThreshold(msg tea.PasteMsg) bool {
	var (
		lineCount = 0
		colCount  = 0
	)
	for line := range strings.SplitSeq(msg.Content, "\n") {
		lineCount++
		colCount = max(colCount, len(line))

		if lineCount > pasteLinesThreshold || colCount > pasteColsThreshold {
			return true
		}
	}
	return false
}

// handleFilePathPaste handles a pasted file path.
func (m *UI) handleFilePathPaste(path string) tea.Cmd {
	return func() tea.Msg {
		fileInfo, err := os.Stat(path)
		if err != nil {
			return util.ReportError(err)
		}
		if fileInfo.IsDir() {
			return util.ReportWarn("Cannot attach a directory")
		}
		if fileInfo.Size() > common.MaxAttachmentSize {
			return util.ReportWarn("File is too big (>5mb)")
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return util.ReportError(err)
		}

		mimeType, ok := supportedImageMimeOf(content)
		if !ok {
			return util.ReportWarn("File is not a supported image")
		}
		fileName := filepath.Base(path)
		return message.Attachment{
			FilePath: path,
			FileName: fileName,
			MimeType: mimeType,
			Content:  content,
		}
	}
}

// pasteImageFromClipboard reads image data from the system clipboard and
// creates an attachment. If no image data is found, it falls back to
// interpreting clipboard text as a file path.
func (m *UI) pasteImageFromClipboard() tea.Msg {
	imageData, err := readClipboard(clipboardFormatImage)
	if int64(len(imageData)) > common.MaxAttachmentSize {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "File too large, max 5MB",
		}
	}
	name := fmt.Sprintf("paste_%d.png", m.pasteIdx())
	if err == nil && len(imageData) > 0 {
		mimeType, ok := supportedImageMimeOf(imageData)
		if !ok {
			return util.NewInfoMsg("Clipboard image is not a supported image format")
		}
		return message.Attachment{
			FilePath: name,
			FileName: name,
			MimeType: mimeType,
			Content:  imageData,
		}
	}

	textData, textErr := readClipboard(clipboardFormatText)
	if textErr != nil || len(textData) == 0 {
		return nil // Clipboard is empty or does not contain an image
	}

	path := strings.TrimSpace(string(textData))
	path = strings.ReplaceAll(path, "\\ ", " ")
	if _, statErr := os.Stat(path); statErr != nil {
		return nil // Clipboard does not contain an image or valid file path
	}

	lowerPath := strings.ToLower(path)
	isAllowed := false
	for _, ext := range common.AllowedImageTypes {
		if strings.HasSuffix(lowerPath, ext) {
			isAllowed = true
			break
		}
	}
	if !isAllowed {
		return util.NewInfoMsg("File type is not a supported image format")
	}

	fileInfo, statErr := os.Stat(path)
	if statErr != nil {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  fmt.Sprintf("Unable to read file: %v", statErr),
		}
	}
	if fileInfo.Size() > common.MaxAttachmentSize {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "File too large, max 5MB",
		}
	}

	content, readErr := os.ReadFile(path)
	if readErr != nil {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  fmt.Sprintf("Unable to read file: %v", readErr),
		}
	}

	mimeType, ok := supportedImageMimeOf(content)
	if !ok {
		return util.NewInfoMsg("File type is not a supported image format")
	}

	return message.Attachment{
		FilePath: path,
		FileName: filepath.Base(path),
		MimeType: mimeType,
		Content:  content,
	}
}

var pasteRE = regexp.MustCompile(`paste_(\d+).txt`)

func (m *UI) pasteIdx() int {
	result := 0
	for _, at := range m.attachments.List() {
		found := pasteRE.FindStringSubmatch(at.FileName)
		if len(found) == 0 {
			continue
		}
		idx, err := strconv.Atoi(found[1])
		if err == nil {
			result = max(result, idx)
		}
	}
	return result + 1
}

// drawSessionDetails draws the session details in compact mode.
func (m *UI) drawSessionDetails(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	s := m.com.Styles

	width := area.Dx() - s.CompactDetails.View.GetHorizontalFrameSize()
	height := area.Dy() - s.CompactDetails.View.GetVerticalFrameSize()

	title := s.CompactDetails.Title.Width(width).MaxHeight(2).Render(m.session.Title)
	blocks := []string{
		title,
		"",
		m.modelInfo(width),
		"",
	}

	detailsHeader := lipgloss.JoinVertical(
		lipgloss.Left,
		blocks...,
	)

	version := s.CompactDetails.Version.Width(width).AlignHorizontal(lipgloss.Right).Render(version.Version)

	remainingHeight := height - lipgloss.Height(detailsHeader) - lipgloss.Height(version)

	const maxSectionWidth = 50
	sectionWidth := max(1, min(maxSectionWidth, width/4-2)) // account for spacing between sections
	maxItemsPerSection := remainingHeight - 3               // Account for section title and spacing

	lspSection := m.lspInfo(sectionWidth, maxItemsPerSection, false)
	mcpSection := m.mcpInfo(sectionWidth, maxItemsPerSection, false)
	skillsSection := m.skillsInfo(sectionWidth, maxItemsPerSection, false)
	filesSection := m.filesInfo(m.com.Workspace.WorkingDir(), sectionWidth, maxItemsPerSection, false)
	sections := lipgloss.JoinHorizontal(lipgloss.Top, filesSection, " ", lspSection, " ", mcpSection, " ", skillsSection)
	uv.NewStyledString(
		s.CompactDetails.View.
			Width(area.Dx()).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Left,
					detailsHeader,
					sections,
					version,
				),
			),
	).Draw(scr, area)
}

func (m *UI) runMCPPrompt(clientID, promptID string, arguments map[string]string) tea.Cmd {
	load := func() tea.Msg {
		prompt, err := m.com.Workspace.GetMCPPrompt(clientID, promptID, arguments)
		if err != nil {
			// TODO: make this better
			return util.ReportError(err)()
		}

		if prompt == "" {
			return nil
		}
		return sendMessageMsg{
			Content: prompt,
		}
	}

	var cmds []tea.Cmd
	if cmd := m.dialog.StartLoading(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	cmds = append(cmds, load, func() tea.Msg {
		return closeDialogMsg{}
	})

	return tea.Sequence(cmds...)
}

func (m *UI) handleStateChanged() tea.Cmd {
	return func() tea.Msg {
		m.com.Workspace.UpdateAgentModel(context.Background())
		return mcpStateChangedMsg{
			states: m.com.Workspace.MCPGetStates(),
		}
	}
}

func handleMCPPromptsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshPrompts(context.Background(), name)
		return nil
	}
}

func handleMCPToolsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.RefreshMCPTools(context.Background(), name)
		return nil
	}
}

func handleMCPResourcesEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshResources(context.Background(), name)
		return nil
	}
}

func (m *UI) copyChatHighlight() tea.Cmd {
	text := m.chat.HighlightContent()
	return common.CopyToClipboardWithCallback(
		text,
		"Selected text copied to clipboard",
		func() tea.Msg {
			m.chat.ClearMouse()
			return nil
		},
	)
}

func (m *UI) enableDockerMCP() tea.Msg {
	ctx := context.Background()
	if err := m.com.Workspace.EnableDockerMCP(ctx); err != nil {
		return util.ReportError(err)()
	}

	return util.NewInfoMsg("Docker MCP enabled and started successfully")
}

func (m *UI) disableDockerMCP() tea.Msg {
	if err := m.com.Workspace.DisableDockerMCP(); err != nil {
		return util.ReportError(err)()
	}

	return util.NewInfoMsg("Docker MCP disabled successfully")
}

func (m *UI) handleSchedulerEvent(ev scheduler.Event) tea.Cmd {
	if m.status == nil {
		return nil
	}
	if !m.shouldHandleSchedulerEvent(ev) {
		return nil
	}

	taskLabel := ev.Goal
	if taskLabel == "" {
		taskLabel = ev.NodeID
	}
	if len(ev.Scope) > 0 && taskLabel != "" {
		taskLabel = fmt.Sprintf("%s (%s)", taskLabel, strings.Join(ev.Scope, ", "))
	}

	switch ev.Kind {
	case scheduler.EventTaskPlanned:
		msgID := m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeInfo,
			Msg:  "Task planned: " + taskLabel,
			TTL:  3 * time.Second,
		})
		return clearInfoMsgCmd(msgID, 3*time.Second)
	case scheduler.EventTaskStarted:
		msgID := m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeInfo,
			Msg:  "Task started: " + taskLabel,
			TTL:  3 * time.Second,
		})
		var cmds []tea.Cmd
		cmds = append(cmds, clearInfoMsgCmd(msgID, 3*time.Second))
		if m.state == uiChat {
			if m.chat.Follow() {
				cmds = append(cmds, m.chat.ForceScrollToBottomAndAnimate())
			} else if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return tea.Batch(cmds...)
	case scheduler.EventTaskProgress:
		msg := ev.Status
		if msg == "" {
			msg = taskLabel
		}
		msgID := m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeInfo,
			Msg:  "Task progress: " + msg,
			TTL:  3 * time.Second,
		})
		var cmds []tea.Cmd
		cmds = append(cmds, clearInfoMsgCmd(msgID, 3*time.Second))
		if m.state == uiChat {
			if m.chat.Follow() {
				cmds = append(cmds, m.chat.ForceScrollToBottomAndAnimate())
			} else if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return tea.Batch(cmds...)
	case scheduler.EventTaskFinished:
		msgID := m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeSuccess,
			Msg:  "Task finished: " + taskLabel,
			TTL:  4 * time.Second,
		})
		return clearInfoMsgCmd(msgID, 4*time.Second)
	case scheduler.EventTaskFailed:
		msg := ev.Error
		if msg == "" {
			msg = "Task failed: " + taskLabel
		}
		msgID := m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  msg,
			TTL:  DefaultErrorStatusTTL,
		})
		return clearInfoMsgCmd(msgID, DefaultErrorStatusTTL)
	default:
		msgID := m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeInfo,
			Msg:  "Task event: " + taskLabel,
			TTL:  3 * time.Second,
		})
		return clearInfoMsgCmd(msgID, 3*time.Second)
	}
}

func (m *UI) shouldHandleSchedulerEvent(ev scheduler.Event) bool {
	if m == nil || m.session == nil {
		return true
	}
	if ev.ConversationSessionID == "" {
		return true
	}
	return ev.ConversationSessionID == m.session.ID
}

// renderLogo renders the Crush logo with the given styles and dimensions.
func renderLogo(t *styles.Styles, compact, hyper bool, width int) string {
	return logo.Render(t.Logo.GradCanvas, version.Version, compact, logo.Opts{
		FieldColor:   t.Logo.FieldColor,
		TitleColorA:  t.Logo.TitleColorA,
		TitleColorB:  t.Logo.TitleColorB,
		CharmColor:   t.Logo.CharmColor,
		VersionColor: t.Logo.VersionColor,
		Width:        width,
		Hyper:        hyper,
	})
}
