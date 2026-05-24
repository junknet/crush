// Package agent is the core orchestration layer for Crush AI agents.
//
// It provides session-based AI agent functionality for managing
// conversations, tool execution, and message handling. It coordinates
// interactions between language models, messages, sessions, and tools while
// handling features like automatic summarization, queuing, and token
// management.
package agent

import (
	"cmp"
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/antigravity"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/memdir"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/eventbus"
	"github.com/charmbracelet/crush/internal/hooks"
	crushlog "github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/stringext"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/google/uuid"
)

const (
	DefaultSessionName = "Untitled Session"

	// Auto-summarization triggers when used >= 70% of context window
	// (i.e. remaining <= 30%). Single ratio for every window size so
	// large-window models compact at the same proportional point as small ones.
	autoSummarizeRemainingRatio = 0.30
)

var userAgent = fmt.Sprintf("Charm-Crush/%s (https://charm.land/crush)", version.Version)

//go:embed templates/title.md
var titlePrompt []byte

//go:embed templates/summary.md
var summaryPrompt []byte

// Used to remove <think> tags from generated titles.
var (
	thinkTagRegex       = regexp.MustCompile(`(?s)<think>.*?</think>`)
	orphanThinkTagRegex = regexp.MustCompile(`</?think>`)
)

type SessionAgentCall struct {
	SessionID        string
	Prompt           string
	DynamicPrefix    string
	BoostReasoning   bool
	ProviderOptions  fantasy.ProviderOptions
	Attachments      []message.Attachment
	MaxOutputTokens  int64
	Temperature      *float64
	TopP             *float64
	TopK             *int64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	NonInteractive   bool
	TraceRuntime     *agentruntime.RuntimeSession
	TaskNodeID       string
	TaskParentID     string
	TaskProfile      string
	ProviderID       string
	ProviderType     string
	ModelID          string
}

type SessionAgent interface {
	Run(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)
	SetModels(primary Model, title Model)
	SetTools(tools []fantasy.AgentTool)
	SetDeferredRegistry(reg *tools.DeferredRegistry)
	SetSystemPrompt(systemPrompt string)
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	Summarize(context.Context, string, fantasy.ProviderOptions) error
	Model() Model
}

type Model struct {
	Model        fantasy.LanguageModel
	CatwalkCfg   catwalk.Model
	ModelCfg     config.SelectedModel
	ProviderType catwalk.Type
	FlatRate     bool
}

type sessionAgent struct {
	primaryModel       *csync.Value[Model]
	titleModel         *csync.Value[Model]
	systemPromptPrefix *csync.Value[string]
	systemPrompt       *csync.Value[string]
	tools              *csync.Slice[fantasy.AgentTool]
	// deferredRegistry holds the per-agent set of tools whose schemas are
	// withheld from the model until tool_search activates them. nil for
	// agents that don't use the deferred-loading path. atomic.Pointer keeps
	// SetDeferredRegistry race-free without dragging in a separate mutex.
	deferredRegistry atomic.Pointer[tools.DeferredRegistry]
	// lastDeferredHash is the hash of deferredRegistry.DeferredNames() at
	// the end of the previous PrepareStep. When the set changes (an MCP
	// server finished connecting, tool_search just promoted entries, etc.)
	// we inject a <system-reminder> so the model notices.
	lastDeferredHash *csync.Value[string]

	isSubAgent           bool
	sessions             session.Service
	messages             message.Service
	disableAutoSummarize bool
	notify               pubsub.Publisher[notify.Notification]
	dataDir              string
	workingDir           string
	hookRunner           *hooks.Runner

	messageQueue   *csync.Map[string, []SessionAgentCall]
	activeRequests *csync.Map[string, context.CancelFunc]
	// cancelGen is incremented every time Cancel(sessionID) is called.
	// Each in-flight Run snapshots the value at start and refuses to
	// auto-resume queued / summarize follow-up work if the generation
	// has advanced — that's what makes ESC mean "stop everything" and
	// not "stop this turn, then keep going with whatever was queued".
	cancelGen *csync.Map[string, uint64]
}

type SessionAgentOptions struct {
	PrimaryModel         Model
	TitleModel           Model
	SystemPromptPrefix   string
	SystemPrompt         string
	IsSubAgent           bool
	DisableAutoSummarize bool
	Sessions             session.Service
	Messages             message.Service
	Tools                []fantasy.AgentTool
	Notify               pubsub.Publisher[notify.Notification]
	// DataDir is the per-workspace data directory (typically `.crush/`). It
	// hosts spill files for microCompact and the tool-results subtree.
	DataDir    string
	WorkingDir string
	// HookRunner, when non-nil, fires Stop hooks at the end of each turn.
	// PreToolUse/PostToolUse hooks are wired separately through the tool
	// wrappers; this field exists so the agent loop can drive turn-level
	// events without re-creating a Runner on every step.
	HookRunner *hooks.Runner
}

func NewSessionAgent(
	opts SessionAgentOptions,
) SessionAgent {
	return &sessionAgent{
		primaryModel:         csync.NewValue(opts.PrimaryModel),
		titleModel:           csync.NewValue(opts.TitleModel),
		systemPromptPrefix:   csync.NewValue(opts.SystemPromptPrefix),
		systemPrompt:         csync.NewValue(opts.SystemPrompt),
		isSubAgent:           opts.IsSubAgent,
		sessions:             opts.Sessions,
		messages:             opts.Messages,
		disableAutoSummarize: opts.DisableAutoSummarize,
		tools:                csync.NewSliceFrom(opts.Tools),
		lastDeferredHash:     csync.NewValue(""),
		notify:               opts.Notify,
		dataDir:              opts.DataDir,
		workingDir:           opts.WorkingDir,
		hookRunner:           opts.HookRunner,
		messageQueue:         csync.NewMap[string, []SessionAgentCall](),
		activeRequests:       csync.NewMap[string, context.CancelFunc](),
		cancelGen:            csync.NewMap[string, uint64](),
	}
}

