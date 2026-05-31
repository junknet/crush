package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// -----------------------------------------------------------------------------
// Agent Tool
// -----------------------------------------------------------------------------

// NestedToolContainer is an interface for tool items that can contain nested tool calls.
type NestedToolContainer interface {
	NestedTools() []ToolMessageItem
	SetNestedTools(tools []ToolMessageItem)
	AddNestedTool(tool ToolMessageItem)
}

// AgentToolMessageItem is a message item that represents an agent tool call.
type AgentToolMessageItem struct {
	*baseToolMessageItem

	nestedTools []ToolMessageItem
}

var (
	_ ToolMessageItem     = (*AgentToolMessageItem)(nil)
	_ NestedToolContainer = (*AgentToolMessageItem)(nil)
)

// NewAgentToolMessageItem creates a new [AgentToolMessageItem].
func NewAgentToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) *AgentToolMessageItem {
	t := &AgentToolMessageItem{}
	t.baseToolMessageItem = newBaseToolMessageItem(sty, toolCall, result, &AgentToolRenderContext{agent: t}, canceled)
	// For the agent tool we keep spinning until the tool call is finished.
	t.spinningFunc = func(state SpinningState) bool {
		return !state.HasResult() && !state.IsCanceled()
	}
	return t
}

// Animate progresses the message animation if it should be spinning.
//
// Bumps the parent's F6 list-cache version on both the parent-tick and
// nested-tick branches. Nested tools are not list entries of their
// own — their IDs map to this parent's index in idInxMap
// (internal/ui/model/chat.go:240-246) and their renders are embedded
// inline in this parent's output — so the list only checks the
// parent's version. Without the bump, the list cache would serve the
// previously rendered frame indefinitely and the spinner would appear
// frozen.
func (a *AgentToolMessageItem) Animate(msg StepMsg) tea.Cmd {
	if a.result != nil || a.Status() == ToolStatusCanceled {
		return nil
	}
	if msg.ID == a.ID() {
		a.Bump()
		return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
			return StepMsg{ID: a.ID()}
		})
	}
	for _, nestedTool := range a.nestedTools {
		if msg.ID != nestedTool.ID() {
			continue
		}
		if s, ok := nestedTool.(Animatable); ok {
			a.Bump()
			return s.Animate(msg)
		}
	}
	return nil
}

// NestedTools returns the nested tools.
func (a *AgentToolMessageItem) NestedTools() []ToolMessageItem {
	return a.nestedTools
}

// SetNestedTools sets the nested tools.
//
// SetNestedTools always bumps the version. The previous design
// deduped when the slice's length and element pointers were
// unchanged, but the live update path in internal/ui/model/ui.go
// mutates existing children in place (SetToolCall / SetResult on the
// same pointers) and then calls SetNestedTools with the same slice.
// Pointer-equality dedupe in that case skips the parent Bump even
// though the parent's rendered output (which embeds the children
// inline) has changed, leaving a stale parent entry in the list
// cache. Always bumping is cheap (one uint64 increment) and called
// at most once per agent event; in the rare case the slice is
// truly unchanged the worst case is one extra parent re-render
// while every child cache hit stays warm.
func (a *AgentToolMessageItem) SetNestedTools(tools []ToolMessageItem) {
	a.nestedTools = tools
	a.clearCache()
	a.Bump()
}

// AddNestedTool adds a nested tool.
func (a *AgentToolMessageItem) AddNestedTool(tool ToolMessageItem) {
	// Mark nested tools as simple (compact) rendering.
	if s, ok := tool.(Compactable); ok {
		s.SetCompact(true)
	}
	a.nestedTools = append(a.nestedTools, tool)
	a.clearCache()
	a.Bump()
}

// AgentToolRenderContext renders agent tool messages.
type AgentToolRenderContext struct {
	agent *AgentToolMessageItem
}

