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
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/provider"
	"github.com/charmbracelet/crush/internal/pubsub"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"golang.org/x/sync/errgroup"

	"charm.land/fantasy/providers/anthropic"
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
var (
	errCoderAgentNotConfigured           = errors.New("coder agent not configured")
	errModelProviderNotConfigured        = errors.New("model provider not configured")
	errBuildModelNotSelected             = errors.New("build model not selected")
	errCoderModelNotSelected             = errors.New("coder model not selected")
	errExploreModelNotSelected           = errors.New("explore model not selected")
	errLargeModelNotSelected             = errors.New("large model not selected")
	errSmallModelNotSelected             = errors.New("small model not selected")
	errBuildModelProviderNotConfigured   = errors.New("build model provider not configured")
	errCoderModelProviderNotConfigured   = errors.New("coder model provider not configured")
	errExploreModelProviderNotConfigured = errors.New("explore model provider not configured")
	errLargeModelProviderNotConfigured   = errors.New("large model provider not configured")
	errSmallModelProviderNotConfigured   = errors.New("small model provider not configured")
	errBuildModelNotFound                = errors.New("build model not found in provider config")
	errCoderModelNotFound                = errors.New("coder model not found in provider config")
	errExploreModelNotFound              = errors.New("explore model not found in provider config")
	errLargeModelNotFound                = errors.New("large model not found in provider config")
	errSmallModelNotFound                = errors.New("small model not found in provider config")
)

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
	Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
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
}

type coordinator struct {
	cfg         *config.ConfigStore
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	history     history.Service
	filetracker filetracker.Service
	lspManager  *lsp.Manager
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

	readyWg errgroup.Group
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
		notify:       notify,
		agents:       make(map[string]SessionAgent),
		allSkills:    allSkills,
		activeSkills: activeSkills,
		skillTracker: skillTracker,
		runtime:      agentruntime.NewSession(cfg.WorkingDir(), nil),
	}

	agentName := config.AgentBuild
	agentCfg, ok := cfg.Config().Agents[agentName]
	if !ok {
		agentName = config.AgentBuild
		agentCfg, ok = cfg.Config().Agents[agentName]
	}
	if !ok {
		return nil, errCoderAgentNotConfigured
	}

	systemPrompt, err := promptForAgentRole(agentName, prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}

	agent, err := c.buildAgent(ctx, systemPrompt, agentCfg, false)
	if err != nil {
		return nil, err
	}
	c.currentAgent = agent
	c.currentAgentName = agentName
	c.agents[config.AgentBuild] = agent

	if coderCfg, ok := cfg.Config().Agents[config.AgentCoder]; ok {
		coderSystemPrompt, err := promptForAgentRole(config.AgentCoder, prompt.WithWorkingDir(c.cfg.WorkingDir()))
		if err != nil {
			return nil, err
		}
		coderAgent, err := c.buildAgent(ctx, coderSystemPrompt, coderCfg, true)
		if err != nil {
			return nil, err
		}
		c.agents[config.AgentCoder] = coderAgent
	}

	if exploreCfg, ok := cfg.Config().Agents[config.AgentExplore]; ok {
		exploreSystemPrompt, err := promptForAgentRole(config.AgentExplore, prompt.WithWorkingDir(c.cfg.WorkingDir()))
		if err != nil {
			return nil, err
		}
		exploreAgent, err := c.buildAgent(ctx, exploreSystemPrompt, exploreCfg, true)
		if err != nil {
			return nil, err
		}
		c.agents[config.AgentExplore] = exploreAgent
	}
	return c, nil
}