func (a *sessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	if call.Prompt == "" && !message.ContainsTextAttachment(call.Attachments) {
		return nil, ErrEmptyPrompt
	}
	if call.SessionID == "" {
		return nil, ErrSessionMissing
	}

	// Queue the message if busy
	if a.IsSessionBusy(call.SessionID) {
		existing, ok := a.messageQueue.Get(call.SessionID)
		if !ok {
			existing = []SessionAgentCall{}
		}
		existing = append(existing, call)
		a.messageQueue.Set(call.SessionID, existing)
		return nil, nil
	}

	// Snapshot the cancellation generation at the start of this Run.
	// Any Cancel() call during this Run will bump it; we re-read at
	// each auto-resume checkpoint and bail out if it advanced.
	startCancelGen, _ := a.cancelGen.Get(call.SessionID)

	// Copy mutable fields under lock to avoid races with SetTools/SetModels.
	agentTools := a.tools.Copy()
	primaryModel := a.primaryModel.Get()
	systemPrompt := a.systemPrompt.Get()
	promptPrefix := a.systemPromptPrefix.Get()
	var instructions strings.Builder

	for _, server := range mcp.GetStates() {
		if server.State != mcp.StateConnected {
			continue
		}
		if s := server.Client.InitializeResult().Instructions; s != "" {
			instructions.WriteString(s)
			instructions.WriteString("\n\n")
		}
	}

	if s := instructions.String(); s != "" {
		systemPrompt += "\n\n<mcp-instructions>\n" + s + "\n</mcp-instructions>"
	}

	if len(agentTools) > 0 {
		// Add Anthropic caching to the last tool.
		agentTools[len(agentTools)-1].SetProviderOptions(a.getCacheControlOptions())
	}

	if strings.HasSuffix(call.SessionID, "-speculate") {
		wrapped := make([]fantasy.AgentTool, len(agentTools))
		for i, tool := range agentTools {
			wrapped[i] = &speculativeToolWrapper{inner: tool}
		}
		agentTools = wrapped
	} else if strings.HasSuffix(call.SessionID, "-mem-extract") {
		memoryDir := filepath.Join(a.dataDir, "projects", memdir.WorkspaceSlug(a.workingDir), "memory")
		wrapped := make([]fantasy.AgentTool, len(agentTools))
		for i, tool := range agentTools {
			wrapped[i] = &memExtractToolWrapper{inner: tool, memoryDir: memoryDir}
		}
		agentTools = wrapped
	}

	agent := fantasy.NewAgent(
		primaryModel.Model,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
		fantasy.WithUserAgent(userAgent),
	)

	sessionLock := sync.Mutex{}
	currentSession, err := a.sessions.Get(ctx, call.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	msgs, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return nil, fmt.Errorf("failed to get session messages: %w", err)
	}

	var wg sync.WaitGroup
	// Generate title if first message and the caller is interactive.
	if len(msgs) == 0 && !call.NonInteractive {
		titleCtx := ctx // Copy to avoid race with ctx reassignment below.
		wg.Go(func() {
			a.generateTitle(titleCtx, call.SessionID, call.Prompt)
		})
	}
	defer wg.Wait()

	// Cancel-equals-discard semantics: when the previous brain turn was
	// cancelled by the user (ESC), we treat that whole turn — the user
	// prompt that triggered it AND the half-formed assistant reply — as
	// if it never happened from the LLM's point of view. So:
	//   1. Do NOT modify call.Prompt here; the new prompt goes into the
	//      message DB as plain user text, which is what the up-arrow
	//      history navigator surfaces (no "[Previous turn was interrupted
	//      …]" prefix bleeding into the editor).
	//   2. preparePrompt below skips the cancelled user/assistant pair
	//      when building the fantasy.Message history sent to the LLM,
	//      so the model never sees the cancelled turn.
	// The interruptMarker constant is kept for the rare flow where a
	// caller explicitly wants the legacy "re-evaluate" hint.

	// Add the user message to the session.
	_, err = a.createUserMessage(ctx, call)
	if err != nil {
		return nil, err
	}

	// Add the session to the context.
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
	ctx = tools.WithTraceContext(ctx, call.TraceRuntime, call.TaskNodeID, call.TaskParentID, call.TaskProfile, call.ProviderID, call.ProviderType, call.ModelID)

	// Establish a turn-level trace id that threads through every observability
	// surface (slog, provider HTTP dumps, IPC dumps). session_id is the join
	// key back to the DAG trace JSONL, which already records it.
	traceID := uuid.NewString()
	ctx = crushlog.WithTraceID(ctx, traceID)
	ctx = crushlog.WithSessionID(ctx, call.SessionID)
	slog.DebugContext(ctx, "Agent run started", "sub_agent", a.isSubAgent)

	genCtx, cancel := context.WithCancel(ctx)
	a.activeRequests.Set(call.SessionID, cancel)

	if !call.NonInteractive && a.notify != nil {
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID:    call.SessionID,
			SessionTitle: currentSession.Title,
			Type:         notify.TypeAgentStarted,
		})
	}

	defer cancel()
	defer a.activeRequests.Del(call.SessionID)
	// Drain any debounced message updates before returning. message.Service
	// already flushes synchronously on terminal updates, but a defer here
	// guarantees the contract at every Run exit (success, error, panic
	// recovery upstream) without callers needing to know.
	defer func() {
		if flushErr := a.messages.FlushAll(ctx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after run", "error", flushErr)
		}
	}()

	history, files := a.preparePrompt(msgs, primaryModel.CatwalkCfg.SupportsImages, primaryModel.ProviderType, call.Attachments...)
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		roles := make([]string, 0, len(history))
		emptyCount := 0
		for _, msg := range history {
			roles = append(roles, string(msg.Role))
			if len(msg.Content) == 0 {
				emptyCount++
			}
		}
		slog.Debug(
			"Prepared agent payload",
			"session_id", call.SessionID,
			"prompt_len", len(call.Prompt),
			"history_len", len(history),
			"history_roles", roles,
			"empty_messages", emptyCount,
			"file_parts", len(files),
			"sub_agent", a.isSubAgent,
			"supports_images", primaryModel.CatwalkCfg.SupportsImages,
		)
	}

	startTime := time.Now()
	a.eventPromptSent(call.SessionID)

	var currentAssistant *message.Message
	var shouldSummarize bool
	// Don't send MaxOutputTokens if 0 — some providers (e.g. LM Studio) reject it
	var maxOutputTokens *int64
	if call.MaxOutputTokens > 0 {
		maxOutputTokens = &call.MaxOutputTokens
	}
	promptWithDyn := call.Prompt
	if call.DynamicPrefix != "" {
		promptWithDyn = call.DynamicPrefix + "\n---\n" + promptWithDyn
	}
	result, err := agent.Stream(genCtx, fantasy.AgentStreamCall{
		Prompt:           message.PromptWithTextAttachments(promptWithDyn, call.Attachments),
		Files:            files,
		Messages:         history,
		ProviderOptions:  call.ProviderOptions,
		MaxOutputTokens:  maxOutputTokens,
		TopP:             call.TopP,
		Temperature:      call.Temperature,
		PresencePenalty:  call.PresencePenalty,
		TopK:             call.TopK,
		FrequencyPenalty: call.FrequencyPenalty,
		PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = options.Messages
			for i := range prepared.Messages {
				prepared.Messages[i].ProviderOptions = nil
			}

			// Use latest tools (updated by SetTools when MCP tools change).
			prepared.Tools = a.tools.Copy()

			// Merge in the deferred registry: activated tools' real
			// schemas, plus proxy stubs for tools whose schemas the
			// model has not asked to load yet. tool_search itself is
			// already in prepared.Tools (registered up-front).
			if reg := a.deferredRegistry.Load(); reg != nil {
				prepared.Tools = mergeDeferredTools(prepared.Tools, reg)
			}

			queuedCalls, _ := a.messageQueue.Get(call.SessionID)
			a.messageQueue.Del(call.SessionID)
			for _, queued := range queuedCalls {
				userMessage, createErr := a.createUserMessage(callContext, queued)
				if createErr != nil {
					return callContext, prepared, createErr
				}
				prepared.Messages = append(prepared.Messages, userMessage.ToAIMessage()...)
			}

			// Drain any externally-published events (background-job completions,
			// monitor hits, fired cron tasks) for this session and fold them
			// into the last user message as a <task-notification> block.
			// This is in addition to the dedicated wake-up Runs the coordinator
			// kicks off — events that arrive concurrently with an in-flight
			// turn surface here on the next step so nothing is lost.
			if pending := eventbus.Default.Drain(call.SessionID); len(pending) > 0 {
				notification := renderTaskNotification(pending)
				prepared.Messages = injectTaskNotification(prepared.Messages, notification)
			}

			// If the visible deferred-tool set changed since last step,
			// surface a <system-reminder> so the model notices new
			// schemas or now-connected MCP servers. The reminder rides
			// on the latest user message; if there isn't one we append
			// a synthetic user message carrying just the reminder.
			if reg := a.deferredRegistry.Load(); reg != nil {
				prepared.Messages = a.maybeInjectDeferredReminder(prepared.Messages, reg)
			}

			prepared.Messages = a.workaroundProviderMediaLimitations(prepared.Messages, primaryModel.ProviderType)

			lastSystemRoleInx := 0
			systemMessageUpdated := false
			for i, msg := range prepared.Messages {
				// Only add cache control to the last message.
				if msg.Role == fantasy.MessageRoleSystem {
					lastSystemRoleInx = i
				} else if !systemMessageUpdated {
					prepared.Messages[lastSystemRoleInx].ProviderOptions = a.getCacheControlOptions()
					systemMessageUpdated = true
				}
				// Than add cache control to the last 2 messages.
				if i > len(prepared.Messages)-3 {
					prepared.Messages[i].ProviderOptions = a.getCacheControlOptions()
				}
			}

			if promptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(promptPrefix)}, prepared.Messages...)
			}

			var assistantMsg message.Message
			assistantMsg, err = a.messages.Create(callContext, call.SessionID, message.CreateMessageParams{
				Role:     message.Assistant,
				Parts:    []message.ContentPart{},
				Model:    primaryModel.ModelCfg.Model,
				Provider: primaryModel.ModelCfg.Provider,
			})
			if err != nil {
				return callContext, prepared, err
			}
			callContext = context.WithValue(callContext, tools.MessageIDContextKey, assistantMsg.ID)
			callContext = context.WithValue(callContext, tools.SupportsImagesContextKey, primaryModel.CatwalkCfg.SupportsImages)
			callContext = context.WithValue(callContext, tools.ModelNameContextKey, primaryModel.CatwalkCfg.Name)
			currentAssistant = &assistantMsg
			currentAssistant.SetBoostedReasoning(call.BoostReasoning)
			return callContext, prepared, err
		},
		OnReasoningStart: func(id string, reasoning fantasy.ReasoningContent) error {
			currentAssistant.AppendReasoningContent(reasoning.Text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningDelta: func(id string, text string) error {
			currentAssistant.AppendReasoningContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
			// handle anthropic signature
			if anthropicData, ok := reasoning.ProviderMetadata[anthropic.Name]; ok {
				if reasoning, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok {
					currentAssistant.AppendReasoningSignature(reasoning.Signature)
				}
			}
			if googleData, ok := reasoning.ProviderMetadata[google.Name]; ok {
				if reasoning, ok := googleData.(*google.ReasoningMetadata); ok {
					currentAssistant.AppendThoughtSignature(reasoning.Signature, reasoning.ToolID)
				}
			}
			if openaiData, ok := reasoning.ProviderMetadata[openai.Name]; ok {
				if reasoning, ok := openaiData.(*openai.ResponsesReasoningMetadata); ok {
					currentAssistant.SetReasoningResponsesData(reasoning)
				}
			}
			currentAssistant.FinishThinking()
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnTextDelta: func(id string, text string) error {
			// Strip leading newline from initial text content. This is is
			// particularly important in non-interactive mode where leading
			// newlines are very visible.
			if len(currentAssistant.Parts) == 0 {
				text = strings.TrimPrefix(text, "\n")
			}

			currentAssistant.AppendContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnToolInputStart: func(id string, toolName string) error {
			toolCall := message.ToolCall{
				ID:               id,
				Name:             toolName,
				ProviderExecuted: false,
				Finished:         false,
			}
			currentAssistant.AddToolCall(toolCall)
			// Use parent ctx instead of genCtx to ensure the update succeeds
			// even if the request is canceled mid-stream
			return a.messages.Update(ctx, *currentAssistant)
		},
		OnRetry: func(err *fantasy.ProviderError, delay time.Duration) {
			slog.Warn("Provider request failed, retrying", providerRetryLogFields(err, delay)...)
		},
		OnToolCall: func(tc fantasy.ToolCallContent) error {
			toolCall := message.ToolCall{
				ID:               tc.ToolCallID,
				Name:             tc.ToolName,
				Input:            tc.Input,
				ProviderExecuted: false,
				Finished:         true,
			}
			// Gemini 3 (antigravity) attaches a thought_signature to each
			// functionCall and rejects replayed tool-call history that lacks
			// it. Persist it so the next turn can round-trip it.
			if po := antigravity.GetProviderOptions(fantasy.ProviderOptions(tc.ProviderMetadata)); po != nil {
				toolCall.ThoughtSignature = po.ThoughtSignature
			}
			currentAssistant.AddToolCall(toolCall)
			// Use parent ctx instead of genCtx to ensure the update succeeds
			// even if the request is canceled mid-stream
			return a.messages.Update(ctx, *currentAssistant)
		},
		OnToolResult: func(result fantasy.ToolResultContent) error {
			toolResult := a.convertToToolResult(result)
			// Use parent ctx instead of genCtx to ensure the message is created
			// even if the request is canceled mid-stream
			_, createMsgErr := a.messages.Create(ctx, currentAssistant.SessionID, message.CreateMessageParams{
				Role: message.Tool,
				Parts: []message.ContentPart{
					toolResult,
				},
			})
			return createMsgErr
		},
		OnStepFinish: func(stepResult fantasy.StepResult) error {
			finishReason := message.FinishReasonUnknown
			switch stepResult.FinishReason {
			case fantasy.FinishReasonLength:
				finishReason = message.FinishReasonMaxTokens
			case fantasy.FinishReasonStop:
				finishReason = message.FinishReasonEndTurn
			case fantasy.FinishReasonToolCalls:
				finishReason = message.FinishReasonToolUse
			}
			// If a tool result halted the turn (e.g. a hook halt or a
			// permission denial), the step ends on FinishReasonToolCalls but
			// the model will not be called again. Treat it as the end of the
			// turn so the UI can render the assistant footer.
			if finishReason == message.FinishReasonToolUse {
				for _, tr := range stepResult.Content.ToolResults() {
					if tr.StopTurn {
						finishReason = message.FinishReasonEndTurn
						break
					}
				}
			}
			// A reasoning model can return finish_reason=length having spent
			// its whole output budget on reasoning, emitting no text and no
			// tool call. Without this guard the turn ends "successfully" with
			// an empty footer, so the user keeps prompting into a dead end
			// (typically a context window that is full but mis-declared larger
			// than it really is). Surface it as an explicit error instead.
			if finishReason == message.FinishReasonMaxTokens &&
				stepResult.Content.Text() == "" &&
				len(stepResult.Content.ToolCalls()) == 0 {
				currentAssistant.AddFinish(
					message.FinishReasonError,
					"Response truncated before any output",
					"The model hit its output-token limit while reasoning and produced no text or tool call. The context window is likely full — run /compact or start a new session, and verify the provider's context_window matches the backend's real limit.",
				)
			} else {
				currentAssistant.AddFinish(finishReason, "", "")
			}
			sessionLock.Lock()
			defer sessionLock.Unlock()

			updatedSession, getSessionErr := a.sessions.Get(ctx, call.SessionID)
			if getSessionErr != nil {
				return getSessionErr
			}
			a.updateSessionUsage(primaryModel, &updatedSession, stepResult.Usage, a.openrouterCost(stepResult.ProviderMetadata))
			_, sessionErr := a.sessions.Save(ctx, updatedSession)
			if sessionErr != nil {
				return sessionErr
			}
			currentSession = updatedSession
			if updateErr := a.messages.Update(genCtx, *currentAssistant); updateErr != nil {
				return updateErr
			}
			// Free token budget without an LLM call by clearing aged-out,
			// oversized tool results to local spill files. Errors inside
			// microCompactStep are logged and swallowed; they must not
			// abort an otherwise successful turn.
			a.microCompactStep(genCtx, call.SessionID)
			// G7 Stop hook: fire once per terminal step. EndTurn and
			// MaxTokens are obvious turn boundaries; ToolUse is in-flight
			// and skipped. Sub-agents inherit the parent's hookRunner only
			// when wired; nil-receiver path is a no-op.
			if a.hookRunner != nil && (finishReason == message.FinishReasonEndTurn || finishReason == message.FinishReasonMaxTokens) {
				a.fireStopHook(genCtx, call.SessionID, finishReason, stepResult)
			}
			return nil
		},
		StopWhen: []fantasy.StopCondition{
			func(_ []fantasy.StepResult) bool {
				cw := int64(primaryModel.CatwalkCfg.ContextWindow)
				// If context window is unknown (0), skip auto-summarize
				// to avoid immediately truncating custom/local models.
				if cw == 0 {
					return false
				}
				tokens := currentSession.CompletionTokens + currentSession.PromptTokens
				remaining := cw - tokens
				threshold := int64(float64(cw) * autoSummarizeRemainingRatio)
				if (remaining <= threshold) && !a.disableAutoSummarize {
					shouldSummarize = true
					return true
				}
				return false
			},
			func(steps []fantasy.StepResult) bool {
				return hasRepeatedToolCalls(steps, loopDetectionWindowSize, loopDetectionMaxRepeats)
			},
		},
	})

	a.eventPromptResponded(call.SessionID, time.Since(startTime).Truncate(time.Second))

	if err != nil {
		isHyper := primaryModel.ModelCfg.Provider == hyper.Name
		isCancelErr := errors.Is(err, context.Canceled)
		if currentAssistant == nil {
			return result, err
		}
		// Ensure we finish thinking on error to close the reasoning state.
		currentAssistant.FinishThinking()
		toolCalls := currentAssistant.ToolCalls()
		// INFO: we use the parent context here because the genCtx has been cancelled.
		msgs, createErr := a.messages.List(ctx, currentAssistant.SessionID)
		if createErr != nil {
			return nil, createErr
		}
		for _, tc := range toolCalls {
			if !tc.Finished {
				tc.Finished = true
				tc.Input = "{}"
				currentAssistant.AddToolCall(tc)
				updateErr := a.messages.Update(ctx, *currentAssistant)
				if updateErr != nil {
					return nil, updateErr
				}
			}

			found := false
			for _, msg := range msgs {
				if msg.Role == message.Tool {
					for _, tr := range msg.ToolResults() {
						if tr.ToolCallID == tc.ID {
							found = true
							break
						}
					}
				}
				if found {
					break
				}
			}
			if found {
				continue
			}
			content := "There was an error while executing the tool"
			if isCancelErr {
				content = "Error: user cancelled assistant tool calling"
			}
			toolResult := message.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    content,
				IsError:    true,
			}
			_, createErr = a.messages.Create(ctx, currentAssistant.SessionID, message.CreateMessageParams{
				Role: message.Tool,
				Parts: []message.ContentPart{
					toolResult,
				},
			})
			if createErr != nil {
				return nil, createErr
			}
		}
		var fantasyErr *fantasy.Error
		var providerErr *fantasy.ProviderError
		const defaultTitle = "Provider Error"
		linkStyle := lipgloss.NewStyle().Foreground(charmtone.Guac).Underline(true)
		if isCancelErr {
			currentAssistant.AddFinish(message.FinishReasonCanceled, "User canceled request", "")
		} else if isHyper && errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
			currentAssistant.AddFinish(message.FinishReasonError, "Unauthorized", `Please re-authenticate with Hyper. You can also run "crush auth" to re-authenticate.`)
			if a.notify != nil {
				a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
					SessionID:    call.SessionID,
					SessionTitle: currentSession.Title,
					Type:         notify.TypeReAuthenticate,
					ProviderID:   primaryModel.ModelCfg.Provider,
				})
			}
		} else if isHyper && errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusPaymentRequired {
			url := hyper.BaseURL()
			link := linkStyle.Hyperlink(url, "id=hyper").Render(url)
			currentAssistant.AddFinish(message.FinishReasonError, "No credits", "You're out of credits. Add more at "+link)
		} else if errors.As(err, &providerErr) {
			if providerErr.Message == "The requested model is not supported." {
				url := "https://github.com/settings/copilot/features"
				link := linkStyle.Hyperlink(url, "id=copilot").Render(url)
				currentAssistant.AddFinish(
					message.FinishReasonError,
					"Copilot model not enabled",
					fmt.Sprintf("%q is not enabled in Copilot. Go to the following page to enable it. Then, wait 5 minutes before trying again. %s", primaryModel.CatwalkCfg.Name, link),
				)
			} else {
				currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(providerErr.Title), defaultTitle), providerErr.Message)
			}
		} else if errors.As(err, &fantasyErr) {
			currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(fantasyErr.Title), defaultTitle), fantasyErr.Message)
		} else {
			currentAssistant.AddFinish(message.FinishReasonError, defaultTitle, err.Error())
		}
		// Note: we use the parent context here because the genCtx has been
		// cancelled.
		updateErr := a.messages.Update(ctx, *currentAssistant)
		if updateErr != nil {
			return nil, updateErr
		}
		return nil, err
	}

	if shouldSummarize {
		a.activeRequests.Del(call.SessionID)
		if summarizeErr := a.summarizeSession(genCtx, call.SessionID, call.ProviderOptions, true); summarizeErr != nil {
			slog.Warn("Automatic summarization failed", "session_id", call.SessionID, "error", summarizeErr)
		}
		// If the agent wasn't done...
		if len(currentAssistant.ToolCalls()) > 0 {
			// Skip the re-queue if the user cancelled during this run —
			// the "previous session was interrupted because it got too
			// long" follow-up shouldn't fire when the actual interrupt
			// was a user ESC, not a context overflow.
			if curGen, _ := a.cancelGen.Get(call.SessionID); curGen == startCancelGen {
				existing, ok := a.messageQueue.Get(call.SessionID)
				if !ok {
					existing = []SessionAgentCall{}
				}
				call.Prompt = fmt.Sprintf("The previous session was interrupted because it got too long, the initial user request was: `%s`", call.Prompt)
				existing = append(existing, call)
				a.messageQueue.Set(call.SessionID, existing)
			}
		}
	}

	// Release active request before publishing the notification.
	// TUI handlers poll IsSessionBusy() and only re-evaluate when a
	// tea.Msg arrives, so the cleanup must precede the notify or
	// subscribers see stale busy state at the moment of receipt.
	a.activeRequests.Del(call.SessionID)
	cancel()

	// Send notification that agent has finished its turn (skip for
	// nested/non-interactive sessions).
	if !call.NonInteractive && a.notify != nil {
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID:    call.SessionID,
			SessionTitle: currentSession.Title,
			Type:         notify.TypeAgentFinished,
		})
	}

	// User-cancellation gate: if Cancel was called at any point during
	// this Run, the cancel generation is now ahead of the snapshot. In
	// that case ESC means "stop everything" — drop any queued prompts
	// and return without spawning a follow-up Run. Without this gate
	// a prompt that landed in the queue moments before ESC would
	// silently auto-execute and surface as a fresh "Working..."
	// spinner right after "Canceled".
	if curGen, _ := a.cancelGen.Get(call.SessionID); curGen != startCancelGen {
		a.messageQueue.Del(call.SessionID)
		return result, err
	}

	queuedMessages, ok := a.messageQueue.Get(call.SessionID)
	if !ok || len(queuedMessages) == 0 {
		return result, err
	}
	// There are queued messages restart the loop.
	firstQueuedMessage := queuedMessages[0]
	a.messageQueue.Set(call.SessionID, queuedMessages[1:])
	return a.Run(ctx, firstQueuedMessage)
}

