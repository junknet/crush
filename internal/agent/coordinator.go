package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/agent/notify"
	agentprompt "github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/suggestion"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/eventbus"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/memdir"
	"github.com/charmbracelet/crush/internal/provider"
	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/skills"
	"golang.org/x/sync/errgroup"

	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/anthropicoauth"
	"charm.land/fantasy/providers/antigravity"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	openaisdk "github.com/charmbracelet/openai-go/option"
	"github.com/qjebbs/go-jsons"
)

// Coordinator errors.
// reasoningBoostKeyword is the substring in a user prompt that escalates that
// single request to the model's strongest reasoning effort (see getProviderOptions).
const reasoningBoostKeyword = "思考"

var (
	errWorkerAgentNotConfigured          = errors.New("worker agent not configured")
	errPlanAgentNotConfigured            = errors.New("plan agent not configured")
	errBrainAgentNotConfigured           = errors.New("brain agent not configured")
	errModelProviderNotConfigured        = errors.New("model provider not configured")
	errBrainModelNotSelected             = errors.New("brain model not selected")
	errPlanModelNotSelected              = errors.New("plan model not selected")
	errWorkerModelNotSelected            = errors.New("worker model not selected")
	errExploreModelNotSelected           = errors.New("explore model not selected")
	errBrainModelProviderNotConfigured   = errors.New("brain model provider not configured")
	errPlanModelProviderNotConfigured    = errors.New("plan model provider not configured")
	errWorkerModelProviderNotConfigured  = errors.New("worker model provider not configured")
	errExploreModelProviderNotConfigured = errors.New("explore model provider not configured")
	errBrainModelNotFound                = errors.New("brain model not found in provider config")
	errPlanModelNotFound                 = errors.New("plan model not found in provider config")
	errWorkerModelNotFound               = errors.New("worker model not found in provider config")
	errExploreModelNotFound              = errors.New("explore model not found in provider config")
)

// anthropicBudgetForEffort maps the OpenAI-style reasoning effort string onto
// an Anthropic `thinking.budget_tokens` integer. Anthropic Messages API has no
// effort field — only an explicit budget — so we translate cross-provider
// configs here. Numbers are picked to match Opus/Sonnet thinking depth tiers
// the user expects from each effort label.
//
// Falls through to 2000 when effort is empty / unknown, matching the original
// hard-coded default so existing `think:true` configs don't regress.
func anthropicBudgetForEffort(effort string) int64 {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "none":
		return 1024
	case "low":
		return 4000
	case "medium":
		return 8000
	case "high":
		return 16000
	case "xhigh":
		return 32000
	default:
		return 2000
	}
}

// Copilot models that use the Responses API instead of Chat Completions.
var copilotResponsesModels = map[string]bool{
	"gpt-5.2":       true,
	"gpt-5.2-codex": true,
	"gpt-5.3-codex": true,
	"gpt-5.4-mini":  true,
	"gpt-5-mini":    true,
}

type Coordinator interface {
	// INFO: (kujtim) this is not used yet we will use this when we have multiple agents
	// SetMainAgent(string)
	Run(ctx context.Context, sessionID, prompt string, planMode bool, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	Summarize(context.Context, string) error
	Model() Model
	UpdateModels(ctx context.Context) error
	// Suggestion returns the ghost-text suggestion service, or nil when
	// disabled by config / not yet wired.
	Suggestion() *suggestion.Service
	PromoteSpeculativeSession(ctx context.Context, sessionID string) error
}

type coordinator struct {
	cfg         *config.ConfigStore
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	history     history.Service
	filetracker filetracker.Service
	lspManager  *lsp.Manager
	bgManager   *shell.BackgroundShellManager
	notify      pubsub.Publisher[notify.Notification]
	runtime     *agentruntime.RuntimeSession
	traceMu     sync.RWMutex
	lastRuntime *agentruntime.RuntimeSession

	currentAgent     SessionAgent
	currentAgentName string
	agents           map[string]SessionAgent

	// Skills discovery results (session-start snapshot).
	allSkills    []*skills.Skill // Pre-filter: all discovered after dedup.
	activeSkills []*skills.Skill // Post-filter: active skills only.
	skillTracker *skills.Tracker

	// Suggestion service for ghost-text autocomplete. Fed after each
	// successful brain turn; nil when DisableSuggestion is true in config.
	suggestion *suggestion.Service

	readyWg errgroup.Group

	speculativeMu      sync.Mutex
	speculativeCancels map[string]context.CancelFunc
}

func NewCoordinator(
	ctx context.Context,
	cfg *config.ConfigStore,
	sessions session.Service,
	messages message.Service,
	permissions permission.Service,
	history history.Service,
	filetracker filetracker.Service,
	lspManager *lsp.Manager,
	notify pubsub.Publisher[notify.Notification],
	bgManager *shell.BackgroundShellManager,
) (Coordinator, error) {
	// Discover skills once at session start.
	allSkills, activeSkills := discoverSkills(cfg)
	skillTracker := skills.NewTracker(activeSkills)

	c := &coordinator{
		cfg:          cfg,
		sessions:     sessions,
		messages:     messages,
		permissions:  permissions,
		history:      history,
		filetracker:  filetracker,
		lspManager:   lspManager,
		bgManager:    bgManager,
		notify:       notify,
		agents:             make(map[string]SessionAgent),
		allSkills:          allSkills,
		activeSkills:       activeSkills,
		skillTracker:       skillTracker,
		runtime:            agentruntime.NewSession(cfg.WorkingDir(), nil),
		speculativeCancels: make(map[string]context.CancelFunc),
	}

	agentName := config.AgentBrain
	agentCfg, ok := cfg.Config().Agents[agentName]
	if !ok {
		agentName = config.AgentBrain
		agentCfg, ok = cfg.Config().Agents[agentName]
	}
	if !ok {
		return nil, errWorkerAgentNotConfigured
	}

	systemPrompt, err := promptForAgentRole(agentName, agentprompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}

	agent, err := c.buildAgent(ctx, systemPrompt, agentCfg, false)
	if err != nil {
		return nil, err
	}
	c.currentAgent = agent
	c.currentAgentName = agentName
	c.agents[config.AgentBrain] = agent

	if workerCfg, ok := cfg.Config().Agents[config.AgentWorker]; ok {
		workerSystemPrompt, err := promptForAgentRole(config.AgentWorker, agentprompt.WithWorkingDir(c.cfg.WorkingDir()))
		if err != nil {
			return nil, err
		}
		workerAgent, err := c.buildAgent(ctx, workerSystemPrompt, workerCfg, true)
		if err != nil {
			return nil, err
		}
		c.agents[config.AgentWorker] = workerAgent
	}

	if planCfg, ok := cfg.Config().Agents[config.AgentPlan]; ok {
		planSystemPrompt, err := promptForAgentRole(config.AgentPlan, agentprompt.WithWorkingDir(c.cfg.WorkingDir()))
		if err != nil {
			return nil, err
		}
		planAgent, err := c.buildAgent(ctx, planSystemPrompt, planCfg, true)
		if err != nil {
			return nil, err
		}
		c.agents[config.AgentPlan] = planAgent
	}

	if exploreCfg, ok := cfg.Config().Agents[config.AgentExplore]; ok {
		exploreSystemPrompt, err := promptForAgentRole(config.AgentExplore, agentprompt.WithWorkingDir(c.cfg.WorkingDir()))
		if err != nil {
			return nil, err
		}
		exploreAgent, err := c.buildAgent(ctx, exploreSystemPrompt, exploreCfg, true)
		if err != nil {
			return nil, err
		}
		c.agents[config.AgentExplore] = exploreAgent
	}

	// Wire suggestion service. Uses the brain agent's title (utility)
	// model so ghost-text predictions don't burn the user's primary
	// model budget. Disabled via Options.DisableSuggestion.
	disableSugg := cfg.Config().Options != nil && cfg.Config().Options.DisableSuggestion
	c.suggestion = suggestion.New(messages, func() fantasy.LanguageModel {
		brain, ok := c.agents[config.AgentBrain]
		if !ok || brain == nil {
			return nil
		}
		sa, ok := brain.(*sessionAgent)
		if !ok {
			return nil
		}
		m := sa.TitleModel()
		return m.Model
	}, disableSugg)

	// Drive event-driven re-wakeups: when a backgrounded shell job finishes or
	// a monitor fires, automatically continue the session that launched it
	// instead of leaving the agent idle waiting for the user to poll.
	go c.watchBackgroundJobs(ctx)
	// Drive timer-based re-wakeups requested via the schedule_wakeup tool.
	go c.watchScheduledWakeups(ctx)

	// Optional Notification hook bridge. CRUSH_HOOK_NOTIFICATION=1 wires
	// every eventbus event into the user's Notification hook command. Kept
	// behind an env gate because a busy session can fire dozens of bus
	// events per second and most users do not want a hook command spawned
	// that often.
	if os.Getenv("CRUSH_HOOK_NOTIFICATION") == "1" {
		c.installNotificationHookBridge()
	}

	return c, nil
}

// installNotificationHookBridge wires eventbus events to the configured
// Notification hooks. Builds an event-routed Runner from the global
// hooks config so the user can match against ev.Kind from inside a hook
// command. A nil hooks config silently disables the bridge.
func (c *coordinator) installNotificationHookBridge() {
	cfg := c.cfg.Config()
	if cfg == nil || len(cfg.Hooks) == 0 {
		return
	}
	anyConfigured := false
	for _, list := range cfg.Hooks {
		if len(list) > 0 {
			anyConfigured = true
			break
		}
	}
	if !anyConfigured {
		return
	}
	runner := hooks.NewEventRunner(cfg.Hooks, c.cfg.WorkingDir(), c.cfg.WorkingDir())
	eventbus.InstallNotificationHandler(func(ev eventbus.Event) {
		raw, err := json.Marshal(map[string]any{
			"kind":       ev.Kind,
			"session_id": ev.SessionID,
			"payload":    ev.Payload,
		})
		if err != nil {
			return
		}
		// Use the event kind as toolName so user hook matchers can filter
		// (e.g. `monitor_match` only). Background-context: the hook fires
		// far from any agent loop, so a fresh context is the right scope.
		if _, err := runner.Run(context.Background(), hooks.EventNotification, ev.SessionID, ev.Kind, string(raw)); err != nil {
			slog.Debug("Notification hook error", "kind", ev.Kind, "error", err)
		}
	})
}

// Suggestion exposes the ghost-text suggestion service so the TUI can
// subscribe to fresh suggestions and acknowledge accept/reject events. May
// return nil if suggestion is disabled or NewCoordinator failed early.
func (c *coordinator) Suggestion() *suggestion.Service {
	if c == nil {
		return nil
	}
	return c.suggestion
}

// watchScheduledWakeups resumes a session when a schedule_wakeup timer fires,
// handing the agent the reason it asked to be woken for.
func (c *coordinator) watchScheduledWakeups(ctx context.Context) {
	events := tools.SubscribeWakeups(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			req := ev.Payload
			if req.SessionID == "" {
				continue
			}
			prompt := fmt.Sprintf(
				"Scheduled wake-up fired — this is an automatic continuation, not a new user request.\n\n"+
					"Reason you asked to be woken: %s\n\n"+
					"Proceed with that now. If the thing you were waiting on still isn't ready, you may schedule another wake-up.",
				req.Reason)
			slog.DebugContext(ctx, "Scheduled wake-up fired", "session_id", req.SessionID)
			go func() {
				if _, err := c.Run(ctx, req.SessionID, prompt, false); err != nil {
					slog.Error("Scheduled wake-up run failed", "session_id", req.SessionID, "error", err)
				}
			}()
		}
	}
}