// Run implements Coordinator.
func (c *coordinator) Run(ctx context.Context, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	if err := c.readyWg.Wait(); err != nil {
		return nil, err
	}

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

	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(model, providerCfg)

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		// NOTE(@andreynering): We don't return here because the event handling to ask the user to reauthenticate
		// depends on the flow below. If refresh fails, proceed with the token we have.
		slog.Error("Failed to refresh OAuth2 token. Proceeding with existing token.", "error", err)
	}

	run := func() (*fantasy.AgentResult, error) {
		taskRuntime := c.newTaskRuntime(sessionID)
		c.setLastRuntime(taskRuntime)
		taskScheduler := scheduler.NewAgentScheduler(taskRuntime)
		taskNode := c.ensureRootTask(taskScheduler, sessionID, prompt, maxTokens)
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
				Attachments:      attachments,
				MaxOutputTokens:  callMaxTokens,
				ProviderOptions:  mergedOptions,
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
				return "", errors.New("agent returned no result")
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

func getProviderOptions(model Model, providerCfg config.ProviderConfig) fantasy.ProviderOptions {
	options := fantasy.ProviderOptions{}

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
		if !hasReasoningEffort && model.ModelCfg.ReasoningEffort != "" && model.CatwalkCfg.CanReason {
			mergedOptions["reasoning_effort"] = model.ModelCfg.ReasoningEffort
		}
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
		var (
			_, hasEffort = mergedOptions["effort"]
			_, hasThink  = mergedOptions["thinking"]
		)
		switch {
		case !hasEffort && model.ModelCfg.ReasoningEffort != "" && model.CatwalkCfg.CanReason:
			mergedOptions["effort"] = model.ModelCfg.ReasoningEffort
		case !hasThink && model.ModelCfg.Think:
			mergedOptions["thinking"] = map[string]any{"budget_tokens": 2000}
		}
		parsed, err := anthropic.ParseOptions(mergedOptions)
		if err == nil {
			options[anthropic.Name] = parsed
		}

	case openrouter.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && model.ModelCfg.ReasoningEffort != "" {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  model.ModelCfg.ReasoningEffort,
			}
		}
		parsed, err := openrouter.ParseOptions(mergedOptions)
		if err == nil {
			options[openrouter.Name] = parsed
		}
	case vercel.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && model.ModelCfg.ReasoningEffort != "" {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  model.ModelCfg.ReasoningEffort,
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
					"thinking_level":   model.ModelCfg.ReasoningEffort,
					"include_thoughts": true,
				}
			}
		}
		parsed, err := google.ParseOptions(mergedOptions)
		if err == nil {
			options[google.Name] = parsed
		}
	case openaicompat.Name, hyper.Name:
		extraBody := make(map[string]any)

		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && model.ModelCfg.ReasoningEffort != "" && model.CatwalkCfg.CanReason {
			switch providerCfg.ID {
			case string(catwalk.InferenceProviderIoNet):
				extraBody["reasoning"] = map[string]string{"effort": model.ModelCfg.ReasoningEffort}
			default:
				mergedOptions["reasoning_effort"] = model.ModelCfg.ReasoningEffort
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
			if model.ModelCfg.Think || model.ModelCfg.ReasoningEffort != "" {
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

func mergeCallOptions(model Model, cfg config.ProviderConfig) (fantasy.ProviderOptions, *float64, *float64, *int64, *float64, *float64) {
	modelOptions := getProviderOptions(model, cfg)
	temp := cmp.Or(model.ModelCfg.Temperature, model.CatwalkCfg.Options.Temperature)
	topP := cmp.Or(model.ModelCfg.TopP, model.CatwalkCfg.Options.TopP)
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	freqPenalty := cmp.Or(model.ModelCfg.FrequencyPenalty, model.CatwalkCfg.Options.FrequencyPenalty)
	presPenalty := cmp.Or(model.ModelCfg.PresencePenalty, model.CatwalkCfg.Options.PresencePenalty)
	return modelOptions, temp, topP, topK, freqPenalty, presPenalty
}

func (c *coordinator) buildAgent(ctx context.Context, prompt *prompt.Prompt, agent config.Agent, isSubAgent bool) (SessionAgent, error) {
	large, small, err := c.buildAgentModels(ctx, agent, isSubAgent)
	if err != nil {
		return nil, err
	}

	largeProviderCfg, _ := c.cfg.Config().Providers.Get(large.ModelCfg.Provider)
	result := NewSessionAgent(SessionAgentOptions{
		LargeModel:           large,
		SmallModel:           small,
		SystemPromptPrefix:   largeProviderCfg.SystemPromptPrefix,
		SystemPrompt:         "",
		IsSubAgent:           isSubAgent,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		IsYolo:               c.permissions.SkipRequests(),
		Sessions:             c.sessions,
		Messages:             c.messages,
		Tools:                nil,
		Notify:               c.notify,
	})

	systemPrompt, err := prompt.Build(ctx, large.Model.Provider(), large.Model.Model(), c.cfg)
	if err != nil {
		return nil, err
	}
	result.SetSystemPrompt(systemPrompt)

	tools, err := c.buildTools(ctx, agent, isSubAgent)
	if err != nil {
		return nil, err
	}
	result.SetTools(tools)

	return result, nil
}

func (c *coordinator) buildTools(ctx context.Context, agent config.Agent, isSubAgent bool) ([]fantasy.AgentTool, error) {
	var allTools []fantasy.AgentTool
	if slices.Contains(agent.AllowedTools, AgentToolName) {
		agentTool, err := c.agentTool(ctx)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agentTool)
	}

	if slices.Contains(agent.AllowedTools, tools.AgenticFetchToolName) {
		agenticFetchTool, err := c.agenticFetchTool(ctx, nil)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agenticFetchTool)
	}

	// Get the model name for the agent
	modelID := ""
	if modelCfg, ok := c.cfg.Config().SelectedModelForType(agent.Model); ok {
		if model := c.cfg.Config().GetModel(modelCfg.Provider, modelCfg.Model); model != nil {
			modelID = model.ID
		}
	}

	logFile := filepath.Join(c.cfg.Config().Options.DataDirectory, "logs", "crush.log")

	// Build hook runner if PreToolUse hooks are configured.
	var hookRunner *hooks.Runner
	if preToolHooks := c.cfg.Config().Hooks[hooks.EventPreToolUse]; len(preToolHooks) > 0 {
		hookRunner = hooks.NewRunner(preToolHooks, c.cfg.WorkingDir(), c.cfg.WorkingDir())
	}

	allTools = append(
		allTools,
		tools.NewBashTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Options.Attribution, modelID),
		tools.NewCommandDAGTool(c.cfg.WorkingDir()),
		tools.NewCrushInfoTool(c.cfg, c.lspManager, c.allSkills, c.activeSkills, c.skillTracker),
		tools.NewCrushLogsTool(logFile),
		tools.NewJobOutputTool(),
		tools.NewJobKillTool(),
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

	for _, tool := range tools.GetMCPTools(c.permissions, c.cfg, c.cfg.WorkingDir()) {
		if agent.AllowedMCP == nil {
			// No MCP restrictions
			filteredTools = append(filteredTools, tool)
			continue
		}
		if len(agent.AllowedMCP) == 0 {
			// No MCPs allowed
			slog.Debug("No MCPs allowed", "tool", tool.Name(), "agent", agent.Name)
			break
		}

		for mcp, tools := range agent.AllowedMCP {
			if mcp != tool.MCP() {
				continue
			}
			if len(tools) == 0 || slices.Contains(tools, tool.MCPToolName()) {
				filteredTools = append(filteredTools, tool)
				break
			}
			slog.Debug("MCP not allowed", "tool", tool.Name(), "agent", agent.Name)
		}
	}
	slices.SortFunc(filteredTools, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})

	// Wrap tools with hook interception for the top-level agent only.
	// Sub-agents (the `agent` task tool, `agentic_fetch`, etc.) run
	// without hook interception to avoid firing the user's hook N times
	// per delegated turn. The top-level invocation of the sub-agent tool
	// itself is still wrapped from the coder's side.
	filteredTools = wrapToolsWithHooks(filteredTools, hookRunner, isSubAgent)

	if c.runtime != nil {
		for _, tool := range filteredTools {
			c.runtime.RegisterTool(tool.Info().Name)
		}
	}

	return filteredTools, nil
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

	large, err := c.buildModelForType(ctx, primaryType, isSubAgent)
	if err != nil {
		return Model{}, Model{}, err
	}
	small, err := c.buildModelForType(ctx, secondaryType, true)
	if err != nil {
		return Model{}, Model{}, err
	}
	return large, small, nil
}