func (a *sessionAgent) Summarize(ctx context.Context, sessionID string, opts fantasy.ProviderOptions) error {
	return a.summarizeSession(ctx, sessionID, opts, false)
}

func (a *sessionAgent) summarizeSession(ctx context.Context, sessionID string, opts fantasy.ProviderOptions, bestEffort bool) error {
	if a.IsSessionBusy(sessionID) {
		return ErrSessionBusy
	}

	// Snapshot cancel generation. A Cancel during summarize must
	// block the post-summarize queue drain (line ~838) for the same
	// reason as the post-Run drain — ESC means stop, not "stop this
	// step then start the next queued prompt".
	startCancelGen, _ := a.cancelGen.Get(sessionID)

	// Copy mutable fields under lock to avoid races with SetModels.
	primaryModel := a.primaryModel.Get()
	systemPromptPrefix := a.systemPromptPrefix.Get()

	currentSession, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}
	msgs, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		// Nothing to summarize.
		return nil
	}

	summaryOutputTokens := summarizeOutputTokenBudget(primaryModel.CatwalkCfg)
	summaryInputBudget := summarizeInputTokenBudget(primaryModel.CatwalkCfg)
	summarySourceMessages, contextWasTrimmed, estimatedInputTokens := selectSummaryMessagesForBudget(
		msgs,
		primaryModel.CatwalkCfg.SupportsImages,
		summaryInputBudget,
	)
	if contextWasTrimmed {
		slog.Debug(
			"Trimmed summary input to fit the model window",
			"session_id", sessionID,
			"original_messages", len(msgs),
			"trimmed_messages", len(summarySourceMessages),
			"estimated_input_tokens", estimatedInputTokens,
			"input_budget_tokens", summaryInputBudget,
			"output_budget_tokens", summaryOutputTokens,
		)
		// G5 sessionMemoryCompact: messages that fell outside the summary
		// input budget will never re-enter the LLM's view. Spill them to
		// memory/sessions/ so the operator (or a future session) can
		// recover the original content. Failure path swallowed inside
		// backupDiscardedMessages — summarize must not block on disk.
		if discardCount := len(msgs) - len(summarySourceMessages); discardCount > 0 {
			a.backupDiscardedMessages(ctx, sessionID, msgs[:discardCount], a.workingDir)
		}
	}

	genCtx, cancel := context.WithCancel(ctx)
	a.activeRequests.Set(sessionID, cancel)
	defer a.activeRequests.Del(sessionID)
	defer cancel()
	defer func() {
		if flushErr := a.messages.FlushAll(ctx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after summarize", "error", flushErr)
		}
	}()

	agent := fantasy.NewAgent(
		primaryModel.Model,
		fantasy.WithSystemPrompt(string(summaryPrompt)),
		fantasy.WithUserAgent(userAgent),
	)
	summaryMessage, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:             message.Assistant,
		Model:            primaryModel.Model.Model(),
		Provider:         primaryModel.Model.Provider(),
		IsSummaryMessage: true,
	})
	if err != nil {
		return err
	}

	summaryPromptText := buildSummaryPromptWithPartialContext(currentSession.Todos, contextWasTrimmed)

	summaryMessages := summarySourceMessages
	var resp *fantasy.AgentResult
	for attempt := 0; attempt < summaryRetryLimit; attempt++ {
		summaryMessage.Parts = nil
		if updateErr := a.messages.Update(ctx, summaryMessage); updateErr != nil {
			return updateErr
		}

		aiMsgs, _ := a.preparePrompt(summaryMessages, primaryModel.CatwalkCfg.SupportsImages, primaryModel.ProviderType)
		resp, err = agent.Stream(genCtx, fantasy.AgentStreamCall{
			Prompt:          summaryPromptText,
			Messages:        aiMsgs,
			ProviderOptions: opts,
			MaxOutputTokens: &summaryOutputTokens,
			PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
				prepared.Messages = options.Messages
				if systemPromptPrefix != "" {
					prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(systemPromptPrefix)}, prepared.Messages...)
				}
				return callContext, prepared, nil
			},
			OnReasoningDelta: func(id string, text string) error {
				summaryMessage.AppendReasoningContent(text)
				return a.messages.Update(genCtx, summaryMessage)
			},
			OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
				// Handle anthropic signature.
				if anthropicData, ok := reasoning.ProviderMetadata["anthropic"]; ok {
					if signature, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok && signature.Signature != "" {
						summaryMessage.AppendReasoningSignature(signature.Signature)
					}
				}
				summaryMessage.FinishThinking()
				return a.messages.Update(genCtx, summaryMessage)
			},
			OnTextDelta: func(id, text string) error {
				summaryMessage.AppendContent(text)
				return a.messages.Update(genCtx, summaryMessage)
			},
		})
		if err == nil {
			summaryMessage.AddFinish(message.FinishReasonEndTurn, "", "")
			err = a.messages.Update(genCtx, summaryMessage)
			if err != nil {
				return err
			}
			break
		}

		if errors.Is(err, context.Canceled) {
			// User cancelled summarize we need to remove the summary message.
			deleteErr := a.messages.Delete(ctx, summaryMessage.ID)
			return deleteErr
		}

		if !isSummaryContextTooLargeError(err) || len(summaryMessages) <= 1 {
			break
		}

		dropCount := summaryRetryDropCount(len(summaryMessages))
		slog.Debug(
			"Summary input still exceeded the model window; retrying with fewer messages",
			"session_id", sessionID,
			"attempt", attempt+1,
			"dropped_messages", dropCount,
			"remaining_messages", len(summaryMessages)-dropCount,
		)
		summaryMessages = summaryMessages[dropCount:]
		summaryPromptText = buildSummaryPromptWithPartialContext(currentSession.Todos, true)
	}
	if err != nil {
		// Mark the summary message as finished with an error so the UI
		// stops spinning.
		summaryMessage.AddFinish(message.FinishReasonError, "Summarization Error", err.Error())
		if updateErr := a.messages.Update(ctx, summaryMessage); updateErr != nil {
			return updateErr
		}
		if bestEffort {
			if deleteErr := a.messages.Delete(ctx, summaryMessage.ID); deleteErr != nil {
				return deleteErr
			}
		}
		return err
	}

	var openrouterCost *float64
	for _, step := range resp.Steps {
		stepCost := a.openrouterCost(step.ProviderMetadata)
		if stepCost != nil {
			newCost := *stepCost
			if openrouterCost != nil {
				newCost += *openrouterCost
			}
			openrouterCost = &newCost
		}
	}

	a.updateSessionUsage(primaryModel, &currentSession, resp.TotalUsage, openrouterCost)

	// Just in case, get just the last usage info.
	usage := resp.Response.Usage
	currentSession.SummaryMessageID = summaryMessage.ID
	currentSession.CompletionTokens = usage.OutputTokens
	currentSession.PromptTokens = 0
	_, err = a.sessions.Save(genCtx, currentSession)
	if err != nil {
		return err
	}

	// Release the active request before processing queued messages so that
	// Run() does not see the session as busy.
	a.activeRequests.Del(sessionID)
	cancel()

	// User-cancellation gate (same semantics as in Run).
	if curGen, _ := a.cancelGen.Get(sessionID); curGen != startCancelGen {
		a.messageQueue.Del(sessionID)
		return nil
	}

	// Process any messages that were queued while summarizing.
	queuedMessages, ok := a.messageQueue.Get(sessionID)
	if !ok || len(queuedMessages) == 0 {
		return nil
	}
	firstQueuedMessage := queuedMessages[0]
	a.messageQueue.Set(sessionID, queuedMessages[1:])
	_, qErr := a.Run(ctx, firstQueuedMessage)
	return qErr
}

