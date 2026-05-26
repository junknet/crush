package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/suggestion"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/relay"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
)

// AppWorkspace implements the Workspace interface by delegating
// directly to an in-process [app.App] instance. This is the default
// mode when the client/server architecture is not enabled.
type AppWorkspace struct {
	app   *app.App
	store *config.ConfigStore
}

// NewAppWorkspace creates a new AppWorkspace wrapping the given app
// and config store.
func NewAppWorkspace(a *app.App, store *config.ConfigStore) *AppWorkspace {
	return &AppWorkspace{
		app:   a,
		store: store,
	}
}

// -- Sessions --

func (w *AppWorkspace) CreateSession(ctx context.Context, title string, mode session.Mode) (session.Session, error) {
	return w.app.Sessions.Create(ctx, title, mode)
}

func (w *AppWorkspace) GetSession(ctx context.Context, sessionID string) (session.Session, error) {
	return w.app.Sessions.Get(ctx, sessionID)
}

func (w *AppWorkspace) ListSessions(ctx context.Context) ([]session.Session, error) {
	return w.app.Sessions.List(ctx)
}

func (w *AppWorkspace) SaveSession(ctx context.Context, sess session.Session) (session.Session, error) {
	return w.app.Sessions.Save(ctx, sess)
}

func (w *AppWorkspace) DeleteSession(ctx context.Context, sessionID string) error {
	return w.app.Sessions.Delete(ctx, sessionID)
}

func (w *AppWorkspace) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return w.app.Sessions.CreateAgentToolSessionID(messageID, toolCallID)
}

func (w *AppWorkspace) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	return w.app.Sessions.ParseAgentToolSessionID(sessionID)
}

// -- Messages --

func (w *AppWorkspace) ListMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	// Drain any debounced updates so the caller observes the latest
	// in-memory state. message.Service buffers streaming deltas and a
	// cold List would otherwise miss them at session-switch time.
	if err := w.app.Messages.FlushAll(ctx); err != nil {
		return nil, err
	}
	return w.app.Messages.List(ctx, sessionID)
}

func (w *AppWorkspace) ListUserMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	return w.app.Messages.ListUserMessages(ctx, sessionID)
}

func (w *AppWorkspace) ListAllUserMessages(ctx context.Context) ([]message.Message, error) {
	return w.app.Messages.ListAllUserMessages(ctx)
}

func (w *AppWorkspace) RepairSessionMessages(ctx context.Context, sessionID string) error {
	if err := w.app.Messages.FlushAll(ctx); err != nil {
		slog.Error("Failed to flush messages during repair", "error", err)
	}

	msgs, err := w.app.Messages.List(ctx, sessionID)
	if err != nil {
		return err
	}

	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}

		if !msg.IsFinished() {
			slog.Info("Repairing unfinished assistant message on load", "session_id", sessionID, "message_id", msg.ID)

			// 1. Mark all of its tool calls as finished if they are not
			toolCalls := msg.ToolCalls()
			for i := range toolCalls {
				tc := &toolCalls[i]
				if !tc.Finished {
					tc.Finished = true
					tc.Input = "{}"
					msg.AddToolCall(*tc)

					// Create a fallback tool result in the DB so that the TUI can render a terminated result
					toolResult := message.ToolResult{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Content:    "Error: session was closed or crashed",
						IsError:    true,
					}
					_, createErr := w.app.Messages.Create(ctx, msg.SessionID, message.CreateMessageParams{
						Role: message.Tool,
						Parts: []message.ContentPart{
							toolResult,
						},
					})
					if createErr != nil {
						slog.Error("Failed to create fallback tool result during repair", "error", createErr)
					}
				}
			}

			// 2. Mark the assistant message itself as finished (canceled)
			msg.AddFinish(message.FinishReasonCanceled, "Interrupted", "The session was closed or crashed during execution.")

			// 3. Save/Update the assistant message in the DB
			if err := w.app.Messages.Update(ctx, msg); err != nil {
				slog.Error("Failed to save repaired assistant message", "error", err)
			}
		}
	}
	return nil
}

// -- Agent --