// RenderTool implements the [ToolRenderer] interface.
func (r *AgentToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)

	var params agent.AgentParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
	agentName := getAgentRoleName(params.Role)

	role := strings.ToLower(strings.TrimSpace(params.Role))
	if role == "" {
		role = "explore"
	}
	elapsed := runningDurationText(opts.StartedAt)
	elapsedSuffix := ""
	if elapsed != "" {
		elapsedSuffix = " · " + elapsed
	}
	var headerText string
	switch opts.Status {
	case ToolStatusRunning:
		headerText = fmt.Sprintf("Running %s agent%s", role, elapsedSuffix)
	case ToolStatusError:
		headerText = fmt.Sprintf("%s agent failed", role)
	case ToolStatusCanceled:
		headerText = fmt.Sprintf("%s agent canceled", role)
	default:
		headerText = fmt.Sprintf("%s agent finished", role)
	}

	header := toolHeader(sty, opts.Status, headerText, cappedWidth, opts.Compact)
	if opts.Compact {
		return header
	}

	// Tree branch formatting (dimmed)
	treeChar := "└─ "
	dimmedTree := sty.Tool.AgentPrompt.Render(treeChar)

	// Agent tag with specific coloring or default
	tagStyle := sty.Tool.AgentWorkTag
	if role == "plan" {
		tagStyle = tagStyle.Background(lipgloss.Color("#2ecc71")).Foreground(lipgloss.Color("#ffffff"))
	} else if role == "explore" {
		tagStyle = tagStyle.Background(lipgloss.Color("#3498db")).Foreground(lipgloss.Color("#ffffff"))
	} else if role == "auditor" {
		tagStyle = tagStyle.Background(lipgloss.Color("#9b59b6")).Foreground(lipgloss.Color("#ffffff"))
	}

	dimLine := !opts.HasResult() && !opts.IsCanceled()
	if dimLine {
		tagStyle = tagStyle.Faint(true)
	}
	taskTag := tagStyle.Render(agentName)

	// Truncate prompt/description to single line
	promptSingleLine := strings.ReplaceAll(params.Prompt, "\n", " ")
	promptSingleLine = strings.TrimSpace(promptSingleLine)

	indentWidth := 3 + 3 + lipgloss.Width(taskTag)
	remainingWidth := cappedWidth - indentWidth - 25
	if remainingWidth < 15 {
		remainingWidth = 15
	}

	promptSingleLine = ansi.Truncate(promptSingleLine, remainingWidth, "…")

	promptStyle := sty.Tool.AgentPrompt
	if dimLine {
		promptStyle = promptStyle.Faint(true)
	}
	promptText := promptStyle.Render(" (" + promptSingleLine + ")")

	// Render stats: only if not resolved
	var statsText string
	if !opts.HasResult() && !opts.IsCanceled() {
		toolUses := len(r.agent.nestedTools)
		plural := "uses"
		if toolUses == 1 {
			plural = "use"
		}
		statsText = fmt.Sprintf(" · %d tool %s", toolUses, plural)
	}
	statsRendered := promptStyle.Render(statsText)

	mainLine := "   " + dimmedTree + taskTag + promptText + statsRendered

	var statusLine string
	if !opts.HasResult() && !opts.IsCanceled() {
		statusText := "Initializing…"
		if len(r.agent.nestedTools) > 0 {
			lastTool := r.agent.nestedTools[len(r.agent.nestedTools)-1]
			toolName := normalizeToolName(lastTool.ToolCall().Name)
			statusText = fmt.Sprintf("active tool: %s", toolName)
		}
		if elapsed != "" {
			statusText = fmt.Sprintf("%s · %s", statusText, elapsed)
		}
		statusPrefix := sty.Tool.AgentPrompt.Render("   \u23BF  ") // U+23BF is ⎿
		statusLine = "   " + statusPrefix + sty.Tool.AgentPrompt.Render(statusText)
	} else if opts.HasResult() {
		statusPrefix := sty.Tool.AgentPrompt.Render("   \u23BF  ")
		statusLine = "   " + statusPrefix + sty.Tool.AgentPrompt.Render("Done")
	}

	var parts []string
	parts = append(parts, header)
	parts = append(parts, mainLine)
	if statusLine != "" {
		parts = append(parts, statusLine)
	}
	if nestedView := r.renderNestedTools(sty, cappedWidth, opts); nestedView != "" {
		parts = append(parts, nestedView)
	}

	result := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// Add body content when completed.
	if opts.HasResult() && opts.Result.Content != "" {
		body := toolOutputMarkdownContent(sty, opts.Result.Content, cappedWidth-toolBodyLeftPaddingTotal, opts.ExpandedContent)
		return joinToolParts(result, body)
	}

	return result
}