func (a *sessionAgent) getCacheControlOptions() fantasy.ProviderOptions {
	if t, _ := strconv.ParseBool(os.Getenv("CRUSH_DISABLE_ANTHROPIC_CACHE")); t {
		return fantasy.ProviderOptions{}
	}
	return fantasy.ProviderOptions{
		anthropic.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		bedrock.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		vercel.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
	}
}

const interruptMarker = "[Previous turn was interrupted by user — re-evaluate before continuing]"

// wasPreviousTurnCancelled reports whether the most recent assistant
// message in msgs finished with FinishReasonCanceled. Used by L2 to
// signal brain that the user pressed ESC before sending this message.
func wasPreviousTurnCancelled(msgs []message.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != message.Assistant {
			continue
		}
		return m.FinishReason() == message.FinishReasonCanceled
	}
	return false
}

func (a *sessionAgent) createUserMessage(ctx context.Context, call SessionAgentCall) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: call.Prompt}}
	var attachmentParts []message.ContentPart
	for _, attachment := range call.Attachments {
		attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
	}
	parts = append(parts, attachmentParts...)
	msg, err := a.messages.Create(ctx, call.SessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
	if err != nil {
		return message.Message{}, fmt.Errorf("failed to create user message: %w", err)
	}
	return msg, nil
}

