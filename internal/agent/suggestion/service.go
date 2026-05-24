// Package suggestion provides ghost-text autocomplete suggestions for the
// prompt input. After an assistant turn finishes, the service asynchronously
// asks the title model to predict what the user is most likely to type next
// (2–12 words, instruction/question only, no filler) and publishes the
// result on a pubsub broker the TUI subscribes to.
//
// The TUI renders the suggestion in dim style after the cursor as ghost
// text; pressing Tab or Right-Arrow at end-of-buffer accepts it.
package suggestion

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

//go:embed prompt.md
var systemPrompt string

// minHistoryUserTurns is the minimum number of user turns required before
// we bother asking for a suggestion. Single-shot conversations don't yet
// have enough context to predict a useful next message.
const minHistoryUserTurns = 1

// minHistoryAssistantTurns gates on the assistant having actually responded
// at least once with text content. Without this, history may be empty or
// tool-call-only and the suggestion would just guess the first user turn.
const minHistoryAssistantTurns = 1

// maxWords is the upper bound on the suggestion length we accept from the
// model. Anything longer is treated as the model violating its contract;
// we drop the suggestion rather than truncate.
const maxWords = 12

// minWords is the lower bound; below this the suggestion is too thin to be
// worth showing.
const minWords = 2

// maxHistoryChars caps how much conversation we send into the suggestion
// prompt. Suggestion is best-effort utility work — paying for a huge
// context here is wasteful.
const maxHistoryChars = 4000

// callTimeout is the hard upper bound on how long we wait for a suggestion
// before giving up; if the model is slow the user will have typed already.
const callTimeout = 8 * time.Second

// fillerPhrases are evaluative/conversational filler we refuse to suggest
// because pressing Tab to "thanks" wastes the user's time.
var fillerPhrases = []string{
	"thanks",
	"thank you",
	"thx",
	"looks good",
	"lgtm",
	"perfect",
	"great",
	"nice",
	"cool",
	"ok",
	"okay",
	"got it",
	"sounds good",
	"awesome",
	"sure",
	"yes",
	"no",
}

// MessageLister is the subset of [message.Service] we need; carved out so
// tests can supply a fixed history without spinning the real service.
type MessageLister interface {
	List(ctx context.Context, sessionID string) ([]message.Message, error)
}

// ModelProvider supplies the fantasy LanguageModel used for suggestion
// calls. We accept any function so tests can inject a fake model without
// dragging in the full agent wiring.
type ModelProvider func() fantasy.LanguageModel

// Suggestion is the payload published when a fresh ghost-text candidate is
// available for a session. SessionID lets the TUI filter for the current
// session; Text is the trimmed candidate (empty string = clear ghost).
type Suggestion struct {
	SessionID string
	Text      string
}

// Service generates and broadcasts ghost-text suggestions per session.
type Service struct {
	messages     MessageLister
	modelProvide ModelProvider
	broker       *pubsub.Broker[Suggestion]
	latest       *csync.Map[string, string]

	// inflight tracks per-session cancel funcs so a fresh Generate call
	// can pre-empt a slow one for the same session.
	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc

	disabled bool
}

// New constructs a Service. messages may be nil only for tests that drive
// Generate with explicit history; in normal app wiring it must be the real
// message.Service.
func New(messages MessageLister, modelProvider ModelProvider, disabled bool) *Service {
	return &Service{
		messages:     messages,
		modelProvide: modelProvider,
		broker:       pubsub.NewBroker[Suggestion](),
		latest:       csync.NewMap[string, string](),
		inflight:     map[string]context.CancelFunc{},
		disabled:     disabled,
	}
}

// Subscribe returns a channel of suggestion events; close ctx to unsubscribe.
func (s *Service) Subscribe(ctx context.Context) <-chan pubsub.Event[Suggestion] {
	return s.broker.Subscribe(ctx)
}

