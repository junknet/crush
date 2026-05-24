// Package relay publishes a local (authoritative) TUI session's event stream to
// a NATS server so a phone — another NATS client — can mirror it live, and
// relays the phone's commands (prompt / cancel / grant) back to this local
// agent. The agent always runs IN this process; the relay is a thin, optional,
// additive mirror. When no NATS URL is configured the relay is a no-op and the
// local TUI is completely unaffected.
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/pubsub"
)

const (
	streamName     = "CRUSH_EVENTS"
	eventsSubject  = "crush.sess.%s.events"
	presenceBucket = "CRUSH_SESSIONS"
	presenceTTL    = 15 * time.Second
	heartbeat      = 5 * time.Second
	// NATS default max_payload is ~1MB; cap below it and drop oversized events
	// rather than wedge the publisher.
	maxEventBytes = 900_000
)

// Config is the relay's NATS connection. Empty NatsURL disables the relay.
type Config struct {
	NatsURL string
	Token   string
}

// FromEnv reads the relay config from the environment. CRUSH_RELAY_NATS_URL
// being empty disables the relay (local TUI runs untouched).
func FromEnv() Config {
	return Config{
		NatsURL: os.Getenv("CRUSH_RELAY_NATS_URL"),
		Token:   os.Getenv("CRUSH_RELAY_TOKEN"),
	}
}

// SessionMeta is the presence record a phone reads to list live sessions.
//
// Provider/Model surface the *brain* role's currently selected model, refreshed
// every heartbeat, so a phone connecting after a `set_model` command sees the
// new selection without having to listen to a separate event stream.
type SessionMeta struct {
	SessionID       string                          `json:"session_id"`
	Path            string                          `json:"path"`
	Title           string                          `json:"title"`
	IsBusy          bool                            `json:"is_busy"`
	Alive           bool                            `json:"alive"`
	UpdatedAt       int64                           `json:"updated_at"`
	Provider        string                          `json:"provider,omitempty"`
	Model           string                          `json:"model,omitempty"`
	Models          map[string]config.SelectedModel `json:"models,omitempty"`
	AvailableModels []config.SelectedModel          `json:"available_models,omitempty"`
}

// Run connects to NATS and, until ctx is done, publishes this session's events
// to JetStream and heartbeats its presence. Blocking; intended to run in its
// own goroutine. Safe no-op when cfg.NatsURL is empty.
func Run(ctx context.Context, p *tea.Program, a *app.App, store *config.ConfigStore, sessionID string, cfg Config) {
	if cfg.NatsURL == "" || sessionID == "" || a == nil {
		return
	}

	nc, err := nats.Connect(cfg.NatsURL,
		nats.Token(cfg.Token),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.Name("crush-tui-"+sessionID),
	)
	if err != nil {
		slog.Warn("Relay NATS connect failed; mirror disabled", "url", cfg.NatsURL, "error", err)
		return
	}
	defer nc.Close()
	slog.Info("Relay connected to NATS", "url", cfg.NatsURL, "session", sessionID)

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Warn("Relay JetStream init failed", "error", err)
		return
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"crush.sess.*.events"},
		MaxAge:    24 * time.Hour,
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
	}); err != nil {
		slog.Warn("Relay stream ensure failed", "error", err)
		return
	}
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: presenceBucket,
		TTL:    presenceTTL,
	})
	if err != nil {
		slog.Warn("Relay presence KV ensure failed", "error", err)
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = kv.Delete(ctx, sessionID)
	}()

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	trigger := make(chan struct{}, 1)
	go presenceLoop(runCtx, a, store, sessionID, kv, trigger)
	go commandLoop(runCtx, p, a, sessionID, nc, runCancel)

	subject := "crush.sess." + sessionID + ".events"
	events := a.Events(ctx)
	for {
		select {
		case <-ctx.Done():
			_ = kv.Delete(context.Background(), sessionID)
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if notifyEv, ok := ev.Payload.(pubsub.Event[notify.Notification]); ok {
				if notifyEv.Payload.SessionID == sessionID {
					if notifyEv.Payload.Type == notify.TypeAgentStarted ||
						notifyEv.Payload.Type == notify.TypeAgentFinished {
						select {
						case trigger <- struct{}{}:
						default:
						}
					}
				}
			}

			payload := wrapEvent(ev.Payload)
			if payload == nil {
				continue
			}
			data, err := json.Marshal(payload)
			if err != nil || len(data) > maxEventBytes {
				// Drop unmarshalable or oversized events (e.g. a huge file
				// view) rather than block the stream; the phone is a mirror,
				// not the source of truth.
				if len(data) > maxEventBytes {
					slog.Debug("Relay dropped oversized event", "bytes", len(data))
				}
				continue
			}
			if _, err := js.Publish(ctx, subject, data); err != nil {
				slog.Debug("Relay publish failed", "error", err)
			}
		}
	}
}