func (a *sessionAgent) preparePrompt(msgs []message.Message, supportsImages bool, providerType catwalk.Type, attachments ...message.Attachment) ([]fantasy.Message, []fantasy.FilePart) {
	var history []fantasy.Message
	if !a.isSubAgent {
		history = append(history, fantasy.NewUserMessage(
			fmt.Sprintf(
				"<system_reminder>%s</system_reminder>",
				`This is a reminder that your todo list is currently empty. DO NOT mention this to the user explicitly because they are already aware.
If you are working on tasks that would benefit from a todo list please use the "todos" tool to create one.
If not, please feel free to ignore. Again do not mention this message to the user.`,
			),
		))
	}
	// Cancel-equals-discard: a cancelled assistant message AND the user
	// message that triggered it are both excluded from the LLM history.
	// This makes ESC behave like "undo this turn" instead of "interrupt
	// then re-evaluate". Tool messages whose tool_use_id points back into
	// the cancelled assistant become orphans and are handled by the
	// existing filterOrphanedToolResults below.
	skipUserIdx := make(map[int]struct{})
	skipAssistantIdx := make(map[int]struct{})
	for i, m := range msgs {
		if m.Role != message.Assistant || m.FinishReason() != message.FinishReasonCanceled {
			continue
		}
		skipAssistantIdx[i] = struct{}{}
		// Walk backwards to the nearest user message that drove this
		// cancelled turn; tool messages in between are also part of the
		// cancelled turn but are naturally filtered later by orphan
		// detection once their assistant disappears.
		for j := i - 1; j >= 0; j-- {
			if msgs[j].Role == message.User {
				skipUserIdx[j] = struct{}{}
				break
			}
			if msgs[j].Role == message.Assistant {
				break
			}
		}
	}

	// Collect all tool call IDs present in assistant messages and all tool
	// result IDs present in tool messages. This lets us detect both orphaned
	// tool results (result without a call) and orphaned tool calls (call
	// without a result). Cancelled assistant messages are excluded so that
	// any tool_use ids they declared are treated as missing — their tool
	// results will be dropped as orphans below.
	knownToolCallIDs := make(map[string]struct{})
	knownToolResultIDs := make(map[string]struct{})
	for i, m := range msgs {
		switch m.Role {
		case message.Assistant:
			if _, skip := skipAssistantIdx[i]; skip {
				continue
			}
			for _, tc := range m.ToolCalls() {
				knownToolCallIDs[tc.ID] = struct{}{}
			}
		case message.Tool:
			for _, tr := range m.ToolResults() {
				knownToolResultIDs[tr.ToolCallID] = struct{}{}
			}
		}
	}

	for i, m := range msgs {
		if len(m.Parts) == 0 {
			continue
		}
		// Cancel-equals-discard: skip the cancelled assistant and its
		// triggering user message entirely.
		if _, skip := skipAssistantIdx[i]; skip {
			continue
		}
		if _, skip := skipUserIdx[i]; skip {
			continue
		}
		// Assistant message without content or tool calls (cancelled before it returned anything).
		if m.Role == message.Assistant && len(m.ToolCalls()) == 0 && m.Content().Text == "" && m.ReasoningContent().String() == "" {
			continue
		}
		if m.Role == message.Tool {
			if msg, ok := filterOrphanedToolResults(m, knownToolCallIDs); ok {
				if providerType == catwalk.Type(catwalk.InferenceProviderAnthropic) || providerType == catwalk.Type(catwalk.InferenceProviderBedrock) {
					msg = ensureAnthropicToolResultVisibility(msg)
				}
				history = append(history, msg)
			}
			continue
		}
		aiMsgs := m.ToAIMessage()
		if !supportsImages {
			for i := range aiMsgs {
				if aiMsgs[i].Role == fantasy.MessageRoleUser {
					aiMsgs[i].Content = filterFileParts(aiMsgs[i].Content)
				}
			}
		}
		if len(aiMsgs) == 0 || len(aiMsgs[0].Content) == 0 {
			continue
		}
		history = append(history, aiMsgs...)

		if m.Role == message.Assistant {
			if msg, ok := syntheticToolResultsForOrphanedCalls(m, knownToolResultIDs); ok {
				history = append(history, msg)
			}
		}
	}

	var files []fantasy.FilePart
	for _, attachment := range attachments {
		if attachment.IsText() {
			continue
		}
		files = append(files, fantasy.FilePart{
			Filename:  attachment.FileName,
			Data:      attachment.Content,
			MediaType: attachment.MimeType,
		})
	}

	return history, files
}

func ensureAnthropicToolResultVisibility(msg fantasy.Message) fantasy.Message {
	if msg.Role != fantasy.MessageRoleTool {
		return msg
	}
	for _, part := range msg.Content {
		switch part.GetType() {
		case fantasy.ContentTypeText, fantasy.ContentTypeFile:
			return msg
		}
	}
	msg.Content = append(msg.Content, fantasy.TextPart{Text: anthropicToolResultFallbackText})
	return msg
}

const anthropicToolResultFallbackText = "Tool result available."