// watchBackgroundJobs subscribes to background-job completion events and, for
// each finished job tied to a session, kicks off a follow-up run that feeds the
// result back to the agent so it can continue. A busy session queues the
// follow-up via the agent's normal message queue, so this never races an
// in-flight turn.
func (c *coordinator) watchBackgroundJobs(ctx context.Context) {
	events := shell.SubscribeBackgroundJobs(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			job := ev.Payload
			if job.SessionID == "" {
				continue
			}
			// Mirror the wake-up onto the unified eventbus so PrepareStep can
			// drain it as a <task-notification> system-reminder for the next
			// turn, in addition to the c.Run continuation below.
			publishBackgroundJobToEventBus(job)

			// Do not wake the agent for streaming output lines
			if job.Kind == shell.BackgroundKindMonitorLine {
				continue
			}

			prompt := buildBackgroundWakePrompt(job)
			slog.DebugContext(ctx, "Background job finished — waking session",
				"job", job.ID, "session_id", job.SessionID, "exit_code", job.ExitCode)
			go func() {
				if _, err := c.Run(ctx, job.SessionID, prompt, false); err != nil {
					slog.Error("Background job wake-up run failed",
						"job", job.ID, "session_id", job.SessionID, "error", err)
				}
			}()
		}
	}
}

// buildBackgroundWakePrompt renders the system-style message handed to the
// agent when a background job it started finished, or when a monitor it set on
// a running job fired.
func buildBackgroundWakePrompt(job shell.BackgroundJobEvent) string {
	output := job.OutputTail
	if output == "" {
		output = "(no output)"
	}
	const tail = "\n\nThis is an automatic continuation, not a new user request. Review this and continue the original task; if it is complete, summarize. Do not repeat work already done."

	switch job.Kind {
	case shell.BackgroundKindMonitorHit:
		return fmt.Sprintf(
			"Your monitor on background job %s matched pattern %q.\nMatched line: %s\nCommand: %s\n\nOutput (tail):\n%s%s",
			job.ID, job.Pattern, job.MatchLine, job.Command, output, tail)
	case shell.BackgroundKindMonitorEOF:
		return fmt.Sprintf(
			"Background job %s ended before your monitored pattern %q ever appeared (exit code %d).\nCommand: %s\n\nOutput (tail):\n%s%s",
			job.ID, job.Pattern, job.ExitCode, job.Command, output, tail)
	case shell.BackgroundKindMonitorTimeout:
		return fmt.Sprintf(
			"Your monitor on background job %s timed out without matching pattern %q; the job is still running.\nCommand: %s\n\nOutput (tail):\n%s%s",
			job.ID, job.Pattern, job.Command, output, tail)
	default: // BackgroundKindDone
		var status string
		switch {
		case job.Interrupted:
			status = "was interrupted/killed before completing"
		case job.ExitCode == 0:
			status = "finished successfully (exit code 0)"
		default:
			status = fmt.Sprintf("failed with exit code %d", job.ExitCode)
		}
		return fmt.Sprintf(
			"A background job you previously started has now completed.\nJob ID: %s\nCommand: %s\nResult: it %s.\n\nOutput (tail):\n%s%s",
			job.ID, job.Command, status, output, tail)
	}
}

// publishBackgroundJobToEventBus mirrors a background-job event onto the
// unified eventbus. Kept side-by-side with the c.Run wake-up path so existing
// behaviour is preserved while PrepareStep also sees the event as a
// task-notification.
func publishBackgroundJobToEventBus(job shell.BackgroundJobEvent) {
	kind := "bash_done"
	priority := eventbus.PriorityNext
	switch job.Kind {
	case shell.BackgroundKindMonitorHit:
		kind = "monitor_match"
		priority = eventbus.PriorityNow
	case shell.BackgroundKindMonitorEOF:
		kind = "monitor_eof"
	case shell.BackgroundKindMonitorTimeout:
		kind = "monitor_timeout"
	case shell.BackgroundKindMonitorLine:
		kind = "monitor_line"
		priority = eventbus.PriorityLater
	}
	payload := map[string]any{
		"shell_id":     job.ID,
		"command":      job.Command,
		"exit_code":    job.ExitCode,
		"interrupted":  job.Interrupted,
		"output_bytes": len(job.OutputTail),
		"output_tail":  job.OutputTail,
		"pattern":      job.Pattern,
		"match_line":   job.MatchLine,
	}
	eventbus.Default.Publish(eventbus.Event{
		Kind:      kind,
		SessionID: job.SessionID,
		Payload:   eventbus.MarshalJSONPayload(payload),
		Priority:  priority,
	})
}

// Run implements Coordinator.
func (c *coordinator) Run(ctx context.Context, sessionID string, prompt string, planMode bool, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	c.speculativeMu.Lock()
	if cancel, ok := c.speculativeCancels[sessionID]; ok {
		cancel()
		delete(c.speculativeCancels, sessionID)
	}
	c.speculativeMu.Unlock()

	if err := c.readyWg.Wait(); err != nil {
		return nil, err
	}

	rootProfile := scheduler.ProfileBrainAgent
	rootAgentName := config.AgentBrain
	if planMode {
		rootProfile = scheduler.ProfilePlanAgent
		rootAgentName = config.AgentPlan
	}

	rootAgent := c.agentForProfile(rootProfile)
	if rootAgent == nil {
		if planMode {
			return nil, errPlanAgentNotConfigured
		}
		return nil, errBrainAgentNotConfigured
	}

	previousAgent := c.currentAgent
	previousAgentName := c.currentAgentName
	c.currentAgent = rootAgent
	c.currentAgentName = rootAgentName
	defer func() {
		c.currentAgent = previousAgent
		c.currentAgentName = previousAgentName
	}()

	// refresh models before each run
	if err := c.UpdateModels(ctx); err != nil {
		return nil, fmt.Errorf("failed to update models: %w", err)
	}

	model := c.currentAgent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	if !model.CatwalkCfg.SupportsImages && attachments != nil {
		// filter out image attachments
		filteredAttachments := make([]message.Attachment, 0, len(attachments))
		for _, att := range attachments {
			if att.IsText() {
				filteredAttachments = append(filteredAttachments, att)
			}
		}
		attachments = filteredAttachments
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return nil, errModelProviderNotConfigured
	}

	// Keyword-triggered reasoning boost: when the user's prompt contains "思考"
	// this single request runs at the model's strongest reasoning effort, and
	// the resulting turn is flagged so the TUI can color it distinctly.
	boost := strings.Contains(prompt, reasoningBoostKeyword)
	if boost {
		slog.Debug("Reasoning boost engaged for this turn", "session", sessionID, "keyword", reasoningBoostKeyword)
	}

	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(sessionID, model, providerCfg, boost)

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		// NOTE(@andreynering): We don't return here because the event handling to ask the user to reauthenticate
		// depends on the flow below. If refresh fails, proceed with the token we have.
		slog.Error("Failed to refresh OAuth2 token. Proceeding with existing token.", "error", err)
	}

	run := func() (*fantasy.AgentResult, error) {
		taskRuntime := c.newTaskRuntime(sessionID)
		c.setLastRuntime(taskRuntime)
		taskScheduler := scheduler.NewAgentScheduler(taskRuntime)
		taskNode := c.ensureRootTask(taskScheduler, sessionID, prompt, maxTokens, rootProfile)
		if taskNode == nil {
			return nil, errors.New("failed to create root task")
		}
		c.preBindTaskTreeModels(taskNode)

		var result *fantasy.AgentResult
		err := taskScheduler.Dispatch(ctx, taskNode, scheduler.WorkerFunc(func(taskCtx context.Context, node *scheduler.TaskNode, intent provider.RequestIntent) (string, error) {
			callMaxTokens := maxTokens
			if intent.MaxOutputTokens > 0 {
				callMaxTokens = int64(intent.MaxOutputTokens)
			}
			taskPrompt := c.composeTaskPrompt(taskRuntime, node, prompt)
			// Only the brain (user-facing root task) carries the per-turn
			// dynamic env prefix. Sub-agents (worker/explore/plan) stay
			// pure so their static prefix hashes remain identical across
			// dispatches, maximising prompt-cache reuse.
			var dynPrefix string
			if node.Profile == scheduler.ProfileBrainAgent {
				dynPrefix = agentprompt.DynamicPrefix(taskCtx, c.cfg)
			}
			agent := c.agentForProfile(node.Profile)
			if agent == nil {
				return "", fmt.Errorf("agent not configured for profile %s", node.Profile)
			}
			model := agent.Model()
			c.bindTaskNodeModel(node, model)
			c.appendTaskInputTrace(taskRuntime, node, taskPrompt)
			requestStartedAt := time.Now()
			res, err := agent.Run(taskCtx, SessionAgentCall{
				SessionID:        sessionID,
				Prompt:           taskPrompt,
				DynamicPrefix:    dynPrefix,
				Attachments:      attachments,
				MaxOutputTokens:  callMaxTokens,
				ProviderOptions:  mergedOptions,
				BoostReasoning:   boost,
				Temperature:      temp,
				TopP:             topP,
				TopK:             topK,
				FrequencyPenalty: freqPenalty,
				PresencePenalty:  presPenalty,
				TraceRuntime:     taskRuntime,
				TaskNodeID:       node.ID,
				TaskParentID:     nodeParentIDForCall(node),
				TaskProfile:      string(node.Profile),
				ProviderID:       node.ProviderID,
				ProviderType:     node.ProviderType,
				ModelID:          node.ModelID,
			})
			node.FinishedAt = time.Now()
			if node.StartedAt.IsZero() {
				node.StartedAt = requestStartedAt
			}
			node.DurationMs = node.FinishedAt.Sub(node.StartedAt).Milliseconds()
			if err != nil {
				return "", err
			}
			if res == nil {
				// Run returns (nil, nil) when the session is busy and this call
				// was queued behind an in-flight turn; that turn absorbs the
				// prompt via the PrepareStep queue drain (see agent.go). It is
				// not a failure — the queued prompt will be answered there.
				slog.Debug("Task queued behind an in-flight turn; deferring to it",
					"session", sessionID, "node", node.ID)
				return "", nil
			}
			result = res
			output := res.Response.Content.Text()
			c.bindTaskNodeUsage(node, model, res.TotalUsage, taskPrompt, output)
			c.appendTaskOutputTrace(taskRuntime, node, output)
			c.recordTaskOutcome(taskRuntime, node, taskPrompt, output)
			return output, nil
		}))
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	beforeLoaded := c.skillTracker.LoadedNames()
	result, originalErr := run()

	if c.isUnauthorized(originalErr) {
		if err := c.retryAfterUnauthorized(ctx, providerCfg); err == nil {
			retryResult, retryErr := run()
			logTurnSkillUsage(sessionID, prompt, c.activeSkills, c.skillTracker, beforeLoaded)
			return retryResult, retryErr
		}
	}

	logTurnSkillUsage(sessionID, prompt, c.activeSkills, c.skillTracker, beforeLoaded)

	// Kick off ghost-text suggestion generation for this session after a
	// successful brain turn. Only the user-facing brain run fires it —
	// plan/worker/explore sub-agents are internal so a suggestion off
	// them would be confusing UX. Always non-blocking; uses context.Background
	// so cancelling this Run doesn't abort the suggestion call (it has
	// its own timeout via the suggestion service).
	if originalErr == nil && result != nil && rootProfile == scheduler.ProfileBrainAgent {
		sid := sessionID
		go c.triggerMemoryExtraction(context.Background(), sid)

		if c.suggestion != nil {
			go func() {
				prediction, err := c.suggestion.Generate(context.Background(), sid)
				if err != nil {
					slog.Debug("Suggestion generation failed", "session", sid, "error", err)
					return
				}
				if prediction != "" {
					c.StartSpeculativeRun(context.Background(), sid, prediction)
				}
			}()
		}
	}

	return result, originalErr
}

