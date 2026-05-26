// Package workspace defines the Workspace interface used by all
// frontends (TUI, CLI) to interact with a running workspace. Two
// implementations exist: one wrapping a local app.App instance and one
// wrapping the HTTP client SDK.
package workspace

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
)

// LSPClientInfo holds information about an LSP client's state. This is
// the frontend-facing type; implementations translate from the
// underlying app or proto representation.
type LSPClientInfo struct {
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
	ConnectedAt     time.Time
}

// LSPEventType represents the type of LSP event.
type LSPEventType string

const (
	LSPEventStateChanged       LSPEventType = "state_changed"
	LSPEventDiagnosticsChanged LSPEventType = "diagnostics_changed"
)

// LSPEvent represents an LSP event forwarded to the TUI.
type LSPEvent struct {
	Type            LSPEventType
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
}

// AgentModel holds the model information exposed to the UI.
type AgentModel struct {
	CatwalkCfg catwalk.Model
	ModelCfg   config.SelectedModel
}

// AgentSuggestionEvent is delivered when a fresh ghost-text suggestion is
// ready (Text empty = clear current ghost).
type AgentSuggestionEvent struct {
	SessionID string
	Text      string
}

// AgentSuggestionService is the subset of the suggestion service exposed
// to the TUI. Kept narrow so client-mode (remote workspace) implementations
// can satisfy it with a stub or future RPC bridge.
type AgentSuggestionService interface {
	// Subscribe yields suggestion events for the lifetime of ctx.
	Subscribe(ctx context.Context) <-chan AgentSuggestionEvent
	// Latest returns the most recent suggestion for a session, if any.
	Latest(sessionID string) (string, bool)
	// MarkAccepted records that the user accepted the suggestion via the
	// given method ("tab", "right", "enter"); also clears it.
	MarkAccepted(sessionID, method string, length int)
	// MarkRejected records that the user dismissed the suggestion; also
	// clears it.
	MarkRejected(sessionID string, length int)
}

// Workspace is the main abstraction consumed by the TUI and CLI. It
// groups every operation a frontend needs to perform against a running
// workspace, regardless of whether the workspace is in-process or
// remote.
type Workspace interface {
	// Sessions
	CreateSession(ctx context.Context, title string, mode session.Mode) (session.Session, error)
	GetSession(ctx context.Context, sessionID string) (session.Session, error)
	ListSessions(ctx context.Context) ([]session.Session, error)
	SaveSession(ctx context.Context, sess session.Session) (session.Session, error)
	DeleteSession(ctx context.Context, sessionID string) error
	CreateAgentToolSessionID(messageID, toolCallID string) string
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool)

	// Messages
	ListMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	ListUserMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	ListAllUserMessages(ctx context.Context) ([]message.Message, error)
	RepairSessionMessages(ctx context.Context, sessionID string) error

	// Agent
	AgentRun(ctx context.Context, sessionID, prompt string, planMode bool, attachments ...message.Attachment) error
	AgentCancel(sessionID string)
	AgentCancelAndFlush(sessionID string) ([]string, bool)
	AgentIsBusy() bool
	AgentIsSessionBusy(sessionID string) bool
	AgentModel() AgentModel
	AgentIsReady() bool
	AgentQueuedPrompts(sessionID string) int
	AgentQueuedPromptsList(sessionID string) []string
	AgentClearQueue(sessionID string)
	AgentSummarize(ctx context.Context, sessionID string) error
	UpdateAgentModel(ctx context.Context) error
	InitBrainAgent(ctx context.Context) error
	GetDefaultExploreModel(providerID string) config.SelectedModel
	BackgroundShellStats() shell.BackgroundShellStats

	// AgentSuggestion returns the ghost-text suggestion service, or nil
	// when disabled / not wired (e.g. client mode without local agent).
	AgentSuggestion() AgentSuggestionService

	// Permissions
	PermissionGrant(perm permission.PermissionRequest)
	PermissionGrantPersistent(perm permission.PermissionRequest)
	PermissionDeny(perm permission.PermissionRequest)

	// FileTracker
	FileTrackerRecordRead(ctx context.Context, sessionID, path string)
	FileTrackerLastReadTime(ctx context.Context, sessionID, path string) time.Time
	FileTrackerListReadFiles(ctx context.Context, sessionID string) ([]string, error)

	// History
	ListSessionHistory(ctx context.Context, sessionID string) ([]history.File, error)

	// LSP
	LSPStart(ctx context.Context, path string)
	LSPStopAll(ctx context.Context)
	LSPGetStates() map[string]LSPClientInfo
	LSPGetDiagnosticCounts(name string) lsp.DiagnosticCounts

	// Config (read-only data)
	Config() *config.Config
	WorkingDir() string
	Resolver() config.VariableResolver

	// Config mutations (proxied to server in client mode)
	UpdatePreferredModel(modelType config.SelectedModelType, model config.SelectedModel) error
	SetCompactMode(enabled bool) error
	SetProviderAPIKey(providerID string, apiKey any) error
	SetConfigField(key string, value any) error
	RemoveConfigField(key string) error
	ImportCopilot() (*oauth.Token, bool)
	RefreshOAuthToken(ctx context.Context, providerID string) error

	// Project lifecycle
	ProjectNeedsInitialization() (bool, error)
	MarkProjectInitialized() error
	InitializePrompt() (string, error)

	// MCP operations (server-side in client mode)
	MCPGetStates() map[string]mcptools.ClientInfo
	MCPRefreshPrompts(ctx context.Context, name string)
	MCPRefreshResources(ctx context.Context, name string)
	RefreshMCPTools(ctx context.Context, name string)
	ReadMCPResource(ctx context.Context, name, uri string) ([]MCPResourceContents, error)
	GetMCPPrompt(clientID, promptID string, args map[string]string) (string, error)
	EnableDockerMCP(ctx context.Context) error
	DisableDockerMCP() error

	// Events
	// Subscribe streams server events into the TUI program. driverSessionID,
	// when non-empty, declares this client as the live "driver" of that session
	// so the server drops it from the session-primary listing when the TUI exits.
	Subscribe(program *tea.Program, driverSessionID string)
	Shutdown()
}

// MCPResourceContents holds the contents of an MCP resource.
type MCPResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}