func (r *AgentToolRenderContext) renderNestedTools(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	toolUses := len(r.agent.nestedTools)
	if toolUses == 0 {
		return ""
	}

	plural := "uses"
	if toolUses == 1 {
		plural = "use"
	}

	if !opts.ExpandedContent {
		prefix := sty.Tool.AgentPrompt.Render("   \u23BF  ")
		text := fmt.Sprintf("+%d tool %s (ctrl+o to expand)", toolUses, plural)
		return "   " + prefix + sty.Tool.AgentPrompt.Render(text)
	}

	childRoot := tree.Root("")
	nestedWidth := max(20, width-toolBodyLeftPaddingTotal-6)
	for _, nestedTool := range r.agent.nestedTools {
		childRoot.Child(nestedTool.Render(nestedWidth))
	}
	rendered := childRoot.Enumerator(roundedEnumerator(2, 2)).String()
	rendered = strings.TrimPrefix(rendered, "\n")
	if rendered == "" {
		return ""
	}
	return sty.Tool.Body.Render(rendered)
}

func getAgentRoleName(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "explore":
		return "Explore Agent"
	case "plan":
		return "Plan Agent"
	case "worker":
		return "Worker Agent"
	case "":
		return "Explore Agent"
	default:
		r := strings.TrimSpace(role)
		if len(r) == 0 {
			return "Agent"
		}
		return strings.ToUpper(r[:1]) + r[1:] + " Agent"
	}
}

// -----------------------------------------------------------------------------
// Agentic Fetch Tool
// -----------------------------------------------------------------------------

// AgenticFetchToolMessageItem is a message item that represents an agentic fetch tool call.
type AgenticFetchToolMessageItem struct {
	*baseToolMessageItem

	nestedTools []ToolMessageItem
}

var (
	_ ToolMessageItem     = (*AgenticFetchToolMessageItem)(nil)
	_ NestedToolContainer = (*AgenticFetchToolMessageItem)(nil)
)

// NewAgenticFetchToolMessageItem creates a new [AgenticFetchToolMessageItem].
func NewAgenticFetchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) *AgenticFetchToolMessageItem {
	t := &AgenticFetchToolMessageItem{}
	t.baseToolMessageItem = newBaseToolMessageItem(sty, toolCall, result, &AgenticFetchToolRenderContext{fetch: t}, canceled)
	// For the agentic fetch tool we keep spinning until the tool call is finished.
	t.spinningFunc = func(state SpinningState) bool {
		return !state.HasResult() && !state.IsCanceled()
	}
	return t
}

// Animate progresses the message animation if it should be spinning.
// See [AgentToolMessageItem.Animate] for the parent-bump rationale —
// without an override, the embedded base.Animate would (a) drop
// StepMsgs whose ID matches a nested child instead of the parent
// (anim.Animate's ID check at internal/ui/anim/anim.go:326-329
// silently returns nil), and (b) never invalidate the parent's
// list-cache entry on a parent tick.
func (a *AgenticFetchToolMessageItem) Animate(msg StepMsg) tea.Cmd {
	if a.result != nil || a.Status() == ToolStatusCanceled {
		return nil
	}
	if msg.ID == a.ID() {
		a.Bump()
		return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
			return StepMsg{ID: a.ID()}
		})
	}
	for _, nestedTool := range a.nestedTools {
		if msg.ID != nestedTool.ID() {
			continue
		}
		if s, ok := nestedTool.(Animatable); ok {
			a.Bump()
			return s.Animate(msg)
		}
	}
	return nil
}