// TraceEntries returns the trace entries recorded by the most recent run.
func (c *coordinator) TraceEntries() []agentruntime.TaskTrace {
	if c == nil {
		return nil
	}
	c.traceMu.RLock()
	lastRuntime := c.lastRuntime
	c.traceMu.RUnlock()
	if lastRuntime == nil {
		return nil
	}
	return lastRuntime.TraceEntries()
}

func (c *coordinator) setLastRuntime(runtime *agentruntime.RuntimeSession) {
	if c == nil {
		return
	}
	c.traceMu.Lock()
	c.lastRuntime = runtime
	c.traceMu.Unlock()
}

// geminiThinkingLevel maps an OpenAI-style effort string onto a Gemini
// thinking_level. Gemini has no "xhigh"; its strongest is "HIGH".
func geminiThinkingLevel(effort string) string {
	if strings.EqualFold(strings.TrimSpace(effort), "xhigh") {
		return string(google.ThinkingLevelHigh)
	}
	return effort
}

// getProviderOptions builds the per-call provider options. When boost is true
// (the user's prompt contained the "思考" keyword), the reasoning effort is
// raised to the strongest the provider/SDK supports for this single request —
// "xhigh" for OpenAI-shaped effort, max thinking budget for Anthropic, and the
// top thinking_level for Gemini/Antigravity.
func getProviderOptions(sessionID string, model Model, providerCfg config.ProviderConfig, boost bool) fantasy.ProviderOptions {
	options := fantasy.ProviderOptions{}

	// effort is the reasoning effort to apply for this call. Boost overrides the
	// configured effort with the strongest value; per-provider branches clamp it
	// where the SDK has a lower ceiling (e.g. Gemini → HIGH).
	effort := model.ModelCfg.ReasoningEffort
	if boost && model.CatwalkCfg.CanReason {
		effort = string(openai.ReasoningEffortXHigh)
	}

	cfgOpts := []byte("{}")
	providerCfgOpts := []byte("{}")
	catwalkOpts := []byte("{}")

	if model.ModelCfg.ProviderOptions != nil {
		data, err := json.Marshal(model.ModelCfg.ProviderOptions)
		if err == nil {
			cfgOpts = data
		}
	}

	if providerCfg.ProviderOptions != nil {
		data, err := json.Marshal(providerCfg.ProviderOptions)
		if err == nil {
			providerCfgOpts = data
		}
	}

	if model.CatwalkCfg.Options.ProviderOptions != nil {
		data, err := json.Marshal(model.CatwalkCfg.Options.ProviderOptions)
		if err == nil {
			catwalkOpts = data
		}
	}

	readers := []io.Reader{
		bytes.NewReader(catwalkOpts),
		bytes.NewReader(providerCfgOpts),
		bytes.NewReader(cfgOpts),
	}

	got, err := jsons.Merge(readers)
	if err != nil {
		slog.Error("Could not merge call config", "err", err)
		return options
	}

	mergedOptions := make(map[string]any)

	err = json.Unmarshal([]byte(got), &mergedOptions)
	if err != nil {
		slog.Error("Could not create config for call", "err", err)
		return options
	}

	switch providerCfg.Type {
	case openai.Name, azure.Name:
		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && effort != "" && model.CatwalkCfg.CanReason {
			mergedOptions["reasoning_effort"] = effort
		}
		// Pin this request to a per-session backend partition so OpenAI's
		// automatic prefix cache hits across turns. Inert when sessionID is
		// empty (e.g. unit tests) or the kill switch is set.
		agentprompt.MaybeInjectPromptCacheKey(mergedOptions, sessionID, string(providerCfg.Type))
		if openai.IsResponsesModel(model.CatwalkCfg.ID) {
			if openai.IsResponsesReasoningModel(model.CatwalkCfg.ID) {
				mergedOptions["reasoning_summary"] = "auto"
				mergedOptions["include"] = []openai.IncludeType{openai.IncludeReasoningEncryptedContent}
			}
			parsed, err := openai.ParseResponsesOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		} else {
			parsed, err := openai.ParseOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		}
	case anthropic.Name, bedrock.Name:
		// Anthropic Messages API has no `effort` field; the only thinking knob
		// is `thinking.budget_tokens` (integer hard cap). To keep crush.json
		// portable across providers we map OpenAI-style effort strings onto
		// equivalent budgets when the model has thinking enabled.
		if _, hasThink := mergedOptions["thinking"]; !hasThink && model.ModelCfg.Think && model.CatwalkCfg.CanReason {
			budget := model.ModelCfg.ThinkingBudget
			if budget <= 0 {
				budget = anthropicBudgetForEffort(effort)
			}
			mergedOptions["thinking"] = map[string]any{"budget_tokens": budget}
		}
		// `effort` is OpenAI-shaped — drop it before parsing so the Anthropic
		// SDK doesn't see an unknown key. (We translated whatever the user
		// asked for into budget_tokens above.)
		delete(mergedOptions, "effort")
		parsed, err := anthropic.ParseOptions(mergedOptions)
		if err == nil {
			options[anthropic.Name] = parsed
		}

	case openrouter.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && effort != "" {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  effort,
			}
		}
		parsed, err := openrouter.ParseOptions(mergedOptions)
		if err == nil {
			options[openrouter.Name] = parsed
		}
	case vercel.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && effort != "" {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  effort,
			}
		}
		parsed, err := vercel.ParseOptions(mergedOptions)
		if err == nil {
			options[vercel.Name] = parsed
		}
	case google.Name:
		_, hasReasoning := mergedOptions["thinking_config"]
		if !hasReasoning {
			if strings.HasPrefix(model.CatwalkCfg.ID, "gemini-2") {
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_budget":  2000,
					"include_thoughts": true,
				}
			} else {
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_level":   geminiThinkingLevel(effort),
					"include_thoughts": true,
				}
			}
		}
		parsed, err := google.ParseOptions(mergedOptions)
		if err == nil {
			options[google.Name] = parsed
		}
	case antigravity.Name:
		// Antigravity (Gemini via CodeAssist) keeps its default thinking config
		// unless boosted. On boost, pin the strongest thinking level for this
		// request. Project/SessionID are filled by the language model.
		if boost {
			options[antigravity.Name] = &antigravity.ProviderOptions{
				Thinking: &antigravity.ThinkingConfig{ThinkingLevel: string(google.ThinkingLevelHigh)},
			}
		}
	case openaicompat.Name, hyper.Name:
		extraBody := make(map[string]any)

		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && effort != "" && model.CatwalkCfg.CanReason {
			switch providerCfg.ID {
			case string(catwalk.InferenceProviderIoNet):
				extraBody["reasoning"] = map[string]string{"effort": effort}
			default:
				mergedOptions["reasoning_effort"] = effort
			}
		}

		// "reasoning effort" is a standard OpenAI field, but "thinking" is not.
		// Setting it in the right way for each provider.
		// TODO: Abstract this in Fantasy somehow?
		// TODO: Allow custom providers to specify how to set this?
		switch providerCfg.ID {
		case hyper.Name:
			extraBody["thinking"] = model.ModelCfg.Think
		case string(catwalk.InferenceProviderIoNet):
			if _, ok := extraBody["reasoning"]; !ok && model.CatwalkCfg.CanReason {
				if model.ModelCfg.Think {
					extraBody["reasoning"] = map[string]string{"effort": "medium"}
				} else {
					extraBody["reasoning"] = map[string]string{"effort": "none"}
				}
			}
		case string(catwalk.InferenceProviderZAI), string(catwalk.InferenceProviderDeepSeek):
			if model.ModelCfg.Think || effort != "" {
				extraBody["thinking"] = map[string]any{
					"type": "enabled",
				}
			} else {
				extraBody["thinking"] = map[string]any{
					"type": "disabled",
				}
			}
		case string(catwalk.InferenceProviderAlibabaSingapore):
			if model.CatwalkCfg.CanReason {
				extraBody["enable_thinking"] = model.ModelCfg.Think
			}
		}

		mergedOptions["extra_body"] = extraBody

		parsed, err := openaicompat.ParseOptions(mergedOptions)
		if err == nil {
			options[openaicompat.Name] = parsed
		}
	}

	return options
}

func mergeCallOptions(sessionID string, model Model, cfg config.ProviderConfig, boost bool) (fantasy.ProviderOptions, *float64, *float64, *int64, *float64, *float64) {
	modelOptions := getProviderOptions(sessionID, model, cfg, boost)
	temp := cmp.Or(model.ModelCfg.Temperature, model.CatwalkCfg.Options.Temperature)
	topP := cmp.Or(model.ModelCfg.TopP, model.CatwalkCfg.Options.TopP)
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	freqPenalty := cmp.Or(model.ModelCfg.FrequencyPenalty, model.CatwalkCfg.Options.FrequencyPenalty)
	presPenalty := cmp.Or(model.ModelCfg.PresencePenalty, model.CatwalkCfg.Options.PresencePenalty)
	return modelOptions, temp, topP, topK, freqPenalty, presPenalty
}