func presenceLoop(ctx context.Context, a *app.App, store *config.ConfigStore, sessionID string, kv jetstream.KeyValue, trigger <-chan struct{}) {
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	put := func() {
		meta := SessionMeta{
			SessionID: sessionID,
			Path:      store.WorkingDir(),
			Alive:     true,
			UpdatedAt: time.Now().Unix(),
		}
		if sess, err := a.Sessions.Get(ctx, sessionID); err == nil {
			meta.Title = sess.Title
		}
		if a.AgentCoordinator != nil {
			meta.IsBusy = a.AgentCoordinator.IsSessionBusy(sessionID)
		}
		// Surface the brain agent's current model selection so the phone
		// chip stops showing the "未就绪" fallback and reflects whichever
		// model state.yaml currently picks (changes after `set_model`).
		if cfg := a.Config(); cfg != nil {
			if m, ok := cfg.Models[config.SelectedModelTypeBrain]; ok {
				meta.Provider = m.Provider
				meta.Model = m.Model
			}
			meta.Models = make(map[string]config.SelectedModel)
			for role, modelCfg := range cfg.Models {
				meta.Models[string(role)] = modelCfg
			}
			var available []config.SelectedModel
			for _, provider := range cfg.EnabledProviders() {
				for _, model := range provider.Models {
					available = append(available, config.SelectedModel{
						Provider: provider.ID,
						Model:    model.ID,
					})
				}
			}
			meta.AvailableModels = available
		}
		if b, err := json.Marshal(meta); err == nil {
			_, _ = kv.Put(ctx, sessionID, b)
		}
	}
	put()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			put()
		case <-trigger:
			put()
		}
	}
}

// Command is a JSON message sent from the phone to the TUI.
type Command struct {
	Type       string `json:"type"` // prompt, cancel, grant, set_model
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Action     string `json:"action,omitempty"` // allow, deny
	// set_model payload: which role's selected model to swap. Role is one
	// of "brain" / "worker" / "explore" matching config.SelectedModelType*
	// keys. Provider+Model are written verbatim to state.yaml via the
	// existing models.<role>.{provider,model} routing.
	Role     string `json:"role,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// RelayPromptMsg is sent to the TUI program to inject a prompt.
type RelayPromptMsg struct {
	Text string
}

// applySetModel persists a model selection coming from the phone into
// state.yaml via the existing isStateKey routing. Validation is intentionally
// thin: we trust the phone to pass a provider/model the user knows about
// (mobile picker only offers configured ones), and rely on the config store
// reload to refuse anything that won't actually resolve at dispatch time.
func applySetModel(a *app.App, cmd Command) error {
	if a == nil || a.Store() == nil {
		return fmt.Errorf("config store unavailable")
	}
	role := strings.ToLower(strings.TrimSpace(cmd.Role))
	if role == "" {
		return fmt.Errorf("role required")
	}
	if cmd.Provider == "" || cmd.Model == "" {
		return fmt.Errorf("provider and model required")
	}
	store := a.Store()
	return store.SetConfigFields(map[string]any{
		"models." + role + ".provider": cmd.Provider,
		"models." + role + ".model":    cmd.Model,
	})
}

func commandLoop(ctx context.Context, p *tea.Program, a *app.App, sessionID string, nc *nats.Conn, cancel context.CancelFunc) {
	sub, err := nc.Subscribe("crush.sess."+sessionID+".cmd", func(m *nats.Msg) {
		var cmd Command
		if err := json.Unmarshal(m.Data, &cmd); err != nil {
			slog.Debug("Relay failed to unmarshal command", "error", err)
			return
		}
		slog.Info("Relay received command", "type", cmd.Type, "session", sessionID)
		switch cmd.Type {
		case "prompt":
			if p != nil {
				p.Send(RelayPromptMsg{Text: cmd.Text})
			}
		case "cancel":
			if a.AgentCoordinator != nil {
				a.AgentCoordinator.Cancel(sessionID)
			}
		case "kill":
			slog.Info("Relay received kill command, exiting TUI process", "session", sessionID)
			cancel()
			if p != nil {
				p.Quit()
			}
		case "grant":
			req := a.Permissions.ActiveRequest()
			if req != nil && req.ToolCallID == cmd.ToolCallID {
				if cmd.Action == "allow" {
					a.Permissions.Grant(*req)
				} else {
					a.Permissions.Deny(*req)
				}
			}
		case "set_model":
			if err := applySetModel(a, cmd); err != nil {
				slog.Warn("Relay set_model failed", "role", cmd.Role, "provider", cmd.Provider, "model", cmd.Model, "error", err)
			} else {
				slog.Info("Relay set_model applied", "role", cmd.Role, "provider", cmd.Provider, "model", cmd.Model)
				if err := a.UpdateAgentModel(ctx); err != nil {
					slog.Warn("Relay failed to update agent model", "error", err)
				}
				if p != nil {
					p.Send(RelayModelUpdateMsg{
						Role:     cmd.Role,
						Provider: cmd.Provider,
						Model:    cmd.Model,
					})
				}
			}
		}
	})
	if err != nil {
		slog.Warn("Relay command subscribe failed", "error", err)
		return
	}
	defer sub.Unsubscribe()
	<-ctx.Done()
}

// RelayModelUpdateMsg is sent to the TUI program to notify it that a model configuration was updated.
type RelayModelUpdateMsg struct {
	Role     string
	Provider string
	Model    string
}