func (w *AppWorkspace) AgentRun(ctx context.Context, sessionID, prompt string, planMode bool, attachments ...message.Attachment) error {
	if w.app.AgentCoordinator == nil {
		return errors.New("agent coordinator not initialized")
	}
	_, err := w.app.AgentCoordinator.Run(ctx, sessionID, prompt, planMode, attachments...)
	return err
}

func (w *AppWorkspace) AgentCancel(sessionID string) {
	if w.app.AgentCoordinator != nil {
		w.app.AgentCoordinator.Cancel(sessionID)
	}
}

func (w *AppWorkspace) AgentCancelAndFlush(sessionID string) ([]string, bool) {
	if w.app.AgentCoordinator != nil {
		return w.app.AgentCoordinator.CancelAndFlush(sessionID)
	}
	return nil, false
}

func (w *AppWorkspace) AgentIsBusy() bool {
	if w.app.AgentCoordinator == nil {
		return false
	}
	return w.app.AgentCoordinator.IsBusy()
}

func (w *AppWorkspace) AgentIsSessionBusy(sessionID string) bool {
	if w.app.AgentCoordinator == nil {
		return false
	}
	return w.app.AgentCoordinator.IsSessionBusy(sessionID)
}

func (w *AppWorkspace) AgentModel() AgentModel {
	if w.app.AgentCoordinator == nil {
		return AgentModel{}
	}
	m := w.app.AgentCoordinator.Model()
	return AgentModel{
		CatwalkCfg: m.CatwalkCfg,
		ModelCfg:   m.ModelCfg,
	}
}

func (w *AppWorkspace) AgentIsReady() bool {
	return w.app.AgentCoordinator != nil
}

func (w *AppWorkspace) AgentQueuedPrompts(sessionID string) int {
	if w.app.AgentCoordinator == nil {
		return 0
	}
	return w.app.AgentCoordinator.QueuedPrompts(sessionID)
}

func (w *AppWorkspace) AgentQueuedPromptsList(sessionID string) []string {
	if w.app.AgentCoordinator == nil {
		return nil
	}
	return w.app.AgentCoordinator.QueuedPromptsList(sessionID)
}

func (w *AppWorkspace) AgentClearQueue(sessionID string) {
	if w.app.AgentCoordinator != nil {
		w.app.AgentCoordinator.ClearQueue(sessionID)
	}
}

func (w *AppWorkspace) AgentSuggestion() AgentSuggestionService {
	if w.app.AgentCoordinator == nil {
		return nil
	}
	svc := w.app.AgentCoordinator.Suggestion()
	if svc == nil {
		return nil
	}
	return &appSuggestionService{
		svc:   svc,
		coord: w.app.AgentCoordinator,
	}
}

// appSuggestionService adapts *suggestion.Service to the workspace-level
// AgentSuggestionService interface so the TUI doesn't import the agent
// package transitively.
type appSuggestionService struct {
	svc   *suggestion.Service
	coord agent.Coordinator
}

func (a *appSuggestionService) Subscribe(ctx context.Context) <-chan AgentSuggestionEvent {
	src := a.svc.Subscribe(ctx)
	out := make(chan AgentSuggestionEvent, 16)
	go func() {
		defer close(out)
		for ev := range src {
			select {
			case <-ctx.Done():
				return
			case out <- AgentSuggestionEvent{
				SessionID: ev.Payload.SessionID,
				Text:      ev.Payload.Text,
			}:
			}
		}
	}()
	return out
}

func (a *appSuggestionService) Latest(sessionID string) (string, bool) {
	return a.svc.Latest(sessionID)
}

func (a *appSuggestionService) MarkAccepted(sessionID, method string, length int) {
	a.svc.MarkAccepted(sessionID, method, length)
	if a.coord != nil {
		_ = a.coord.PromoteSpeculativeSession(context.Background(), sessionID)
	}
}

func (a *appSuggestionService) MarkRejected(sessionID string, length int) {
	a.svc.MarkRejected(sessionID, length)
}

func (w *AppWorkspace) AgentSummarize(ctx context.Context, sessionID string) error {
	if w.app.AgentCoordinator == nil {
		return errors.New("agent coordinator not initialized")
	}
	return w.app.AgentCoordinator.Summarize(ctx, sessionID)
}