func (c *coordinator) buildAgent(ctx context.Context, prompt *agentprompt.Prompt, agent config.Agent, isSubAgent bool) (SessionAgent, error) {
	primary, title, err := c.buildAgentModels(ctx, agent, isSubAgent)
	if err != nil {
		return nil, err
	}

	// Build the event-routed hook runner up front so the sessionAgent
	// can fire Stop hooks at turn end (in addition to the PreToolUse /
	// PostToolUse wraps applied to tool calls below). Sub-agents never
	// fire hooks — keep parity with wrapToolsWithHooks's isSubAgent guard.
	var hookRunner *hooks.Runner
	if allHooks := c.cfg.Config().Hooks; len(allHooks) > 0 && !isSubAgent {
		for _, list := range allHooks {
			if len(list) > 0 {
				hookRunner = hooks.NewEventRunner(allHooks, c.cfg.WorkingDir(), c.cfg.WorkingDir())
				break
			}
		}
	}

	primaryProviderCfg, _ := c.cfg.Config().Providers.Get(primary.ModelCfg.Provider)
	result := NewSessionAgent(SessionAgentOptions{
		PrimaryModel:         primary,
		TitleModel:           title,
		SystemPromptPrefix:   primaryProviderCfg.SystemPromptPrefix,
		SystemPrompt:         "",
		IsSubAgent:           isSubAgent,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		Sessions:             c.sessions,
		Messages:             c.messages,
		Tools:                nil,
		Notify:               c.notify,
		DataDir:              c.cfg.Config().Options.DataDirectory,
		WorkingDir:           c.cfg.WorkingDir(),
		HookRunner:           hookRunner,
	})

	systemPrompt, err := prompt.Build(ctx, primary.Model.Provider(), primary.Model.Model(), c.cfg)
	if err != nil {
		return nil, err
	}
	result.SetSystemPrompt(systemPrompt)

	builtTools, deferredRegistry, err := c.buildTools(ctx, agent, isSubAgent)
	if err != nil {
		return nil, err
	}
	result.SetTools(builtTools)
	if deferredRegistry != nil {
		result.SetDeferredRegistry(deferredRegistry)
	}

	return result, nil
}

func (c *coordinator) buildTools(ctx context.Context, agent config.Agent, isSubAgent bool) ([]fantasy.AgentTool, *tools.DeferredRegistry, error) {
	var allTools []fantasy.AgentTool

	// Recursion guard: sub-agents never get the delegation tools. Even if a
	// misconfigured AllowedTools includes "agent" or "agentic_fetch", we drop
	// them here so a sub-agent can't spawn a sub-sub-agent. The current
	// architecture has no depth budget, so one-level-only is the safe rule.
	if !isSubAgent {
		if slices.Contains(agent.AllowedTools, AgentToolName) {
			agentTool, err := c.agentTool(ctx)
			if err != nil {
				return nil, nil, err
			}
			allTools = append(allTools, agentTool)
		}

		if slices.Contains(agent.AllowedTools, tools.AgenticFetchToolName) {
			agenticFetchTool, err := c.agenticFetchTool(ctx, nil)
			if err != nil {
				return nil, nil, err
			}
			allTools = append(allTools, agenticFetchTool)
		}
	}

	// Get the model name for the agent
	modelID := ""
	if modelCfg, ok := c.cfg.Config().SelectedModelForType(agent.Model); ok {
		if model := c.cfg.Config().GetModel(modelCfg.Provider, modelCfg.Model); model != nil {
			modelID = model.ID
		}
	}

	logFile := filepath.Join(c.cfg.Config().Options.DataDirectory, "logs", "crush.log")

	// Hook runner for tool-call wrappers. Distinct from the one built in
	// buildAgent (which feeds turn-level Stop hooks) because buildTools
	// runs on a different control path; both share the same config so
	// either-or is safe — the slot lookup is per-event anyway.
	var hookRunner *hooks.Runner
	if allHooks := c.cfg.Config().Hooks; len(allHooks) > 0 && !isSubAgent {
		for _, list := range allHooks {
			if len(list) > 0 {
				hookRunner = hooks.NewEventRunner(allHooks, c.cfg.WorkingDir(), c.cfg.WorkingDir())
				break
			}
		}
	}

	allTools = append(
		allTools,
		tools.NewBashTool(c.permissions, c.bgManager, c.cfg.WorkingDir(), c.cfg.Config().Options.DataDirectory, c.cfg.Config().Options.Attribution, modelID),
		tools.NewCrushInfoTool(c.cfg, c.lspManager, c.allSkills, c.activeSkills, c.skillTracker),
		tools.NewCrushLogsTool(logFile),
		tools.NewJobOutputTool(c.bgManager),
		tools.NewJobKillTool(c.bgManager),
		tools.NewMonitorTool(c.bgManager),
		tools.NewScheduleWakeupTool(c.cfg.Config().Options.DataDirectory),
		tools.NewDownloadTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewMultiEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewFetchTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewGlobTool(c.cfg.WorkingDir()),
		tools.NewGrepTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Grep),
		tools.NewLsTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(nil),
		tools.NewTodosTool(c.sessions),
		tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, c.skillTracker, c.cfg.WorkingDir(), c.cfg.Config().Options.SkillsPaths...),
		tools.NewWriteTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
	)

	// Add LSP tools if user has configured LSPs or auto_lsp is enabled (nil or true).
	if len(c.cfg.Config().LSP) > 0 || c.cfg.Config().Options.AutoLSP == nil || *c.cfg.Config().Options.AutoLSP {
		allTools = append(allTools,
			tools.NewDiagnosticsTool(c.lspManager),
			tools.NewReferencesTool(c.lspManager),
			tools.NewNimRestartTool(c.lspManager),
			tools.NewNimMacroExpandTool(c.lspManager),
			tools.NewNimSafeToDeleteTool(c.lspManager),
			tools.NewNimProjectMapsTool(c.lspManager),
			tools.NewNimDefinitionTool(c.lspManager),
			tools.NewNimHoverTool(c.lspManager),
			tools.NewNimDocumentSymbolsTool(c.lspManager),
			tools.NewNimWorkspaceSymbolsTool(c.lspManager),
			tools.NewNimCheckFileTool(c.lspManager),
			tools.NewNimCallHierarchyTool(c.lspManager),
		)
	}

	if len(c.cfg.Config().MCP) > 0 {
		allTools = append(
			allTools,
			tools.NewListMCPResourcesTool(c.cfg, c.permissions),
			tools.NewReadMCPResourceTool(c.cfg, c.permissions),
		)
	}

	var filteredTools []fantasy.AgentTool
	for _, tool := range allTools {
		if slices.Contains(agent.AllowedTools, tool.Info().Name) {
			filteredTools = append(filteredTools, tool)
		}
	}

	// MCP tools are *deferred*: the model sees a stub list (name +
	// description only) and must call tool_search to load JSON schemas
	// before invoking them. This keeps the initial tool-list footprint
	// bounded even when many MCP servers expose hundreds of tools.
	var deferredRegistry *tools.DeferredRegistry
	mcpTools := tools.GetMCPTools(c.permissions, c.cfg, c.cfg.WorkingDir())
	if len(mcpTools) > 0 || len(c.cfg.Config().MCP) > 0 {
		deferredRegistry = tools.NewDeferredRegistry()
	}
	for _, tool := range mcpTools {
		if agent.AllowedMCP == nil {
			// No MCP restrictions
			deferredRegistry.Register(tool, tool.MCP())
			continue
		}
		if len(agent.AllowedMCP) == 0 {
			// No MCPs allowed
			slog.Debug("No MCPs allowed", "tool", tool.Name(), "agent", agent.Name)
			break
		}

		for mcpName, mcpTools := range agent.AllowedMCP {
			if mcpName != tool.MCP() {
				continue
			}
			if len(mcpTools) == 0 || slices.Contains(mcpTools, tool.MCPToolName()) {
				deferredRegistry.Register(tool, tool.MCP())
				break
			}
			slog.Debug("MCP not allowed", "tool", tool.Name(), "agent", agent.Name)
		}
	}

	// If anything is deferred (now or once MCP servers finish
	// connecting), surface tool_search so the model can promote
	// schemas on demand.
	if deferredRegistry != nil {
		filteredTools = append(filteredTools, tools.NewToolSearchTool(deferredRegistry))
	}

	slices.SortFunc(filteredTools, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})

	// Wrap tools with hook interception for the top-level agent only.
	// Sub-agents (the `agent` tool, `agentic_fetch`, etc.) run
	// without hook interception to avoid firing the user's hook N times
	// per delegated turn. The top-level invocation of the sub-agent tool
	// itself is still wrapped from the brain's side.
	filteredTools = wrapToolsWithHooks(filteredTools, hookRunner, isSubAgent)

	if c.runtime != nil {
		for _, tool := range filteredTools {
			c.runtime.RegisterTool(tool.Info().Name)
		}
		if deferredRegistry != nil {
			for _, name := range deferredRegistry.Names() {
				c.runtime.RegisterTool(name)
			}
		}
	}

	return filteredTools, deferredRegistry, nil
}

func (c *coordinator) buildAgentModels(ctx context.Context, agent config.Agent, isSubAgent bool) (Model, Model, error) {
	primaryType := agent.Model
	if primaryType == "" {
		primaryType = selectedModelTypeForAgent(agent.ID)
	}
	secondaryType := config.SelectedModelTypeExplore
	if primaryType == secondaryType {
		secondaryType = primaryType
	}

	primary, err := c.buildModelForType(ctx, primaryType, isSubAgent)
	if err != nil {
		return Model{}, Model{}, err
	}
	title, err := c.buildModelForType(ctx, secondaryType, true)
	if err != nil {
		return Model{}, Model{}, err
	}
	return primary, title, nil
}

func selectedModelTypeForAgent(agentID string) config.SelectedModelType {
	switch agentID {
	case config.AgentBrain:
		return config.SelectedModelTypeBrain
	case config.AgentPlan:
		return config.SelectedModelTypePlan
	case config.AgentWorker:
		return config.SelectedModelTypeWorker
	case config.AgentExplore:
		return config.SelectedModelTypeExplore
	default:
		return config.SelectedModelTypeBrain
	}
}