// filterFileParts removes fantasy.FilePart entries from a slice of message
// parts. Used to strip image attachments from historical user messages when
// the current model does not support them.
func filterFileParts(parts []fantasy.MessagePart) []fantasy.MessagePart {
	filtered := make([]fantasy.MessagePart, 0, len(parts))
	for _, part := range parts {
		if _, ok := fantasy.AsMessagePart[fantasy.FilePart](part); ok {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

// filterOrphanedToolResults converts a tool message to a fantasy.Message,
// dropping any tool result parts whose tool_call_id has no matching tool call
// in the known set. An orphaned result causes API validation to fail on every
// subsequent turn, permanently locking the session. Returns the filtered
// message and true if at least one valid part remains.
func filterOrphanedToolResults(m message.Message, knownToolCallIDs map[string]struct{}) (fantasy.Message, bool) {
	aiMsgs := m.ToAIMessage()
	if len(aiMsgs) == 0 {
		return fantasy.Message{}, false
	}
	var validParts []fantasy.MessagePart
	for _, part := range aiMsgs[0].Content {
		tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
		if !ok {
			validParts = append(validParts, part)
			continue
		}
		if _, known := knownToolCallIDs[tr.ToolCallID]; known {
			validParts = append(validParts, part)
		} else {
			slog.Warn(
				"Dropping orphaned tool result with no matching tool call",
				"tool_call_id", tr.ToolCallID,
			)
		}
	}
	if len(validParts) == 0 {
		return fantasy.Message{}, false
	}
	msg := aiMsgs[0]
	msg.Content = validParts
	return msg, true
}

// syntheticToolResultsForOrphanedCalls returns a tool message containing
// synthetic tool results for any tool calls in the assistant message that
// have no matching result in knownToolResultIDs. LLM APIs require every
// tool_use to be immediately followed by a tool_result; an interrupted
// session can leave orphaned tool_use blocks that permanently lock the
// conversation. Returns the message and true if any synthetic results were
// produced.
func syntheticToolResultsForOrphanedCalls(m message.Message, knownToolResultIDs map[string]struct{}) (fantasy.Message, bool) {
	var syntheticParts []fantasy.MessagePart
	for _, tc := range m.ToolCalls() {
		if _, hasResult := knownToolResultIDs[tc.ID]; hasResult {
			continue
		}
		slog.Warn(
			"Injecting synthetic tool result for orphaned tool call",
			"tool_call_id", tc.ID,
			"tool_name", tc.Name,
		)
		syntheticParts = append(syntheticParts, fantasy.ToolResultPart{
			ToolCallID: tc.ID,
			Output: fantasy.ToolResultOutputContentError{
				Error: errors.New("tool call was interrupted and did not produce a result, you may retry this call if the result is still needed"),
			},
		})
	}
	if len(syntheticParts) == 0 {
		return fantasy.Message{}, false
	}
	return fantasy.Message{
		Role:    fantasy.MessageRoleTool,
		Content: syntheticParts,
	}, true
}

func (a *sessionAgent) getSessionMessages(ctx context.Context, session session.Session) ([]message.Message, error) {
	msgs, err := a.messages.List(ctx, session.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}

	if session.SummaryMessageID != "" {
		summaryMsgIndex := -1
		for i, msg := range msgs {
			if msg.ID == session.SummaryMessageID {
				summaryMsgIndex = i
				break
			}
		}
		if summaryMsgIndex != -1 {
			msgs = msgs[summaryMsgIndex:]
			msgs[0].Role = message.User
		}
	}
	return msgs, nil
}

// generateTitle generates a session titled based on the initial prompt.
func (a *sessionAgent) generateTitle(ctx context.Context, sessionID string, userPrompt string) {
	if userPrompt == "" {
		return
	}

	titleModel := a.titleModel.Get()
	primaryModel := a.primaryModel.Get()
	systemPromptPrefix := a.systemPromptPrefix.Get()

	var maxOutputTokens int64 = 40
	if titleModel.CatwalkCfg.CanReason {
		maxOutputTokens = titleModel.CatwalkCfg.DefaultMaxTokens
	}

	newAgent := func(m fantasy.LanguageModel, p []byte, tok int64) fantasy.Agent {
		return fantasy.NewAgent(
			m,
			fantasy.WithSystemPrompt(string(p)+"\n /no_think"),
			fantasy.WithMaxOutputTokens(tok),
			fantasy.WithUserAgent(userAgent),
		)
	}

	streamCall := fantasy.AgentStreamCall{
		Prompt: fmt.Sprintf("Generate a concise title for the following content:\n\n%s\n <think>\n\n</think>", userPrompt),
		PrepareStep: func(callCtx context.Context, opts fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = opts.Messages
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{
					fantasy.NewSystemMessage(systemPromptPrefix),
				}, prepared.Messages...)
			}
			return callCtx, prepared, nil
		},
	}

	// Use the title model first because title generation is utility work.
	model := titleModel
	agent := newAgent(model.Model, titlePrompt, maxOutputTokens)
	resp, err := agent.Stream(ctx, streamCall)
	if err == nil {
		slog.Debug("Generated title with title model")
	} else {
		slog.Error("Error generating title with title model; trying primary model", "err", err)
		model = primaryModel
		agent = newAgent(model.Model, titlePrompt, maxOutputTokens)
		resp, err = agent.Stream(ctx, streamCall)
		if err == nil {
			slog.Debug("Generated title with primary model")
		} else {
			// The primary model did not work either. Use the default
			// session name and return.
			slog.Error("Error generating title with primary model", "err", err)
			saveErr := a.sessions.Rename(ctx, sessionID, DefaultSessionName)
			if saveErr != nil {
				slog.Error("Failed to save session title", "error", saveErr)
			}
			return
		}
	}

	if resp == nil {
		// Actually, we didn't get a response so we can't. Use the default
		// session name and return.
		slog.Error("Response is nil; can't generate title")
		saveErr := a.sessions.Rename(ctx, sessionID, DefaultSessionName)
		if saveErr != nil {
			slog.Error("Failed to save session title", "error", saveErr)
		}
		return
	}

	// Clean up title.
	var title string
	title = strings.ReplaceAll(resp.Response.Content.Text(), "\n", " ")

	// Remove thinking tags if present.
	title = thinkTagRegex.ReplaceAllString(title, "")
	title = orphanThinkTagRegex.ReplaceAllString(title, "")

	title = strings.TrimSpace(title)
	title = cmp.Or(title, DefaultSessionName)

	// Calculate usage and cost.
	var openrouterCost *float64
	for _, step := range resp.Steps {
		stepCost := a.openrouterCost(step.ProviderMetadata)
		if stepCost != nil {
			newCost := *stepCost
			if openrouterCost != nil {
				newCost += *openrouterCost
			}
			openrouterCost = &newCost
		}
	}

	modelConfig := model.CatwalkCfg
	cost := modelConfig.CostPer1MInCached/1e6*float64(resp.TotalUsage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(resp.TotalUsage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(resp.TotalUsage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(resp.TotalUsage.OutputTokens)

	// Use override cost if available (e.g., from OpenRouter).
	if openrouterCost != nil {
		cost = *openrouterCost
	}

	// Skip cost accumulation
	if model.FlatRate {
		cost = 0
	}

	promptTokens := resp.TotalUsage.InputTokens + resp.TotalUsage.CacheCreationTokens
	completionTokens := resp.TotalUsage.OutputTokens

	// Atomically update only title and usage fields to avoid overriding other
	// concurrent session updates.
	saveErr := a.sessions.UpdateTitleAndUsage(ctx, sessionID, title, promptTokens, completionTokens, cost)
	if saveErr != nil {
		slog.Error("Failed to save session title and usage", "error", saveErr)
		return
	}
}

func (a *sessionAgent) openrouterCost(metadata fantasy.ProviderMetadata) *float64 {
	openrouterMetadata, ok := metadata[openrouter.Name]
	if !ok {
		return nil
	}

	opts, ok := openrouterMetadata.(*openrouter.ProviderMetadata)
	if !ok {
		return nil
	}
	return &opts.Usage.Cost
}

func (a *sessionAgent) updateSessionUsage(model Model, session *session.Session, usage fantasy.Usage, overrideCost *float64) {
	modelConfig := model.CatwalkCfg
	cost := modelConfig.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(usage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(usage.OutputTokens)

	a.eventTokensUsed(session.ID, model, usage, cost)

	// Use override cost if available (e.g., from OpenRouter).
	if overrideCost != nil {
		cost = *overrideCost
	}

	// Skip cost accumulation
	if model.FlatRate {
		cost = 0
	}

	session.Cost += cost
	session.CompletionTokens = usage.OutputTokens
	session.PromptTokens = usage.InputTokens + usage.CacheReadTokens
}

func (a *sessionAgent) Cancel(sessionID string) {
	// Bump the cancel generation BEFORE cancelling the ctx. The Run
	// goroutine compares against the snapshot it captured at start; if
	// it sees an advanced value at any auto-resume checkpoint
	// (post-Stream queue drain, post-Summarize queue drain) it bails
	// out. Bumping first closes the race where the goroutine unwinds
	// and dequeues a follow-up prompt before we get to increment.
	prev, _ := a.cancelGen.Get(sessionID)
	a.cancelGen.Set(sessionID, prev+1)

	// Cancel regular requests. Don't use Take() here - we need the entry to
	// remain in activeRequests so IsBusy() returns true until the goroutine
	// fully completes (including error handling that may access the DB).
	// The defer in processRequest will clean up the entry.
	if cancel, ok := a.activeRequests.Get(sessionID); ok && cancel != nil {
		slog.Debug("Request cancellation initiated", "session_id", sessionID)
		cancel()
	}

	// Also check for summarize requests.
	if cancel, ok := a.activeRequests.Get(sessionID + "-summarize"); ok && cancel != nil {
		slog.Debug("Summarize cancellation initiated", "session_id", sessionID)
		cancel()
	}

	if a.QueuedPrompts(sessionID) > 0 {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.messageQueue.Del(sessionID)
	}
}

func (a *sessionAgent) ClearQueue(sessionID string) {
	if a.QueuedPrompts(sessionID) > 0 {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.messageQueue.Del(sessionID)
	}
}

func (a *sessionAgent) CancelAll() {
	if !a.IsBusy() {
		return
	}
	for key := range a.activeRequests.Seq2() {
		a.Cancel(key) // key is sessionID
	}

	timeout := time.After(5 * time.Second)
	for a.IsBusy() {
		select {
		case <-timeout:
			return
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (a *sessionAgent) IsBusy() bool {
	var busy bool
	for cancelFunc := range a.activeRequests.Seq() {
		if cancelFunc != nil {
			busy = true
			break
		}
	}
	return busy
}

func (a *sessionAgent) IsSessionBusy(sessionID string) bool {
	_, busy := a.activeRequests.Get(sessionID)
	return busy
}

func (a *sessionAgent) QueuedPrompts(sessionID string) int {
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return 0
	}
	return len(l)
}

func (a *sessionAgent) QueuedPromptsList(sessionID string) []string {
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return nil
	}
	prompts := make([]string, len(l))
	for i, call := range l {
		prompts[i] = call.Prompt
	}
	return prompts
}

func (a *sessionAgent) SetModels(primary Model, title Model) {
	a.primaryModel.Set(primary)
	a.titleModel.Set(title)
}

func (a *sessionAgent) SetTools(toolList []fantasy.AgentTool) {
	a.tools.SetSlice(toolList)
}

func (a *sessionAgent) SetDeferredRegistry(reg *tools.DeferredRegistry) {
	a.deferredRegistry.Store(reg)
	a.lastDeferredHash.Set("")
}

// mergeDeferredTools combines an explicitly registered tool list with the
// deferred registry. Activated tools' real schemas are merged in; tools
// still deferred surface as proxy stubs. If a tool name already appears
// in `base` (e.g. someone wired it directly), the existing entry wins.
func mergeDeferredTools(base []fantasy.AgentTool, reg *tools.DeferredRegistry) []fantasy.AgentTool {
	seen := make(map[string]struct{}, len(base))
	for _, t := range base {
		seen[t.Info().Name] = struct{}{}
	}
	out := append([]fantasy.AgentTool{}, base...)
	for _, t := range reg.ActivatedTools() {
		name := t.Info().Name
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, t)
	}
	for _, stub := range reg.SnapshotStubs() {
		name := stub.Info().Name
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, stub)
	}
	return out
}

// maybeInjectDeferredReminder appends a <system-reminder> to the last user
// message in `messages` (creating one if necessary) whenever the set of
// deferred tools changed since the previous step. This is how the model
// gets notified that:
//
//   - new MCP servers just finished connecting and surfaced extra tools, or
//   - a previous tool_search call promoted entries out of the deferred set
//
// without paying the full schema cost up front.
func (a *sessionAgent) maybeInjectDeferredReminder(messages []fantasy.Message, reg *tools.DeferredRegistry) []fantasy.Message {
	hash := reg.DeferredHash()
	prev := a.lastDeferredHash.Get()
	a.lastDeferredHash.Set(hash)
	if hash == prev {
		return messages
	}
	deferred := reg.DeferredNames()
	pending := pendingMCPServersForReminder()
	if len(deferred) == 0 && len(pending) == 0 {
		return messages
	}
	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	if len(deferred) > 0 {
		sb.WriteString("The following deferred tools are now available via tool_search. Their schemas are NOT loaded — calling them directly will fail with InputValidationError. Use tool_search with query \"select:<name>[,<name>...]\" to load tool schemas before calling them:\n")
		for _, n := range deferred {
			sb.WriteString("  - ")
			sb.WriteString(n)
			sb.WriteString("\n")
		}
	}
	if len(pending) > 0 {
		sb.WriteString("\nThe following MCP servers are still connecting; their tools will appear in a later turn:\n")
		for _, n := range pending {
			sb.WriteString("  - ")
			sb.WriteString(n)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("</system-reminder>")
	reminder := sb.String()

	// Attach reminder to the last user message; if there is none (e.g.
	// the conversation just kicked off with an assistant continuation),
	// append a fresh user message.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == fantasy.MessageRoleUser {
			messages[i].Content = append(messages[i].Content, fantasy.TextPart{Text: reminder})
			return messages
		}
	}
	return append(messages, fantasy.NewUserMessage(reminder))
}

// pendingMCPServersForReminder mirrors the helper in the tools package so
// the agent doesn't need to round-trip through tool_search to learn which
// MCP servers haven't finished connecting.
func pendingMCPServersForReminder() []string {
	var out []string
	for name, info := range mcp.GetStates() {
		if info.State == mcp.StateConnected || info.State == mcp.StateDisabled {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (a *sessionAgent) SetSystemPrompt(systemPrompt string) {
	a.systemPrompt.Set(systemPrompt)
}

func (a *sessionAgent) Model() Model {
	return a.primaryModel.Get()
}

// TitleModel returns the small/utility model paired with this agent. Used
// for non-conversational background work like title generation and
// ghost-text suggestions where the primary model is overkill.
func (a *sessionAgent) TitleModel() Model {
	return a.titleModel.Get()
}

// convertToToolResult converts a fantasy tool result to a message tool result.
// maxToolResultLength caps the characters of any single tool result that
// reaches the model. Auto-summarize is reactive (checked between steps, see
// StopWhen), so a single oversized result — a huge file View, MCP/ptc payload —
// could otherwise leap the context window past its limit in one step before the
// next summarize check runs. ~120k chars ≈ 30k tokens, generous for legitimate
// reads while bounding the worst case to a fraction of the window. Tools with
// their own caps (bash at MaxOutputLength) arrive well under this.
const maxToolResultLength = 120_000

// truncateToolResultContent keeps the head and tail and drops the middle, with
// an explicit marker so the model knows output was clipped.
func truncateToolResultContent(content string) string {
	if len(content) <= maxToolResultLength {
		return content
	}
	half := maxToolResultLength / 2
	omitted := len(content) - 2*half
	return fmt.Sprintf("%s\n\n... [%d characters truncated to protect the context window] ...\n\n%s",
		content[:half], omitted, content[len(content)-half:])
}

func (a *sessionAgent) convertToToolResult(result fantasy.ToolResultContent) message.ToolResult {
	baseResult := message.ToolResult{
		ToolCallID: result.ToolCallID,
		Name:       result.ToolName,
		Metadata:   result.ClientMetadata,
	}

	switch result.Result.GetType() {
	case fantasy.ToolResultContentTypeText:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result.Result); ok {
			baseResult.Content = r.Text
		}
	case fantasy.ToolResultContentTypeError:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Result); ok {
			baseResult.Content = r.Error.Error()
			baseResult.IsError = true
		}
	case fantasy.ToolResultContentTypeMedia:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result.Result); ok {
			if !stringext.IsValidBase64(r.Data) {
				slog.Warn(
					"Tool returned media with invalid base64 data, discarding image",
					"tool", result.ToolName,
					"tool_call_id", result.ToolCallID,
				)
				baseResult.Content = "Tool returned image data with invalid encoding"
				baseResult.IsError = true
			} else {
				content := r.Text
				if content == "" {
					content = fmt.Sprintf("Loaded %s content", r.MediaType)
				}
				baseResult.Content = content
				baseResult.Data = r.Data
				baseResult.MIMEType = r.MediaType
			}
		}
	}

	// Bound any single result so one oversized payload can't blow the context
	// window in a step. Media Data (base64) lives in baseResult.Data, not
	// Content, so image bytes are unaffected.
	baseResult.Content = truncateToolResultContent(baseResult.Content)

	return baseResult
}

// workaroundProviderMediaLimitations converts media content in tool results to
// user messages for providers that don't natively support images in tool results.
//
// Problem: OpenAI, Google, OpenRouter, and other OpenAI-compatible providers
// don't support sending images/media in tool result messages - they only accept
// text in tool results. However, they DO support images in user messages.
//
// If we send media in tool results to these providers, the API returns an error.
//
// Solution: For these providers, we:
//  1. Replace the media in the tool result with a text placeholder
//  2. Inject a user message immediately after with the image as a file attachment
//  3. This maintains the tool execution flow while working around API limitations
//
// Anthropic and Bedrock support images natively in tool results, so we skip
// this workaround for them.
//
// Example transformation:
//
//	BEFORE: [tool result: image data]
//	AFTER:  [tool result: "Image loaded - see attached"], [user: image attachment]
func (a *sessionAgent) workaroundProviderMediaLimitations(messages []fantasy.Message, providerType catwalk.Type) []fantasy.Message {
	providerSupportsMedia := providerType == catwalk.Type(catwalk.InferenceProviderAnthropic) ||
		providerType == catwalk.Type(catwalk.InferenceProviderBedrock)

	if providerSupportsMedia {
		return messages
	}

	convertedMessages := make([]fantasy.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role != fantasy.MessageRoleTool {
			convertedMessages = append(convertedMessages, msg)
			continue
		}

		textParts := make([]fantasy.MessagePart, 0, len(msg.Content))
		var mediaFiles []fantasy.FilePart

		for _, part := range msg.Content {
			toolResult, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				textParts = append(textParts, part)
				continue
			}

			if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](toolResult.Output); ok {
				decoded, err := base64.StdEncoding.DecodeString(media.Data)
				if err != nil {
					slog.Warn("Failed to decode media data", "error", err)
					textParts = append(textParts, part)
					continue
				}

				mediaFiles = append(mediaFiles, fantasy.FilePart{
					Data:      decoded,
					MediaType: media.MediaType,
					Filename:  fmt.Sprintf("tool-result-%s", toolResult.ToolCallID),
				})

				textParts = append(textParts, fantasy.ToolResultPart{
					ToolCallID: toolResult.ToolCallID,
					Output: fantasy.ToolResultOutputContentText{
						Text: "[Image/media content loaded - see attached file]",
					},
					ProviderOptions: toolResult.ProviderOptions,
				})
			} else {
				textParts = append(textParts, part)
			}
		}

		convertedMessages = append(convertedMessages, fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: textParts,
		})

		if len(mediaFiles) > 0 {
			convertedMessages = append(convertedMessages, fantasy.NewUserMessage(
				"Here is the media content from the tool result:",
				mediaFiles...,
			))
		}
	}

	return convertedMessages
}

// buildSummaryPrompt constructs the prompt text for session summarization.
func buildSummaryPrompt(todos []session.Todo) string {
	return buildSummaryPromptWithPartialContext(todos, false)
}

// buildSummaryPromptWithPartialContext constructs the summary prompt text and
// optionally tells the model that the oldest context was trimmed to fit the
// available window.
func buildSummaryPromptWithPartialContext(todos []session.Todo, contextWasTrimmed bool) string {
	var sb strings.Builder
	sb.WriteString("Compress the conversation into durable memory for the next agent.")
	sb.WriteString("\nPreserve the minimum state needed to resume without rereading the whole transcript: goal, chosen approach, decision boundaries, files touched or to touch, commands run, verification results, and open risks.")
	sb.WriteString("\nKeep confirmed facts, decisions, constraints, file paths, commands run, verification results, unresolved questions, and todo statuses.")
	sb.WriteString("\nSeparate durable memory from session-local execution state: keep the plan, what was tried, and what remains; drop greetings, repetition, and step-by-step narration unless it changes the outcome.")
	if contextWasTrimmed {
		sb.WriteString("\n\nNote: the oldest messages were trimmed to fit the model window. Focus on the retained tail and call out any uncertainty about earlier context.")
	}
	if len(todos) > 0 {
		sb.WriteString("\n\n## Current Todo List\n\n")
		for _, t := range todos {
			fmt.Fprintf(&sb, "- [%s] %s\n", t.Status, t.Content)
		}
		sb.WriteString("\nInclude these tasks and their statuses in your summary. ")
		sb.WriteString("Instruct the resuming assistant to use the `todos` tool to continue tracking progress on these tasks.")
	}
	return sb.String()
}

func providerRetryLogFields(err *fantasy.ProviderError, delay time.Duration) []any {
	fields := []any{
		"retry_delay", delay.String(),
	}
	if err == nil {
		return fields
	}
	fields = append(fields, "status_code", err.StatusCode)
	if err.Title != "" {
		fields = append(fields, "title", err.Title)
	}
	if err.Message != "" {
		fields = append(fields, "message", err.Message)
	}
	return fields
}

var safeSpeculativeTools = map[string]bool{
	"glob":                  true,
	"grep":                  true,
	"view":                  true,
	"todos":                 true,
	"crush_info":            true,
	"diagnostics":           true,
	"references":            true,
	"nim_macro_expand":      true,
	"nim_safe_to_delete":    true,
	"nim_project_maps":      true,
	"nim_definition":        true,
	"nim_hover":             true,
	"nim_document_symbols":  true,
	"nim_workspace_symbols": true,
	"nim_check_file":        true,
	"nim_call_hierarchy":    true,
	"read_mcp_resource":     true,
	"list_mcp_resources":    true,
}

type speculativeToolWrapper struct {
	inner fantasy.AgentTool
}

func (s *speculativeToolWrapper) Info() fantasy.ToolInfo {
	return s.inner.Info()
}

func (s *speculativeToolWrapper) ProviderOptions() fantasy.ProviderOptions {
	return s.inner.ProviderOptions()
}

func (s *speculativeToolWrapper) SetProviderOptions(opts fantasy.ProviderOptions) {
	s.inner.SetProviderOptions(opts)
}

func (s *speculativeToolWrapper) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if !safeSpeculativeTools[call.Name] {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf("Tool %s is not allowed during speculative execution.", call.Name))
		resp.StopTurn = true
		return resp, nil
	}
	return s.inner.Run(ctx, call)
}