func (w *AppWorkspace) UpdateAgentModel(ctx context.Context) error {
	return w.app.UpdateAgentModel(ctx)
}

func (w *AppWorkspace) InitBrainAgent(ctx context.Context) error {
	return w.app.InitBrainAgent(ctx)
}

func (w *AppWorkspace) GetDefaultExploreModel(providerID string) config.SelectedModel {
	return w.app.GetDefaultExploreModel(providerID)
}

func (w *AppWorkspace) BackgroundShellStats() shell.BackgroundShellStats {
	if w.app.BackgroundShells == nil {
		return shell.BackgroundShellStats{}
	}
	return w.app.BackgroundShells.Stats()
}

// -- Permissions --

func (w *AppWorkspace) PermissionGrant(perm permission.PermissionRequest) {
	w.app.Permissions.Grant(perm)
}

func (w *AppWorkspace) PermissionGrantPersistent(perm permission.PermissionRequest) {
	w.app.Permissions.GrantPersistent(perm)
}

func (w *AppWorkspace) PermissionDeny(perm permission.PermissionRequest) {
	w.app.Permissions.Deny(perm)
}

// -- FileTracker --

func (w *AppWorkspace) FileTrackerRecordRead(ctx context.Context, sessionID, path string) {
	w.app.FileTracker.RecordRead(ctx, sessionID, path)
}

func (w *AppWorkspace) FileTrackerLastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return w.app.FileTracker.LastReadTime(ctx, sessionID, path)
}

func (w *AppWorkspace) FileTrackerListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return w.app.FileTracker.ListReadFiles(ctx, sessionID)
}

// -- History --

func (w *AppWorkspace) ListSessionHistory(ctx context.Context, sessionID string) ([]history.File, error) {
	return w.app.History.ListBySession(ctx, sessionID)
}

// -- LSP --

func (w *AppWorkspace) LSPStart(ctx context.Context, path string) {
	w.app.LSPManager.Start(ctx, path)
}

func (w *AppWorkspace) LSPStopAll(ctx context.Context) {
	w.app.LSPManager.StopAll(ctx)
}

func (w *AppWorkspace) LSPGetStates() map[string]LSPClientInfo {
	states := app.GetLSPStates()
	result := make(map[string]LSPClientInfo, len(states))
	for k, v := range states {
		result[k] = LSPClientInfo{
			Name:            v.Name,
			State:           v.State,
			Error:           v.Error,
			DiagnosticCount: v.DiagnosticCount,
			ConnectedAt:     v.ConnectedAt,
		}
	}
	return result
}

func (w *AppWorkspace) LSPGetDiagnosticCounts(name string) lsp.DiagnosticCounts {
	state, ok := app.GetLSPState(name)
	if !ok || state.Client == nil {
		return lsp.DiagnosticCounts{}
	}
	return state.Client.GetDiagnosticCounts()
}

// -- Config (read-only) --

func (w *AppWorkspace) Config() *config.Config {
	return w.store.Config()
}

func (w *AppWorkspace) WorkingDir() string {
	return w.store.WorkingDir()
}

func (w *AppWorkspace) Resolver() config.VariableResolver {
	return w.store.Resolver()
}

// -- Config mutations --

func (w *AppWorkspace) UpdatePreferredModel(modelType config.SelectedModelType, model config.SelectedModel) error {
	return w.store.UpdatePreferredModel(modelType, model)
}

func (w *AppWorkspace) SetCompactMode(enabled bool) error {
	return w.store.SetCompactMode(enabled)
}

func (w *AppWorkspace) SetProviderAPIKey(providerID string, apiKey any) error {
	return w.store.SetProviderAPIKey(providerID, apiKey)
}

func (w *AppWorkspace) SetConfigField(key string, value any) error {
	return w.store.SetConfigField(key, value)
}

func (w *AppWorkspace) RemoveConfigField(key string) error {
	return w.store.RemoveConfigField(key)
}

func (w *AppWorkspace) ImportCopilot() (*oauth.Token, bool) {
	return w.store.ImportCopilot()
}