func (c *coordinator) buildModelForType(ctx context.Context, modelType config.SelectedModelType, isSubAgent bool) (Model, error) {
	selectedModelCfg, ok := c.cfg.Config().SelectedModelForType(modelType)
	if !ok {
		switch modelType {
		case config.SelectedModelTypeBrain:
			return Model{}, errBrainModelNotSelected
		case config.SelectedModelTypePlan:
			return Model{}, errPlanModelNotSelected
		case config.SelectedModelTypeWorker:
			return Model{}, errWorkerModelNotSelected
		case config.SelectedModelTypeExplore:
			return Model{}, errExploreModelNotSelected
		default:
			return Model{}, errBrainModelNotSelected
		}
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(selectedModelCfg.Provider)
	if !ok {
		switch modelType {
		case config.SelectedModelTypeBrain:
			return Model{}, errBrainModelProviderNotConfigured
		case config.SelectedModelTypePlan:
			return Model{}, errPlanModelProviderNotConfigured
		case config.SelectedModelTypeWorker:
			return Model{}, errWorkerModelProviderNotConfigured
		case config.SelectedModelTypeExplore:
			return Model{}, errExploreModelProviderNotConfigured
		default:
			return Model{}, errBrainModelProviderNotConfigured
		}
	}

	provider, err := c.buildProvider(providerCfg, selectedModelCfg, isSubAgent)
	if err != nil {
		return Model{}, err
	}

	var catwalkModel *catwalk.Model
	for _, m := range providerCfg.Models {
		if m.ID == selectedModelCfg.Model {
			candidate := m
			catwalkModel = &candidate
			break
		}
	}
	if catwalkModel == nil {
		switch modelType {
		case config.SelectedModelTypeBrain:
			return Model{}, errBrainModelNotFound
		case config.SelectedModelTypeWorker:
			return Model{}, errWorkerModelNotFound
		case config.SelectedModelTypeExplore:
			return Model{}, errExploreModelNotFound
		default:
			return Model{}, errBrainModelNotFound
		}
	}

	modelID := selectedModelCfg.Model
	if selectedModelCfg.Provider == openrouter.Name && isExactoSupported(modelID) {
		modelID += ":exacto"
	}

	languageModel, err := provider.LanguageModel(ctx, modelID)
	if err != nil {
		return Model{}, err
	}

	return Model{
		Model:        languageModel,
		CatwalkCfg:   *catwalkModel,
		ModelCfg:     selectedModelCfg,
		ProviderType: providerCfg.Type,
		FlatRate:     providerCfg.FlatRate,
	}, nil
}

func (c *coordinator) buildAnthropicProvider(baseURL, apiKey string, headers map[string]string, providerID string) (fantasy.Provider, error) {
	var opts []anthropic.Option

	switch {
	case strings.HasPrefix(apiKey, "Bearer "):
		// NOTE: Prevent the SDK from picking up the API key from env.
		os.Setenv("ANTHROPIC_API_KEY", "")
		headers["Authorization"] = apiKey
	case providerID == string(catwalk.InferenceProviderMiniMax) || providerID == string(catwalk.InferenceProviderMiniMaxChina):
		// NOTE: Prevent the SDK from picking up the API key from env.
		os.Setenv("ANTHROPIC_API_KEY", "")
		headers["Authorization"] = "Bearer " + apiKey
	case apiKey != "":
		// X-Api-Key header
		opts = append(opts, anthropic.WithAPIKey(apiKey))
	}

	if len(headers) > 0 {
		opts = append(opts, anthropic.WithHeaders(headers))
	}

	if baseURL != "" {
		opts = append(opts, anthropic.WithBaseURL(baseURL))
	}

	if httpClient := log.NewProviderHTTPClient("anthropic", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, anthropic.WithHTTPClient(httpClient))
	}
	return anthropic.New(opts...)
}

func (c *coordinator) buildOpenaiProvider(baseURL, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []openai.Option{
		openai.WithAPIKey(apiKey),
		openai.WithUseResponsesAPI(),
	}
	if httpClient := log.NewProviderHTTPClient("openai", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, openai.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, openai.WithHeaders(headers))
	}
	if baseURL != "" {
		opts = append(opts, openai.WithBaseURL(baseURL))
	}
	return openai.New(opts...)
}

func (c *coordinator) buildOpenrouterProvider(_, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []openrouter.Option{
		openrouter.WithAPIKey(apiKey),
	}
	if httpClient := log.NewProviderHTTPClient("openrouter", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, openrouter.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, openrouter.WithHeaders(headers))
	}
	return openrouter.New(opts...)
}

func (c *coordinator) buildVercelProvider(_, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []vercel.Option{
		vercel.WithAPIKey(apiKey),
	}
	if httpClient := log.NewProviderHTTPClient("vercel", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, vercel.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, vercel.WithHeaders(headers))
	}
	return vercel.New(opts...)
}

func (c *coordinator) buildOpenaiCompatProvider(baseURL, apiKey string, headers map[string]string, extraBody map[string]any, providerID string, isSubAgent bool) (fantasy.Provider, error) {
	opts := []openaicompat.Option{
		openaicompat.WithBaseURL(baseURL),
		openaicompat.WithAPIKey(apiKey),
	}

	// Set HTTP client based on provider and debug mode.
	var httpClient *http.Client
	if providerID == string(catwalk.InferenceProviderCopilot) {
		opts = append(
			opts,
			openaicompat.WithUseResponsesAPI(),
			openaicompat.WithResponsesAPIFunc(func(modelID string) bool {
				return copilotResponsesModels[modelID]
			}),
		)
		httpClient = copilot.NewClient(isSubAgent, c.cfg.Config().Options.Debug)
	} else {
		httpClient = log.NewProviderHTTPClient(providerID, c.cfg.Config().Options.Debug)
	}
	if httpClient != nil {
		opts = append(opts, openaicompat.WithHTTPClient(httpClient))
	}

	if len(headers) > 0 {
		opts = append(opts, openaicompat.WithHeaders(headers))
	}

	for extraKey, extraValue := range extraBody {
		opts = append(opts, openaicompat.WithSDKOptions(openaisdk.WithJSONSet(extraKey, extraValue)))
	}

	return openaicompat.New(opts...)
}

func (c *coordinator) buildAzureProvider(baseURL, apiKey string, headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []azure.Option{
		azure.WithBaseURL(baseURL),
		azure.WithAPIKey(apiKey),
		azure.WithUseResponsesAPI(),
	}
	if httpClient := log.NewProviderHTTPClient("azure", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, azure.WithHTTPClient(httpClient))
	}
	if options == nil {
		options = make(map[string]string)
	}
	if apiVersion, ok := options["apiVersion"]; ok {
		opts = append(opts, azure.WithAPIVersion(apiVersion))
	}
	if len(headers) > 0 {
		opts = append(opts, azure.WithHeaders(headers))
	}

	return azure.New(opts...)
}

func (c *coordinator) buildBedrockProvider(apiKey string, headers map[string]string) (fantasy.Provider, error) {
	var opts []bedrock.Option
	if httpClient := log.NewProviderHTTPClient("bedrock", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, bedrock.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, bedrock.WithHeaders(headers))
	}
	switch {
	case apiKey != "":
		opts = append(opts, bedrock.WithAPIKey(apiKey))
	case os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "":
		opts = append(opts, bedrock.WithAPIKey(os.Getenv("AWS_BEARER_TOKEN_BEDROCK")))
	default:
		// Skip, let the SDK do authentication.
	}
	return bedrock.New(opts...)
}

func (c *coordinator) buildGoogleProvider(baseURL, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []google.Option{
		google.WithBaseURL(baseURL),
		google.WithGeminiAPIKey(apiKey),
	}
	if httpClient := log.NewProviderHTTPClient("google", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, google.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, google.WithHeaders(headers))
	}
	return google.New(opts...)
}

// buildAntigravityProvider wires the Antigravity (Google CodeAssist subscription)
// provider. OAuth credentials live in libsecret keyring + ~/.gemini/oauth_creds.json
// — both managed by the provider itself, no API key plumbing required.
//
// `extra_params` honoured:
//   - "default_project": pins the cloudaicompanion project id
func (c *coordinator) buildAntigravityProvider(headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []antigravity.Option{}
	if httpClient := log.NewProviderHTTPClient("antigravity", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, antigravity.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		// Antigravity has no header injection path of its own yet — surfacing
		// the warning here keeps the dispatch table honest if a user sets
		// extra_headers on this provider in crush.json.
		slog.Warn("Antigravity provider ignores extra_headers", "headers", headers)
	}
	if project := options["default_project"]; project != "" {
		opts = append(opts, antigravity.WithDefaultProject(project))
	}
	return antigravity.New(opts...)
}

// buildAnthropicOAuthProvider wires the Claude Code subscription-side OAuth
// provider. Credentials come from ~/.claude/.credentials.json by default
// (the path the official CLI writes).
//
// `extra_params` honoured:
//   - "credentials_path": override the on-disk creds file location
//   - "profile":          legacy wecode-CLI layout (~/.claude-accounts/<p>/)
//   - "app_name":         override the `x-app` header (defaults to "cli")
func (c *coordinator) buildAnthropicOAuthProvider(headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	var opts []anthropicoauth.Option
	if httpClient := log.NewProviderHTTPClient("anthropic-oauth", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, anthropicoauth.WithHTTPClient(httpClient))
	}
	if path := options["credentials_path"]; path != "" {
		opts = append(opts, anthropicoauth.WithCredentialsPath(path))
	}
	if profile := options["profile"]; profile != "" {
		opts = append(opts, anthropicoauth.WithProfile(profile))
	}
	if appName := options["app_name"]; appName != "" {
		opts = append(opts, anthropicoauth.WithAppName(appName))
	}
	if len(headers) > 0 {
		slog.Warn("Anthropic-OAuth provider ignores extra_headers (server-side validates Claude Code header set)", "headers", headers)
	}
	return anthropicoauth.New(opts...)
}

func (c *coordinator) buildGoogleVertexProvider(headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []google.Option{}
	if httpClient := log.NewProviderHTTPClient("google-vertex", c.cfg.Config().Options.Debug); httpClient != nil {
		opts = append(opts, google.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, google.WithHeaders(headers))
	}

	project := options["project"]
	location := options["location"]

	opts = append(opts, google.WithVertex(project, location))

	return google.New(opts...)
}

func (c *coordinator) isAnthropicThinking(model config.SelectedModel) bool {
	if model.Think {
		return true
	}
	opts, err := anthropic.ParseOptions(model.ProviderOptions)
	return err == nil && opts.Thinking != nil
}

func (c *coordinator) buildProvider(providerCfg config.ProviderConfig, model config.SelectedModel, isSubAgent bool) (fantasy.Provider, error) {
	headers := maps.Clone(providerCfg.ExtraHeaders)
	if headers == nil {
		headers = make(map[string]string)
	}

	// handle special headers for anthropic
	if providerCfg.Type == anthropic.Name && c.isAnthropicThinking(model) {
		if v, ok := headers["anthropic-beta"]; ok {
			headers["anthropic-beta"] = v + ",interleaved-thinking-2025-05-14"
		} else {
			headers["anthropic-beta"] = "interleaved-thinking-2025-05-14"
		}
	}

	apiKey, _ := c.cfg.Resolve(providerCfg.APIKey)
	baseURL, _ := c.cfg.Resolve(providerCfg.BaseURL)

	switch providerCfg.Type {
	case openai.Name:
		return c.buildOpenaiProvider(baseURL, apiKey, headers)
	case anthropic.Name:
		return c.buildAnthropicProvider(baseURL, apiKey, headers, providerCfg.ID)
	case openrouter.Name:
		return c.buildOpenrouterProvider(baseURL, apiKey, headers)
	case vercel.Name:
		return c.buildVercelProvider(baseURL, apiKey, headers)
	case azure.Name:
		return c.buildAzureProvider(baseURL, apiKey, headers, providerCfg.ExtraParams)
	case bedrock.Name:
		return c.buildBedrockProvider(apiKey, headers)
	case google.Name:
		return c.buildGoogleProvider(baseURL, apiKey, headers)
	case "google-vertex":
		return c.buildGoogleVertexProvider(headers, providerCfg.ExtraParams)
	case antigravity.Name:
		return c.buildAntigravityProvider(headers, providerCfg.ExtraParams)
	case anthropicoauth.Name:
		return c.buildAnthropicOAuthProvider(headers, providerCfg.ExtraParams)
	case openaicompat.Name, hyper.Name:
		switch providerCfg.ID {
		case hyper.Name:
			baseURL = hyper.BaseURL() + "/v1"
			headers["x-crush-id"] = event.GetID()
		case string(catwalk.InferenceProviderZAI):
			if providerCfg.ExtraBody == nil {
				providerCfg.ExtraBody = map[string]any{}
			}
			providerCfg.ExtraBody["tool_stream"] = true
		}
		return c.buildOpenaiCompatProvider(baseURL, apiKey, headers, providerCfg.ExtraBody, providerCfg.ID, isSubAgent)
	default:
		return nil, fmt.Errorf("provider type not supported: %q", providerCfg.Type)
	}
}