type memExtractToolWrapper struct {
	inner     fantasy.AgentTool
	memoryDir string
}

func (m *memExtractToolWrapper) Info() fantasy.ToolInfo {
	return m.inner.Info()
}

func (m *memExtractToolWrapper) ProviderOptions() fantasy.ProviderOptions {
	return m.inner.ProviderOptions()
}

func (m *memExtractToolWrapper) SetProviderOptions(opts fantasy.ProviderOptions) {
	m.inner.SetProviderOptions(opts)
}

func (m *memExtractToolWrapper) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	name := call.Name
	if name == "view" || name == "glob" || name == "grep" || name == "todos" {
		return m.inner.Run(ctx, call)
	}

	if name == "write" || name == "edit" {
		var input struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Input), &input); err == nil {
			fp := input.FilePath
			if fp == "" {
				fp = input.Path
			}
			if fp != "" {
				cleanPath := filepath.Clean(fp)
				cleanMemDir := filepath.Clean(m.memoryDir)
				if strings.HasPrefix(cleanPath, cleanMemDir) {
					return m.inner.Run(ctx, call)
				}
			}
		}
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf("Tool %s is restricted to the memory directory during extraction.", name))
		resp.StopTurn = true
		return resp, nil
	}

	resp := fantasy.NewTextErrorResponse(fmt.Sprintf("Tool %s is not allowed during memory extraction.", name))
	resp.StopTurn = true
	return resp, nil
}