// NestedTools returns the nested tools.
func (a *AgenticFetchToolMessageItem) NestedTools() []ToolMessageItem {
	return a.nestedTools
}

// SetNestedTools sets the nested tools. Always bumps the version;
// see [AgentToolMessageItem.SetNestedTools] for the rationale.
func (a *AgenticFetchToolMessageItem) SetNestedTools(tools []ToolMessageItem) {
	a.nestedTools = tools
	a.clearCache()
	a.Bump()
}

// AddNestedTool adds a nested tool.
func (a *AgenticFetchToolMessageItem) AddNestedTool(tool ToolMessageItem) {
	// Mark nested tools as simple (compact) rendering.
	if s, ok := tool.(Compactable); ok {
		s.SetCompact(true)
	}
	a.nestedTools = append(a.nestedTools, tool)
	a.clearCache()
	a.Bump()
}

// AgenticFetchToolRenderContext renders agentic fetch tool messages.
type AgenticFetchToolRenderContext struct {
	fetch *AgenticFetchToolMessageItem
}

// agenticFetchParams matches tools.AgenticFetchParams.
type agenticFetchParams struct {
	URL    string `json:"url,omitempty"`
	Prompt string `json:"prompt"`
}

// RenderTool implements the [ToolRenderer] interface.
func (r *AgenticFetchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if !opts.ToolCall.Finished && !opts.IsCanceled() && len(r.fetch.nestedTools) == 0 {
		return pendingTool(sty, "Agentic Fetch", opts.Compact)
	}

	var params agenticFetchParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	prompt := params.Prompt
	prompt = strings.ReplaceAll(prompt, "\n", " ")

	// Brain header with optional URL param.
	var toolParams []string
	if params.URL != "" {
		toolParams = append(toolParams, params.URL)
	}

	header := toolHeader(sty, opts.Status, "Agentic Fetch", cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}

	// Brain the prompt tag.
	promptTag := sty.Tool.AgenticFetchPromptTag.Render("Prompt")
	promptTagWidth := lipgloss.Width(promptTag)

	// Calculate remaining width for prompt text.
	remainingWidth := min(cappedWidth-promptTagWidth-3, maxTextWidth-promptTagWidth-3) // -3 for spacing

	promptText := sty.Tool.AgentPrompt.Width(remainingWidth).Render(prompt)

	header = lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		lipgloss.JoinHorizontal(
			lipgloss.Left,
			promptTag,
			" ",
			promptText,
		),
	)

	// Brain tree with nested tool calls.
	childTools := tree.Root(header)

	for _, nestedTool := range r.fetch.nestedTools {
		childView := nestedTool.Render(remainingWidth)
		childTools.Child(childView)
	}

	// Brain parts.
	var parts []string
	parts = append(parts, childTools.Enumerator(roundedEnumerator(2, promptTagWidth-5)).String())

	// Show one-cell braille spinner if still running. opts.Anim still
	// ticks the redraw cycle; we discard its 15-char cycling output and
	// render a clean spinner+label instead.
	if !opts.HasResult() && !opts.IsCanceled() {
		parts = append(parts, "", renderBrailleSpinner(sty, "Working"))
	}

	result := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// Add body content when completed.
	if opts.HasResult() && opts.Result.Content != "" {
		body := toolOutputMarkdownContent(sty, opts.Result.Content, cappedWidth-toolBodyLeftPaddingTotal, opts.ExpandedContent)
		return joinToolParts(result, body)
	}

	return result
}