func (w *AppWorkspace) RefreshOAuthToken(ctx context.Context, providerID string) error {
	return w.store.RefreshOAuthToken(ctx, providerID)
}

// -- Project lifecycle --

func (w *AppWorkspace) ProjectNeedsInitialization() (bool, error) {
	return config.ProjectNeedsInitialization(w.store)
}

func (w *AppWorkspace) MarkProjectInitialized() error {
	return config.MarkProjectInitialized(w.store)
}

func (w *AppWorkspace) InitializePrompt() (string, error) {
	return agent.InitializePrompt(w.store)
}

// -- MCP operations --

func (w *AppWorkspace) MCPGetStates() map[string]mcptools.ClientInfo {
	return mcptools.GetStates()
}

func (w *AppWorkspace) MCPRefreshPrompts(ctx context.Context, name string) {
	mcptools.RefreshPrompts(ctx, name)
}

func (w *AppWorkspace) MCPRefreshResources(ctx context.Context, name string) {
	mcptools.RefreshResources(ctx, name)
}

func (w *AppWorkspace) RefreshMCPTools(ctx context.Context, name string) {
	mcptools.RefreshTools(ctx, w.store, name)
}

func (w *AppWorkspace) ReadMCPResource(ctx context.Context, name, uri string) ([]MCPResourceContents, error) {
	contents, err := mcptools.ReadResource(ctx, w.store, name, uri)
	if err != nil {
		return nil, err
	}
	result := make([]MCPResourceContents, len(contents))
	for i, c := range contents {
		result[i] = MCPResourceContents{
			URI:      c.URI,
			MIMEType: c.MIMEType,
			Text:     c.Text,
			Blob:     c.Blob,
		}
	}
	return result, nil
}

func (w *AppWorkspace) GetMCPPrompt(clientID, promptID string, args map[string]string) (string, error) {
	return commands.GetMCPPrompt(w.store, clientID, promptID, args)
}

func (w *AppWorkspace) EnableDockerMCP(ctx context.Context) error {
	mcpConfig, err := w.store.PrepareDockerMCPConfig()
	if err != nil {
		return err
	}

	if err := mcptools.InitializeSingle(ctx, config.DockerMCPName, w.store); err != nil {
		disableErr := mcptools.DisableSingle(w.store, config.DockerMCPName)
		delete(w.store.Config().MCP, config.DockerMCPName)
		return fmt.Errorf("failed to start docker MCP: %w", errors.Join(err, disableErr))
	}

	if err := w.store.PersistDockerMCPConfig(mcpConfig); err != nil {
		disableErr := mcptools.DisableSingle(w.store, config.DockerMCPName)
		delete(w.store.Config().MCP, config.DockerMCPName)
		return fmt.Errorf("docker MCP started but failed to persist configuration: %w", errors.Join(err, disableErr))
	}

	return nil
}

func (w *AppWorkspace) DisableDockerMCP() error {
	if err := mcptools.DisableSingle(w.store, config.DockerMCPName); err != nil {
		return fmt.Errorf("failed to disable docker MCP: %w", err)
	}
	return w.store.DisableDockerMCP()
}

// -- Lifecycle --

// Subscribe streams app events into the TUI program and, when a NATS relay is
// configured (CRUSH_RELAY_NATS_URL), also mirrors this session to the relay so
// a phone can observe it live. The agent still runs locally; the relay is an
// optional, additive mirror.
func (w *AppWorkspace) Subscribe(program *tea.Program, driverSessionID string) {
	if cfg := relay.FromEnv(); cfg.NatsURL != "" && driverSessionID != "" {
		go relay.Run(context.Background(), program, w.app, w.store, driverSessionID, cfg)
	}
	w.app.Subscribe(program)
}

func (w *AppWorkspace) Shutdown() {
	w.app.Shutdown()
}

// App returns the underlying app.App instance.
func (w *AppWorkspace) App() *app.App {
	return w.app
}

// Store returns the underlying config store.
func (w *AppWorkspace) Store() *config.ConfigStore {
	return w.store
}

// Compile-time check that AppWorkspace implements Workspace.
var _ Workspace = (*AppWorkspace)(nil)