func selectedModelTypeForAgent(agentID string) config.SelectedModelType {
	switch agentID {
	case config.AgentBuild:
		return config.SelectedModelTypeBuild
	case config.AgentCoder:
		return config.SelectedModelTypeCoder
	case config.AgentExplore:
		return config.SelectedModelTypeExplore
	default:
		return config.SelectedModelTypeBuild
	}
}

func (c *coordinator) buildModelForType(ctx context.Context, modelType config.SelectedModelType, isSubAgent bool) (Model, error) {
	selectedModelCfg, ok := c.cfg.Config().SelectedModelForType(modelType)
	if !ok {
		switch modelType {
		case config.SelectedModelTypeBuild:
			return Model{}, errBuildModelNotSelected
		case config.SelectedModelTypeCoder:
			return Model{}, errCoderModelNotSelected
		case config.SelectedModelTypeExplore, config.SelectedModelTypeSmall:
			return Model{}, errExploreModelNotSelected
		default:
			return Model{}, errBuildModelNotSelected
		}
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(selectedModelCfg.Provider)
	if !ok {
		switch modelType {
		case config.SelectedModelTypeBuild:
			return Model{}, errBuildModelProviderNotConfigured
		case config.SelectedModelTypeCoder:
			return Model{}, errCoderModelProviderNotConfigured
		case config.SelectedModelTypeExplore, config.SelectedModelTypeSmall:
			return Model{}, errExploreModelProviderNotConfigured
		default:
			return Model{}, errBuildModelProviderNotConfigured
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
		case config.SelectedModelTypeBuild:
			return Model{}, errBuildModelNotFound
		case config.SelectedModelTypeCoder:
			return Model{}, errCoderModelNotFound
		case config.SelectedModelTypeExplore, config.SelectedModelTypeSmall:
			return Model{}, errExploreModelNotFound
		default:
			return Model{}, errBuildModelNotFound
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

	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, anthropic.WithHTTPClient(httpClient))
	}
	return anthropic.New(opts...)
}

func (c *coordinator) buildOpenaiProvider(baseURL, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []openai.Option{
		openai.WithAPIKey(apiKey),
		openai.WithUseResponsesAPI(),
	}
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
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
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
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
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
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
	} else if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
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
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
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
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
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
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, google.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, google.WithHeaders(headers))
	}
	return google.New(opts...)
}