// fireStopHook publishes a turn-end event to the configured Stop hooks.
// Decision values are ignored — the turn is already over, so there is
// nothing to allow, deny, or halt; only Context is honoured, surfaced
// as a debug log so a hook author can see whether its message landed.
// Failures are swallowed so a misbehaving hook never aborts a turn.
func (a *sessionAgent) fireStopHook(ctx context.Context, sessionID string, finishReason message.FinishReason, stepResult fantasy.StepResult) {
	payload := map[string]any{
		"finish_reason": string(finishReason),
		"last_text":     stepResult.Content.Text(),
		"tool_calls":    len(stepResult.Content.ToolCalls()),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Debug("Stop hook payload marshal failed", "session", sessionID, "error", err)
		return
	}
	// Empty toolName: Stop is a turn-level event, not bound to one tool.
	result, err := a.hookRunner.Run(ctx, hooks.EventStop, sessionID, "", string(raw))
	if err != nil {
		slog.Debug("Stop hook execution error", "session", sessionID, "error", err)
		return
	}
	if result.HookCount > 0 && result.Context != "" {
		slog.Debug("Stop hook context", "session", sessionID, "context", result.Context)
	}
}

// backupDiscardedMessages persists messages that auto-summarize is about
// to drop so the content survives the LLM context view boundary. Writes a
// single markdown archive under memory/sessions/ and appends one
// MEMORY.md index line so future sessions can find it via the same auto-
// memory injection path. Failures are logged but never bubble up — the
// caller's summarize must not be blocked by disk hiccups.
func (a *sessionAgent) backupDiscardedMessages(ctx context.Context, sessionID string, discarded []message.Message, workspacePath string) {
	if len(discarded) == 0 || a.dataDir == "" {
		return
	}
	_ = ctx // reserved for future cancellation; AppendEntry is sync today
	if err := memdir.EnsureWorkspace(a.dataDir, workspacePath); err != nil {
		slog.Debug("Session backup: ensure workspace failed", "session", sessionID, "error", err)
		return
	}
	sum := sha1.Sum([]byte(sessionID))
	shortSID := hex.EncodeToString(sum[:6])
	ts := time.Now()
	fileName := fmt.Sprintf("session-%s-%d.md", shortSID, ts.UnixNano())
	dir := filepath.Join(a.dataDir, "projects", memdir.WorkspaceSlug(workspacePath), "memory", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Debug("Session backup: mkdir failed", "session", sessionID, "error", err)
		return
	}
	path := filepath.Join(dir, fileName)

	fm := memdir.Frontmatter{
		Name:        fmt.Sprintf("session-backup-%s", shortSID),
		Description: fmt.Sprintf("Auto-summarize backup of %d discarded messages from session %s", len(discarded), shortSID),
		Type:        memdir.MemoryProject,
	}
	var b strings.Builder
	b.WriteString(memdir.EncodeFrontmatter(fm))
	fmt.Fprintf(&b, "# Session Backup\n\nSession ID: %s\nTimestamp: %s\nMessages: %d\n\n",
		sessionID, ts.Format(time.RFC3339), len(discarded))
	for i, m := range discarded {
		body := m.Content().Text
		if len(body) > 2048 {
			body = body[:2048] + "\n\n... [truncated]"
		}
		fmt.Fprintf(&b, "## Message %d — %s\n\n%s\n\n---\n\n", i+1, m.Role, body)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		slog.Debug("Session backup: write failed", "session", sessionID, "error", err)
		return
	}
	relPath := filepath.ToSlash(filepath.Join("sessions", fileName))
	title := fmt.Sprintf("Session backup %s", shortSID)
	hook := fmt.Sprintf("Auto-summarize spill %s — %d msgs", ts.Format("2006-01-02 15:04"), len(discarded))
	if err := memdir.AppendEntry(a.dataDir, workspacePath, title, relPath, hook); err != nil {
		slog.Debug("Session backup: index append failed", "session", sessionID, "error", err)
		return
	}
	slog.Info("Session backup written", "session", sessionID, "path", path, "messages", len(discarded))
}