func isExactoSupported(modelID string) bool {
	supportedModels := []string{
		"moonshotai/kimi-k2-0905",
		"deepseek/deepseek-v3.1-terminus",
		"z-ai/glm-4.6",
		"openai/gpt-oss-120b",
		"qwen/qwen3-coder",
	}
	return slices.Contains(supportedModels, modelID)
}

func (c *coordinator) Cancel(sessionID string) {
	c.currentAgent.Cancel(sessionID)
	c.speculativeMu.Lock()
	if cancel, ok := c.speculativeCancels[sessionID]; ok {
		cancel()
		delete(c.speculativeCancels, sessionID)
	}
	c.speculativeMu.Unlock()
}

func (c *coordinator) CancelAll() {
	c.currentAgent.CancelAll()
	c.speculativeMu.Lock()
	for _, cancel := range c.speculativeCancels {
		cancel()
	}
	c.speculativeCancels = make(map[string]context.CancelFunc)
	c.speculativeMu.Unlock()
}

func (c *coordinator) StartSpeculativeRun(ctx context.Context, sessionID string, prediction string) {
	c.speculativeMu.Lock()
	if cancel, ok := c.speculativeCancels[sessionID]; ok {
		cancel()
	}
	specCtx, specCancel := context.WithCancel(ctx)
	c.speculativeCancels[sessionID] = specCancel
	c.speculativeMu.Unlock()

	go func() {
		defer func() {
			c.speculativeMu.Lock()
			delete(c.speculativeCancels, sessionID)
			c.speculativeMu.Unlock()
		}()

		specID := sessionID + "-speculate"
		if err := c.sessions.PrepareSpeculativeSession(specCtx, sessionID, specID); err != nil {
			slog.Debug("Failed to prepare speculative session", "session", sessionID, "error", err)
			return
		}

		slog.Debug("Starting background speculative run", "session", sessionID, "prediction", prediction)
		if _, err := c.Run(specCtx, specID, prediction, false); err != nil {
			slog.Debug("Speculative run failed or cancelled", "session", sessionID, "error", err)
		} else {
			slog.Debug("Speculative run completed successfully", "session", sessionID)
		}
	}()
}

func (c *coordinator) PromoteSpeculativeSession(ctx context.Context, sessionID string) error {
	c.speculativeMu.Lock()
	if cancel, ok := c.speculativeCancels[sessionID]; ok {
		cancel()
		delete(c.speculativeCancels, sessionID)
	}
	c.speculativeMu.Unlock()

	if err := c.messages.FlushAll(ctx); err != nil {
		slog.Error("Failed to flush messages during promotion", "session", sessionID, "error", err)
	}

	return c.sessions.PromoteSpeculativeSession(ctx, sessionID)
}

func (c *coordinator) ClearQueue(sessionID string) {
	c.currentAgent.ClearQueue(sessionID)
}

func (c *coordinator) IsBusy() bool {
	return c.currentAgent.IsBusy()
}

func (c *coordinator) IsSessionBusy(sessionID string) bool {
	return c.currentAgent.IsSessionBusy(sessionID)
}

func (c *coordinator) Model() Model {
	return c.currentAgent.Model()
}

func (c *coordinator) agentForProfile(profile scheduler.WorkerProfile) SessionAgent {
	if c == nil {
		return nil
	}
	switch profile {
	case scheduler.ProfilePlanAgent:
		if agent, ok := c.agents[config.AgentPlan]; ok {
			return agent
		}
	case scheduler.ProfileWorkerAgent:
		if agent, ok := c.agents[config.AgentWorker]; ok {
			return agent
		}
	case scheduler.ProfileExploreAgent:
		if agent, ok := c.agents[config.AgentExplore]; ok {
			return agent
		}
	default:
		if agent, ok := c.agents[config.AgentBrain]; ok {
			return agent
		}
	}
	return nil
}

func (c *coordinator) UpdateModels(ctx context.Context) error {
	for _, agentName := range []string{config.AgentBrain, config.AgentPlan, config.AgentWorker, config.AgentExplore} {
		agent, ok := c.agents[agentName]
		if !ok || agent == nil {
			continue
		}
		agentCfg, ok := c.cfg.Config().Agents[agentName]
		if !ok {
			continue
		}
		primary, title, err := c.buildAgentModels(ctx, agentCfg, agentName != config.AgentBrain)
		if err != nil {
			return err
		}
		agent.SetModels(primary, title)
		builtTools, deferredRegistry, err := c.buildTools(ctx, agentCfg, agentName != config.AgentBrain)
		if err != nil {
			return err
		}
		agent.SetTools(builtTools)
		agent.SetDeferredRegistry(deferredRegistry)
		if agentName == c.currentAgentName {
			c.currentAgent = agent
		}
	}
	return nil
}

func (c *coordinator) QueuedPrompts(sessionID string) int {
	return c.currentAgent.QueuedPrompts(sessionID)
}

func (c *coordinator) QueuedPromptsList(sessionID string) []string {
	return c.currentAgent.QueuedPromptsList(sessionID)
}

func (c *coordinator) Summarize(ctx context.Context, sessionID string) error {
	providerCfg, ok := c.cfg.Config().Providers.Get(c.currentAgent.Model().ModelCfg.Provider)
	if !ok {
		return errModelProviderNotConfigured
	}

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		slog.Error("Failed to refresh OAuth2 token before summarize. Proceeding with existing token.", "error", err)
	}

	summarize := func() error {
		return c.currentAgent.Summarize(ctx, sessionID, getProviderOptions(sessionID, c.currentAgent.Model(), providerCfg, false))
	}

	err := summarize()
	if err != nil && c.isUnauthorized(err) {
		if retryErr := c.retryAfterUnauthorized(ctx, providerCfg); retryErr == nil {
			return summarize()
		}
	}

	return err
}

// refreshTokenIfExpired proactively refreshes the OAuth token if it has expired.
func (c *coordinator) refreshTokenIfExpired(ctx context.Context, providerCfg config.ProviderConfig) error {
	if providerCfg.OAuthToken == nil || !providerCfg.OAuthToken.IsExpired() {
		return nil
	}
	slog.Debug("Token needs to be refreshed", "provider", providerCfg.ID)
	return c.refreshOAuth2Token(ctx, providerCfg)
}

// retryAfterUnauthorized attempts to refresh credentials after receiving a 401
// and returns nil if retry should be attempted.
func (c *coordinator) retryAfterUnauthorized(ctx context.Context, providerCfg config.ProviderConfig) error {
	switch {
	case providerCfg.OAuthToken != nil:
		slog.Debug("Received 401. Refreshing token and retrying", "provider", providerCfg.ID)
		return c.refreshOAuth2Token(ctx, providerCfg)
	case strings.Contains(providerCfg.APIKeyTemplate, "$"):
		slog.Debug("Received 401. Refreshing API Key template and retrying", "provider", providerCfg.ID)
		return c.refreshApiKeyTemplate(ctx, providerCfg)
	default:
		return nil
	}
}

func (c *coordinator) isUnauthorized(err error) bool {
	var providerErr *fantasy.ProviderError
	return errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized
}