func (c *coordinator) buildGoogleVertexProvider(headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []google.Option{}
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
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
}

func (c *coordinator) CancelAll() {
	c.currentAgent.CancelAll()
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
	case scheduler.ProfileWorkerAgent:
		if agent, ok := c.agents[config.AgentCoder]; ok {
			return agent
		}
	case scheduler.ProfileToolsAgent:
		if agent, ok := c.agents[config.AgentExplore]; ok {
			return agent
		}
	default:
		if agent, ok := c.agents[config.AgentBuild]; ok {
			return agent
		}
	}
	return c.currentAgent
}

func (c *coordinator) UpdateModels(ctx context.Context) error {
	for _, agentName := range []string{config.AgentBuild, config.AgentCoder, config.AgentExplore} {
		agent, ok := c.agents[agentName]
		if !ok || agent == nil {
			continue
		}
		agentCfg, ok := c.cfg.Config().Agents[agentName]
		if !ok {
			continue
		}
		large, small, err := c.buildAgentModels(ctx, agentCfg, agentName != config.AgentBuild)
		if err != nil {
			return err
		}
		agent.SetModels(large, small)
		tools, err := c.buildTools(ctx, agentCfg, agentName != config.AgentBuild)
		if err != nil {
			return err
		}
		agent.SetTools(tools)
		if agentName == config.AgentBuild {
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
		return c.currentAgent.Summarize(ctx, sessionID, getProviderOptions(c.currentAgent.Model(), providerCfg))
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
	if err := c.cfg.RefreshOAuthToken(ctx, config.ScopeGlobal, providerCfg.ID); err != nil {
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

	taskRuntime := c.newTaskRuntime(session.ID)
	taskScheduler := scheduler.NewAgentScheduler(taskRuntime)
	taskNode := c.ensureChildTask(taskScheduler, params.SessionID, session.ID, params.Prompt, maxTokens)
	if taskNode == nil {
		return fantasy.ToolResponse{}, errors.New("failed to create child task")
	}

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
			ProviderOptions:  getProviderOptions(model, providerCfg),
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
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to generate response: %s", err)), nil
	}
	if result == nil {
		return fantasy.ToolResponse{}, errors.New("sub-agent returned no result")
	}

	// Update parent session cost
	if err := c.updateParentSessionCost(ctx, session.ID, params.SessionID); err != nil {
		return fantasy.ToolResponse{}, err
	}

	return fantasy.NewTextResponse(result.Response.Content.Text()), nil
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

func (c *coordinator) ensureRootTask(taskScheduler *scheduler.AgentScheduler, sessionID, goal string, maxTokens int64) *scheduler.TaskNode {
	if c == nil || taskScheduler == nil {
		return nil
	}
	node := taskScheduler.EnsureRoot(sessionID, goal, nil, scheduler.ProfileBuildAgent)
	if node == nil {
		return nil
	}
	node.Kind = scheduler.TaskEdit
	node.Mode = scheduler.TaskWrite
	node.MaxRetries = 0
	node.Intent.BudgetTokens = int(maxTokens)
	return node
}

func (c *coordinator) ensureChildTask(taskScheduler *scheduler.AgentScheduler, parentSessionID, sessionID, goal string, maxTokens int64) *scheduler.TaskNode {
	if c == nil || taskScheduler == nil {
		return nil
	}
	parent, ok := taskScheduler.Root(parentSessionID)
	if !ok || parent == nil {
		parent = taskScheduler.EnsureRoot(parentSessionID, "", nil, scheduler.ProfileBuildAgent)
	}
	node := taskScheduler.SpawnChild(parent, sessionID, goal, scheduler.ProfileWorkerAgent, nil, "")
	if node == nil {
		return nil
	}
	node.Kind = scheduler.TaskEdit
	node.Mode = scheduler.TaskWrite
	node.MaxRetries = 0
	node.Intent.BudgetTokens = int(maxTokens)
	return node
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