// Latest returns the most recent suggestion for a session, if any.
func (s *Service) Latest(sessionID string) (string, bool) {
	if s == nil {
		return "", false
	}
	return s.latest.Get(sessionID)
}

// Clear drops the cached suggestion for a session and notifies subscribers.
// Called by the TUI when the user accepts, rejects, or starts a new turn.
func (s *Service) Clear(sessionID string) {
	if s == nil {
		return
	}
	s.latest.Del(sessionID)
	s.broker.Publish(pubsub.UpdatedEvent, Suggestion{SessionID: sessionID, Text: ""})
}

// Shutdown stops the broker; in-flight Generate calls return via context.
func (s *Service) Shutdown() {
	if s == nil {
		return
	}
	s.broker.Shutdown()
}

// Generate asks the title model to predict the user's likely next prompt and
// publishes the result. It is safe to call from any goroutine; multiple
// concurrent calls for the same session cancel earlier ones. Returns the
// suggestion text (possibly "") and any error from the underlying model.
//
// Call sites must NOT block on this; the typical pattern is
//
//	go svc.Generate(ctx, sessionID)
//
// from the coordinator after a brain turn ends.
func (s *Service) Generate(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.disabled {
		return "", nil
	}
	if sessionID == "" {
		return "", errors.New("suggestion: empty sessionID")
	}
	if s.messages == nil {
		return "", errors.New("suggestion: no message lister configured")
	}

	// Cancel any in-flight generation for this session so a fresh turn
	// doesn't race with a stale one.
	s.cancelPrevious(sessionID)
	genCtx, cancel := context.WithTimeout(ctx, callTimeout)
	s.recordInflight(sessionID, cancel)
	defer s.clearInflight(sessionID, cancel)
	defer cancel()

	msgs, err := s.messages.List(genCtx, sessionID)
	if err != nil {
		return "", err
	}
	if !s.historyReady(msgs) {
		slog.Debug("Suggestion skipped: history not ready", "session", sessionID, "messages", len(msgs))
		return "", nil
	}

	prompt := buildPrompt(msgs)
	if prompt == "" {
		return "", nil
	}

	model := s.modelProvide()
	if model == nil {
		slog.Debug("Suggestion skipped: no model available", "session", sessionID)
		return "", nil
	}

	agent := fantasy.NewAgent(
		model,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithMaxOutputTokens(40),
	)
	res, err := agent.Stream(genCtx, fantasy.AgentStreamCall{Prompt: prompt})
	if err != nil {
		slog.Debug("Suggestion call failed", "session", sessionID, "error", err)
		return "", err
	}
	if res == nil {
		return "", nil
	}

	candidate := sanitize(res.Response.Content.Text())
	if candidate == "" {
		slog.Debug("Suggestion empty after sanitize", "session", sessionID)
		s.latest.Del(sessionID)
		s.broker.Publish(pubsub.UpdatedEvent, Suggestion{SessionID: sessionID, Text: ""})
		return "", nil
	}

	s.latest.Set(sessionID, candidate)
	s.broker.Publish(pubsub.UpdatedEvent, Suggestion{SessionID: sessionID, Text: candidate})
	slog.Info("Suggestion shown", "session", sessionID, "length", len(candidate))
	return candidate, nil
}

// MarkAccepted records that the user accepted the current suggestion and
// drops it from cache. method is "tab" or "right" or "enter".
func (s *Service) MarkAccepted(sessionID, method string, length int) {
	if s == nil {
		return
	}
	slog.Info("Suggestion accepted", "session", sessionID, "method", method, "length", length)
	s.Clear(sessionID)
}

// MarkRejected records that the user dismissed the suggestion (any other
// keypress or explicit clear).
func (s *Service) MarkRejected(sessionID string, length int) {
	if s == nil {
		return
	}
	slog.Info("Suggestion rejected", "session", sessionID, "length", length)
	s.Clear(sessionID)
}

func (s *Service) cancelPrevious(sessionID string) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if prev, ok := s.inflight[sessionID]; ok {
		prev()
		delete(s.inflight, sessionID)
	}
}