func (c *coordinator) refreshOAuth2Token(ctx context.Context, providerCfg config.ProviderConfig) error {
	if err := c.cfg.RefreshOAuthToken(ctx, providerCfg.ID); err != nil {
		slog.Error("Failed to refresh OAuth token after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}
	if err := c.UpdateModels(ctx); err != nil {
		return err
	}
	return nil
}

func (c *coordinator) refreshApiKeyTemplate(ctx context.Context, providerCfg config.ProviderConfig) error {
	newAPIKey, err := c.cfg.Resolve(providerCfg.APIKeyTemplate)
	if err != nil {
		slog.Error("Failed to re-resolve API key after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}

	providerCfg.APIKey = newAPIKey
	c.cfg.Config().Providers.Set(providerCfg.ID, providerCfg)

	if err := c.UpdateModels(ctx); err != nil {
		return err
	}
	return nil
}

// subAgentParams holds the parameters for running a sub-agent.
type subAgentParams struct {
	Agent          SessionAgent
	SessionID      string
	AgentMessageID string
	ToolCallID     string
	Prompt         string
	Profile        scheduler.WorkerProfile
	SessionTitle   string
	// SessionSetup is an optional callback invoked after session creation
	// but before agent execution, for custom session configuration.
	SessionSetup func(sessionID string)
}

// runSubAgent runs a sub-agent and handles session management and cost accumulation.
// It creates a sub-session, runs the agent with the given prompt, and propagates
// the cost to the parent session.
func (c *coordinator) runSubAgent(ctx context.Context, params subAgentParams) (fantasy.ToolResponse, error) {
	// Create sub-session
	agentToolSessionID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	session, err := c.sessions.CreateTaskSession(ctx, agentToolSessionID, params.SessionID, params.SessionTitle)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("create session: %w", err)
	}

	// Phase 6: notify sidebar/listeners that a sub-agent has been dispatched.
	// All three Started/Finished/Failed events share SubAgentToolCallID so the
	// UI can update a single row in place.
	c.publishSubAgentEvent(notify.TypeSubAgentStarted, params, session.ID, "")

	// Call session setup function if provided
	if params.SessionSetup != nil {
		params.SessionSetup(session.ID)
	}

	// Get model configuration
	model := params.Agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	profile := params.Profile
	if profile == "" {
		profile = scheduler.ProfileWorkerAgent
	}

	taskRuntime := c.newTaskRuntime(session.ID)
	taskScheduler := scheduler.NewAgentScheduler(taskRuntime)
	taskNode := c.ensureChildTask(taskScheduler, params.SessionID, session.ID, params.Prompt, profile, maxTokens)
	if taskNode == nil {
		return fantasy.ToolResponse{}, errors.New("failed to create child task")
	}
	c.preBindTaskTreeModels(taskNode)

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return fantasy.ToolResponse{}, errModelProviderNotConfigured
	}

	var result *fantasy.AgentResult
	taskWorker := scheduler.WorkerFunc(func(taskCtx context.Context, node *scheduler.TaskNode, intent provider.RequestIntent) (string, error) {
		callMaxTokens := maxTokens
		if intent.MaxOutputTokens > 0 {
			callMaxTokens = int64(intent.MaxOutputTokens)
		}
		taskPrompt := c.composeTaskPrompt(taskRuntime, node, params.Prompt)
		c.bindTaskNodeModel(node, model)
		c.appendTaskInputTrace(taskRuntime, node, taskPrompt)
		requestStartedAt := time.Now()
		runResult, runErr := params.Agent.Run(taskCtx, SessionAgentCall{
			SessionID:        session.ID,
			Prompt:           taskPrompt,
			MaxOutputTokens:  callMaxTokens,
			ProviderOptions:  getProviderOptions(session.ID, model, providerCfg, false),
			Temperature:      model.ModelCfg.Temperature,
			TopP:             model.ModelCfg.TopP,
			TopK:             model.ModelCfg.TopK,
			FrequencyPenalty: model.ModelCfg.FrequencyPenalty,
			PresencePenalty:  model.ModelCfg.PresencePenalty,
			NonInteractive:   true,
			TraceRuntime:     taskRuntime,
			TaskNodeID:       node.ID,
			TaskParentID:     nodeParentIDForCall(node),
			TaskProfile:      string(node.Profile),
			ProviderID:       node.ProviderID,
			ProviderType:     node.ProviderType,
			ModelID:          node.ModelID,
		})
		node.FinishedAt = time.Now()
		if node.StartedAt.IsZero() {
			node.StartedAt = requestStartedAt
		}
		node.DurationMs = node.FinishedAt.Sub(node.StartedAt).Milliseconds()
		if runErr != nil {
			if taskRuntime != nil {
				taskRuntime.AppendTrace(agentruntime.TaskTrace{
					StartedAt:             node.StartedAt,
					FinishedAt:            node.FinishedAt,
					DurationMs:            node.DurationMs,
					ConversationSessionID: node.ConversationSessionID,
					SessionID:             node.SessionID,
					NodeID:                node.ID,
					ParentID:              node.Intent.ParentID,
					Depth:                 taskNodeDepth(node),
					Profile:               string(node.Profile),
					ProviderID:            node.ProviderID,
					ProviderType:          node.ProviderType,
					ModelID:               node.ModelID,
					Kind:                  agentruntime.TraceKindTaskFailed,
					Status:                "failed",
					Goal:                  node.Intent.Goal,
					Scope:                 append([]string(nil), node.Intent.Scope...),
					Error:                 runErr.Error(),
				})
			}
			return "", runErr
		}
		if runResult == nil {
			return "", errors.New("sub-agent returned no result")
		}
		result = runResult
		output := runResult.Response.Content.Text()
		c.bindTaskNodeUsage(node, model, runResult.TotalUsage, taskPrompt, output)
		c.appendTaskOutputTrace(taskRuntime, node, output)
		c.recordTaskOutcome(taskRuntime, node, taskPrompt, output)
		return output, nil
	})

	if err := taskScheduler.Dispatch(ctx, taskNode, taskWorker); err != nil {
		c.publishSubAgentEvent(notify.TypeSubAgentFailed, params, session.ID, err.Error())
		c.propagateSubAgentTraces(taskRuntime)
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to generate response: %s", err)), nil
	}
	c.propagateSubAgentTraces(taskRuntime)
	if result == nil {
		c.publishSubAgentEvent(notify.TypeSubAgentFailed, params, session.ID, "sub-agent returned no result")
		return fantasy.ToolResponse{}, errors.New("sub-agent returned no result")
	}

	// Update parent session cost
	if err := c.updateParentSessionCost(ctx, session.ID, params.SessionID); err != nil {
		c.publishSubAgentEvent(notify.TypeSubAgentFailed, params, session.ID, err.Error())
		return fantasy.ToolResponse{}, err
	}

	c.publishSubAgentEvent(notify.TypeSubAgentFinished, params, session.ID, "")
	return fantasy.NewTextResponse(result.Response.Content.Text()), nil
}

// propagateSubAgentTraces copies all trace entries recorded by a sub-agent to
// the parent session's runtime trace so that the final trace dump contains
// them.
func (c *coordinator) propagateSubAgentTraces(subRuntime *agentruntime.RuntimeSession) {
	if subRuntime == nil {
		return
	}
	c.traceMu.RLock()
	parentRuntime := c.lastRuntime
	c.traceMu.RUnlock()
	if parentRuntime == nil {
		return
	}
	for _, entry := range subRuntime.TraceEntries() {
		entry.Sequence = 0
		parentRuntime.AppendTrace(entry)
	}
}

// publishSubAgentEvent fires a Started/Finished/Failed notification for the
// given sub-agent run. Safe to call with nil notify publisher (no-op).
func (c *coordinator) publishSubAgentEvent(t notify.Type, params subAgentParams, sessionID, errText string) {
	if c.notify == nil {
		return
	}
	c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
		SessionID:          sessionID,
		SessionTitle:       params.SessionTitle,
		Type:               t,
		SubAgentToolCallID: params.ToolCallID,
		SubAgentPrompt:     params.Prompt,
		SubAgentProfile:    string(normalizeSubAgentProfile(params.Profile)),
		SubAgentError:      errText,
	})
}

// updateParentSessionCost accumulates the cost from a child session to its parent session.
func (c *coordinator) updateParentSessionCost(ctx context.Context, childSessionID, parentSessionID string) error {
	childSession, err := c.sessions.Get(ctx, childSessionID)
	if err != nil {
		return fmt.Errorf("get child session: %w", err)
	}

	parentSession, err := c.sessions.Get(ctx, parentSessionID)
	if err != nil {
		return fmt.Errorf("get parent session: %w", err)
	}

	parentSession.Cost += childSession.Cost

	if _, err := c.sessions.Save(ctx, parentSession); err != nil {
		return fmt.Errorf("save parent session: %w", err)
	}

	return nil
}

func (c *coordinator) newTaskRuntime(sessionID string) *agentruntime.RuntimeSession {
	if c == nil {
		return nil
	}
	if c.runtime == nil {
		c.runtime = agentruntime.NewSession(c.cfg.WorkingDir(), nil)
	}
	return c.runtime.CloneForRun(sessionID)
}

func (c *coordinator) ensureRootTask(taskScheduler *scheduler.AgentScheduler, sessionID, goal string, maxTokens int64, profile scheduler.WorkerProfile) *scheduler.TaskNode {
	if c == nil || taskScheduler == nil {
		return nil
	}
	profile = normalizeSubAgentProfile(profile)
	if profile == scheduler.ProfileWorkerAgent {
		profile = scheduler.ProfileBrainAgent
	}
	node := taskScheduler.EnsureRoot(sessionID, goal, nil, profile)
	if node == nil {
		return nil
	}
	node.Kind, node.Mode = taskKindAndModeForProfile(profile)
	node.MaxRetries = 0
	node.Intent.BudgetTokens = int(maxTokens)
	return node
}

func (c *coordinator) ensureChildTask(taskScheduler *scheduler.AgentScheduler, parentSessionID, sessionID, goal string, profile scheduler.WorkerProfile, maxTokens int64) *scheduler.TaskNode {
	if c == nil || taskScheduler == nil {
		return nil
	}
	parent, ok := taskScheduler.Root(parentSessionID)
	if !ok || parent == nil {
		parent = taskScheduler.EnsureRoot(parentSessionID, "", nil, scheduler.ProfileBrainAgent)
	}
	profile = normalizeSubAgentProfile(profile)
	node := taskScheduler.SpawnChild(parent, sessionID, goal, profile, nil, "")
	if node == nil {
		return nil
	}
	node.Kind, node.Mode = taskKindAndModeForProfile(profile)
	node.MaxRetries = 0
	node.Intent.BudgetTokens = int(maxTokens)
	return node
}

func normalizeSubAgentProfile(profile scheduler.WorkerProfile) scheduler.WorkerProfile {
	switch profile {
	case scheduler.ProfileExploreAgent, scheduler.ProfilePlanAgent, scheduler.ProfileWorkerAgent:
		return profile
	default:
		return scheduler.ProfileWorkerAgent
	}
}

func taskKindAndModeForProfile(profile scheduler.WorkerProfile) (scheduler.TaskKind, scheduler.TaskMode) {
	switch profile {
	case scheduler.ProfilePlanAgent:
		return scheduler.TaskPlan, scheduler.TaskReadOnly
	case scheduler.ProfileExploreAgent:
		return scheduler.TaskExplore, scheduler.TaskReadOnly
	default:
		return scheduler.TaskEdit, scheduler.TaskWrite
	}
}

func (c *coordinator) composeTaskPrompt(runtime *agentruntime.RuntimeSession, node *scheduler.TaskNode, fallback string) string {
	if node == nil {
		return fallback
	}

	prompt := node.Intent.Goal
	if prompt == "" {
		prompt = fallback
	}
	if runtime == nil {
		return prompt
	}

	switch node.Kind {
	case scheduler.TaskEdit:
		if plan, ok := runtime.Fact("task.plan"); ok && strings.TrimSpace(plan) != "" {
			return prompt + "\n\nImplementation plan:\n" + plan
		}
	case scheduler.TaskVerify:
		if output, ok := runtime.Fact("task.output"); ok && strings.TrimSpace(output) != "" {
			return prompt + "\n\nImplementation output:\n" + output
		}
	}

	return prompt
}

func (c *coordinator) recordTaskOutcome(runtime *agentruntime.RuntimeSession, node *scheduler.TaskNode, prompt, output string) {
	if runtime == nil || node == nil {
		return
	}

	goal := strings.TrimSpace(node.Intent.Goal)
	if goal == "" {
		goal = strings.TrimSpace(prompt)
	}
	if goal != "" {
		runtime.AppendCompactHistory(goal)
	}
	if output != "" {
		runtime.AppendCompactHistory(output)
	}

	switch node.Kind {
	case scheduler.TaskPlan:
		runtime.SetFact("task.plan", output)
	case scheduler.TaskResearch, scheduler.TaskExplore:
		if node.Parent != nil && len(node.Parent.Children) > 1 && node.Parent.Children[0] == node {
			runtime.SetFact("task.plan", output)
		} else {
			runtime.SetFact("task.insight", output)
		}
	case scheduler.TaskEdit:
		runtime.SetFact("task.output", output)
	case scheduler.TaskVerify:
		runtime.SetFact("task.verify", output)
	case scheduler.TaskSummarize:
		runtime.SetFact("task.summary", output)
	}
}

// preBindTaskTreeModels walks the task tree before Dispatch and binds the
// model attached to each node's profile, so task_started traces carry
// provider/model/request_id rather than empty strings. The worker's per-call
// bindTaskNodeUsage still owns token/cost numbers populated after the LLM
// response returns.
func (c *coordinator) preBindTaskTreeModels(node *scheduler.TaskNode) {
	if node == nil {
		return
	}
	if node.Profile != "" {
		if agent := c.agentForProfile(node.Profile); agent != nil {
			c.bindTaskNodeModel(node, agent.Model())
		}
	}
	for _, child := range node.Children {
		c.preBindTaskTreeModels(child)
	}
}

func (c *coordinator) bindTaskNodeModel(node *scheduler.TaskNode, model Model) {
	if node == nil {
		return
	}
	node.ProviderID = model.ModelCfg.Provider
	node.ProviderType = string(model.ProviderType)
	node.ModelID = model.ModelCfg.Model
	node.RequestID = node.ID
}

func (c *coordinator) bindTaskNodeUsage(node *scheduler.TaskNode, model Model, usage fantasy.Usage, input, output string) {
	if node == nil {
		return
	}
	node.InputBytes = len(input)
	node.OutputBytes = len(output)
	node.InputTokens = usage.InputTokens
	node.OutputTokens = usage.OutputTokens
	node.TotalTokens = usage.TotalTokens
	node.ReasoningTokens = usage.ReasoningTokens
	node.CacheCreationTokens = usage.CacheCreationTokens
	node.CacheReadTokens = usage.CacheReadTokens
	node.EstimatedCostUSD = estimateUsageCost(model, usage)
}

func (c *coordinator) appendTaskInputTrace(runtime *agentruntime.RuntimeSession, node *scheduler.TaskNode, input string) {
	if runtime == nil || node == nil {
		return
	}
	runtime.AppendTrace(agentruntime.TaskTrace{
		StartedAt:             node.StartedAt,
		ConversationSessionID: node.ConversationSessionID,
		SessionID:             node.SessionID,
		NodeID:                node.ID,
		ParentID:              nodeParentIDForCall(node),
		Depth:                 taskNodeDepth(node),
		Profile:               string(node.Profile),
		ProviderID:            node.ProviderID,
		ProviderType:          node.ProviderType,
		ModelID:               node.ModelID,
		RequestID:             node.RequestID,
		Kind:                  agentruntime.TraceKindTaskInput,
		Status:                "dispatching",
		Goal:                  node.Intent.Goal,
		Scope:                 append([]string(nil), node.Intent.Scope...),
		Input:                 input,
		InputBytes:            len(input),
	})
}

func (c *coordinator) appendTaskOutputTrace(runtime *agentruntime.RuntimeSession, node *scheduler.TaskNode, output string) {
	if runtime == nil || node == nil {
		return
	}
	runtime.AppendTrace(agentruntime.TaskTrace{
		StartedAt:             node.StartedAt,
		FinishedAt:            node.FinishedAt,
		DurationMs:            node.DurationMs,
		ConversationSessionID: node.ConversationSessionID,
		SessionID:             node.SessionID,
		NodeID:                node.ID,
		ParentID:              nodeParentIDForCall(node),
		Depth:                 taskNodeDepth(node),
		Profile:               string(node.Profile),
		ProviderID:            node.ProviderID,
		ProviderType:          node.ProviderType,
		ModelID:               node.ModelID,
		RequestID:             node.RequestID,
		Kind:                  agentruntime.TraceKindTaskOutput,
		Status:                "completed",
		Success:               true,
		Goal:                  node.Intent.Goal,
		Scope:                 append([]string(nil), node.Intent.Scope...),
		Output:                output,
		OutputBytes:           len(output),
		InputTokens:           node.InputTokens,
		OutputTokens:          node.OutputTokens,
		TotalTokens:           node.TotalTokens,
		ReasoningTokens:       node.ReasoningTokens,
		CacheCreationTokens:   node.CacheCreationTokens,
		CacheReadTokens:       node.CacheReadTokens,
		EstimatedCostUSD:      node.EstimatedCostUSD,
	})
}

func estimateUsageCost(model Model, usage fantasy.Usage) float64 {
	if model.FlatRate {
		return 0
	}
	modelConfig := model.CatwalkCfg
	return modelConfig.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(usage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(usage.OutputTokens)
}

func nodeParentIDForCall(node *scheduler.TaskNode) string {
	if node == nil {
		return ""
	}
	if node.Parent != nil {
		return node.Parent.ID
	}
	return node.Intent.ParentID
}

func taskNodeDepth(node *scheduler.TaskNode) int {
	depth := 0
	for current := node; current != nil && current.Parent != nil; current = current.Parent {
		depth++
	}
	return depth
}

// discoverSkills runs the skill discovery pipeline and returns both the
// pre-filter (all discovered, after dedup) and post-filter (active) lists.
// It also emits a single diagnostic log line summarising the outcome to
// help track skill-loading health over time.
func discoverSkills(cfg *config.ConfigStore) (allSkills, activeSkills []*skills.Skill) {
	builtin, builtinStates := skills.DiscoverBuiltinWithStates()
	discovered := append([]*skills.Skill(nil), builtin...)

	var userStates []*skills.SkillState
	var userPaths []string

	opts := cfg.Config().Options
	if opts != nil && len(opts.SkillsPaths) > 0 {
		userPaths = make([]string, 0, len(opts.SkillsPaths))
		for _, pth := range opts.SkillsPaths {
			expanded := home.Long(pth)
			if strings.HasPrefix(expanded, "$") {
				if resolved, err := cfg.Resolver().ResolveValue(expanded); err == nil {
					expanded = resolved
				}
			}
			userPaths = append(userPaths, expanded)
		}
		var userSkills []*skills.Skill
		userSkills, userStates = skills.DiscoverWithStates(userPaths)
		discovered = append(discovered, userSkills...)
	}

	allSkills = skills.Deduplicate(discovered)
	var disabledSkills []string
	if opts != nil {
		disabledSkills = opts.DisabledSkills
	}
	activeSkills = skills.Filter(allSkills, disabledSkills)

	allStates := append([]*skills.SkillState(nil), builtinStates...)
	allStates = append(allStates, userStates...)

	allStates = skills.DeduplicateStates(allStates)

	slices.SortStableFunc(allStates, func(a, b *skills.SkillState) int {
		return strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
	})
	skills.SetLatestStates(allStates)
	skills.PublishStates(allStates)

	logDiscoveryStats(builtin, builtinStates, userStates, userPaths, allSkills, activeSkills, disabledSkills)
	return allSkills, activeSkills
}

// logTurnSkillUsage emits a per-turn diagnostic line showing which skills
// (if any) were loaded during this turn and which looked relevant based on
// a cheap keyword match against the user prompt. The goal is to surface
// "should-have-loaded but didn't" situations for later analysis.
//
// Logged at Info level under component=skills; heavy fields are elided when
// there is nothing interesting to report.
func logTurnSkillUsage(
	sessionID string,
	prompt string,
	activeSkills []*skills.Skill,
	tracker *skills.Tracker,
	before []string,
) {
	if tracker == nil || len(activeSkills) == 0 {
		return
	}

	after := tracker.LoadedNames()

	beforeSet := make(map[string]bool, len(before))
	for _, n := range before {
		beforeSet[n] = true
	}
	var loadedThisTurn []string
	for _, n := range after {
		if !beforeSet[n] {
			loadedThisTurn = append(loadedThisTurn, n)
		}
	}

	slog.Info(
		"Skill turn summary",
		"component", "skills",
		"session_id", sessionID,
		"prompt_len", len(prompt),
		"active_total", len(activeSkills),
		"loaded_total", len(after),
		"loaded_this_turn", loadedThisTurn,
	)
}

// logDiscoveryStats emits a single structured log line summarising skill
// discovery for the current session. It is intentionally low-volume: one
// line per session start.
func logDiscoveryStats(
	builtin []*skills.Skill,
	builtinStates, userStates []*skills.SkillState,
	userPaths []string,
	allSkills, activeSkills []*skills.Skill,
	disabled []string,
) {
	countErrors := func(states []*skills.SkillState) int {
		n := 0
		for _, s := range states {
			if s.State == skills.StateError {
				n++
			}
		}
		return n
	}

	userOK := 0
	for _, s := range userStates {
		if s.State == skills.StateNormal {
			userOK++
		}
	}

	activeNames := make([]string, 0, len(activeSkills))
	for _, s := range activeSkills {
		activeNames = append(activeNames, s.Name)
	}

	xml := skills.ToPromptXML(activeSkills)

	slog.Info(
		"Skill discovery complete",
		"component", "skills",
		"builtin_ok", len(builtin),
		"builtin_errors", countErrors(builtinStates),
		"user_ok", userOK,
		"user_errors", countErrors(userStates),
		"user_paths", len(userPaths),
		"deduped_total", len(allSkills),
		"active", len(activeSkills),
		"disabled", len(disabled),
		"prompt_bytes", len(xml),
		"prompt_tok_est", skills.ApproxTokenCount(xml),
		"active_names", activeNames,
	)
}

func (c *coordinator) triggerMemoryExtraction(ctx context.Context, sessionID string) {
	exploreAgent := c.agentForProfile(scheduler.ProfileExploreAgent)
	if exploreAgent == nil {
		slog.Debug("Skipping memory extraction — explore agent not configured")
		return
	}

	msgs, err := c.messages.List(ctx, sessionID)
	if err != nil {
		slog.Error("Failed to list messages for memory extraction", "session", sessionID, "error", err)
		return
	}

	memoryDir := filepath.Join(c.cfg.Config().Options.DataDirectory, "projects", memdir.WorkspaceSlug(c.cfg.WorkingDir()), "memory")

	if hasMemoryWrites(msgs, memoryDir) {
		slog.Debug("Skipping memory extraction — turn already wrote memories", "session", sessionID)
		return
	}

	specID := sessionID + "-mem-extract"
	if err := c.sessions.PrepareSpeculativeSession(ctx, sessionID, specID); err != nil {
		slog.Debug("Failed to prepare memory extraction session", "session", sessionID, "error", err)
		return
	}

	extractionPrompt := `You are a background memory extraction agent. Your job is to extract durable learning and project-specific setup/preferences from the conversation and update the workspace memories under the memory/ folder.
Identify:
1. Build, test, and lint commands that were run or defined.
2. Custom settings or developer preferences.
3. Code patterns, conventions, or design decisions discussed or implemented.
4. Open risks, key files to watch, or next steps.

Use the ` + "`glob`" + `, ` + "`grep`" + `, ` + "`view`" + `, ` + "`write`" + `, and ` + "`edit`" + ` tools to inspect the memory directory and MEMORY.md and write or update the memories.
Only write files under the memory directory. Do not make any edits or writes outside of the memory directory. Do not use bash tools to run mutating commands.
If there are no new learnings, setup, or preferences to record, do not write any files and simply reply.`

	slog.Debug("Starting background memory extraction", "session", sessionID)

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	_, err = exploreAgent.Run(runCtx, SessionAgentCall{
		SessionID: specID,
		Prompt:    extractionPrompt,
	})
	if err != nil {
		slog.Debug("Background memory extraction finished with error", "session", sessionID, "error", err)
	} else {
		slog.Debug("Background memory extraction finished successfully", "session", sessionID)
	}

	_ = c.sessions.Delete(runCtx, specID)
}

func hasMemoryWrites(msgs []message.Message, memoryDir string) bool {
	cleanMemDir := filepath.Clean(memoryDir)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == message.User {
			break
		}
		if m.Role != message.Assistant {
			continue
		}
		for _, tc := range m.ToolCalls() {
			if tc.Name == "write" || tc.Name == "edit" || tc.Name == "multiedit" {
				var input struct {
					FilePath string `json:"file_path"`
					Path     string `json:"path"`
				}
				if err := json.Unmarshal([]byte(tc.Input), &input); err == nil {
					fp := input.FilePath
					if fp == "" {
						fp = input.Path
					}
					if fp != "" {
						cleanPath := filepath.Clean(fp)
						if strings.HasPrefix(cleanPath, cleanMemDir) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}