func (s *Service) recordInflight(sessionID string, cancel context.CancelFunc) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	s.inflight[sessionID] = cancel
}

func (s *Service) clearInflight(sessionID string, cancel context.CancelFunc) {
	// We don't try to verify that the stored cancel is ours; cancelPrevious
	// owns that lifecycle. The only way to leak a stale entry is racing
	// with a fresh Generate that replaced us — and that one already wrote
	// its own cancel into the map under the lock. Leaving the new cancel
	// untouched is correct; the next Generate cleans up any stale entry
	// via cancelPrevious.
	_ = cancel
	_ = sessionID
}

// historyReady reports whether the message log has enough substance for a
// useful prediction. We require ≥1 user turn AND ≥1 assistant turn that
// produced text content.
func (s *Service) historyReady(msgs []message.Message) bool {
	userTurns, assistantTurns := 0, 0
	for _, m := range msgs {
		switch m.Role {
		case message.User:
			if strings.TrimSpace(m.Content().Text) != "" {
				userTurns++
			}
		case message.Assistant:
			if strings.TrimSpace(m.Content().Text) != "" {
				assistantTurns++
			}
		}
	}
	return userTurns >= minHistoryUserTurns && assistantTurns >= minHistoryAssistantTurns
}

// buildPrompt renders the recent history into a compact transcript the
// suggestion model can read. Tool calls, reasoning, and system messages
// are stripped — only user/assistant text matters for predicting the next
// user turn.
func buildPrompt(msgs []message.Message) string {
	var b strings.Builder
	b.WriteString("Recent conversation (oldest first):\n\n")
	// Walk newest-to-oldest, accumulating into a temporary slice so we can
	// emit oldest-first without buffering the whole transcript twice.
	type line struct {
		role string
		text string
	}
	rev := make([]line, 0, len(msgs))
	used := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		text := strings.TrimSpace(m.Content().Text)
		if text == "" {
			continue
		}
		var role string
		switch m.Role {
		case message.User:
			role = "User"
		case message.Assistant:
			role = "Assistant"
		default:
			continue
		}
		entry := line{role: role, text: text}
		// +len(role)+4 accounts for ": " prefix and "\n\n" separator.
		cost := len(text) + len(role) + 4
		if used+cost > maxHistoryChars && len(rev) > 0 {
			break
		}
		rev = append(rev, entry)
		used += cost
	}
	if len(rev) == 0 {
		return ""
	}
	for i := len(rev) - 1; i >= 0; i-- {
		b.WriteString(rev[i].role)
		b.WriteString(": ")
		b.WriteString(rev[i].text)
		b.WriteString("\n\n")
	}
	b.WriteString("Predict the user's next message (2-12 words, instruction or question only, no filler):")
	return b.String()
}

// sanitize cleans up raw model output: trim wrappers, drop empty / too-long
// / filler suggestions. Returns "" if the candidate should be discarded.
func sanitize(raw string) string {
	s := strings.TrimSpace(raw)
	// Strip markdown / quote wrappers a sloppy model might add.
	s = strings.Trim(s, "`\"' ")
	s = strings.TrimSpace(s)
	// Single line only.
	if idx := strings.IndexAny(s, "\n\r"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	// Drop common prefixes the model adds despite the system prompt.
	for _, prefix := range []string{"Suggestion:", "User:", "Next:", "->", "-"} {
		if strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix)) {
			s = strings.TrimSpace(s[len(prefix):])
		}
	}
	s = strings.Trim(s, "`\"' ")
	if s == "" {
		return ""
	}
	words := strings.Fields(s)
	if len(words) < minWords || len(words) > maxWords {
		return ""
	}
	if isFiller(s) {
		return ""
	}
	return s
}

func isFiller(s string) bool {
	low := strings.ToLower(strings.TrimRight(s, ".!?"))
	for _, f := range fillerPhrases {
		if low == f {
			return true
		}
	}
	return false
}
