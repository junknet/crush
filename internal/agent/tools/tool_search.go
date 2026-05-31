package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
)

// ToolSearchToolName is the model-facing identifier for the schema-loading
// tool. Kept short so the model uses it freely.
const ToolSearchToolName = "ToolSearch"

//go:embed tool_search.md
var toolSearchDescription string

// ToolSearchParams is the input schema for the tool_search tool.
type ToolSearchParams struct {
	// Query selects deferred tools to load. See tool_search.md for syntax.
	Query string `json:"query" description:"Search query. 'select:Foo,Bar' loads named tools; '+term' marks required terms; remaining tokens score by name/description match."`
	// MaxResults caps how many tools are returned in keyword mode. Ignored
	// for 'select:' queries.
	MaxResults int `json:"max_results,omitempty" description:"Maximum tools to return for keyword queries. Default 5. Ignored when using select: syntax."`
}

// NewToolSearchTool constructs the schema-loader tool bound to a given
// deferred registry. Activating a tool here is durable: subsequent
// PrepareStep cycles pick the activated schemas up via reg.ActivatedTools()
// and merge them into the agent's live tool list.
func NewToolSearchTool(reg *DeferredRegistry) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		ToolSearchToolName,
		toolSearchDescription,
		func(ctx context.Context, params ToolSearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if reg == nil {
				return fantasy.NewTextErrorResponse("Deferred tool registry not configured for this agent."), nil
			}
			query := strings.TrimSpace(params.Query)
			if query == "" {
				return fantasy.NewTextErrorResponse("query parameter is required"), nil
			}
			maxResults := params.MaxResults
			if maxResults <= 0 {
				maxResults = 5
			}

			matches := searchDeferred(reg, query, maxResults)

			// Activate every match so subsequent PrepareStep cycles surface
			// the real schema, not the proxy stub.
			activatedNames := make([]string, 0, len(matches))
			for _, m := range matches {
				activatedNames = append(activatedNames, m.Info().Name)
			}
			reg.Activate(activatedNames...)

			return fantasy.NewTextResponse(renderToolSearchResponse(matches, pendingMCPServers())), nil
		},
	)
}

// searchDeferred resolves a tool_search query against the registry and
// returns the matched fantasy.AgentTool values, ordered by score
// descending. Results are deduplicated by name.
func searchDeferred(reg *DeferredRegistry, query string, maxResults int) []fantasy.AgentTool {
	q := strings.TrimSpace(query)
	if strings.HasPrefix(q, "select:") {
		raw := strings.TrimPrefix(q, "select:")
		var out []fantasy.AgentTool
		seen := map[string]struct{}{}
		for _, part := range strings.Split(raw, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			if t, ok := reg.Get(name); ok {
				out = append(out, t)
				seen[name] = struct{}{}
			}
		}
		return out
	}

	required, optional := parseQueryTerms(q)
	type scored struct {
		tool  fantasy.AgentTool
		score int
	}
	var ranked []scored
	for _, name := range reg.Names() {
		tool, _ := reg.Get(name)
		if tool == nil {
			continue
		}
		info := tool.Info()
		haystackName := normalizeForSearch(info.Name)
		haystackDescr := strings.ToLower(info.Description)
		haystackHint := strings.ToLower(reg.SearchHint(name))

		// Required terms must appear *somewhere*.
		skip := false
		for _, term := range required {
			if !strings.Contains(haystackName, term) &&
				!strings.Contains(haystackDescr, term) &&
				!strings.Contains(haystackHint, term) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		score := 0
		// Required terms still contribute to score so multi-required
		// queries rank higher than a single optional hit.
		for _, term := range append(append([]string{}, required...), optional...) {
			if term == "" {
				continue
			}
			if strings.Contains(haystackName, term) {
				score += 12
			}
			if strings.Contains(haystackHint, term) {
				score += 4
			}
			if strings.Contains(haystackDescr, term) {
				score += 2
			}
		}
		if score == 0 && len(required) == 0 {
			continue
		}
		ranked = append(ranked, scored{tool: tool, score: score})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].tool.Info().Name < ranked[j].tool.Info().Name
	})

	if maxResults > 0 && len(ranked) > maxResults {
		ranked = ranked[:maxResults]
	}
	out := make([]fantasy.AgentTool, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.tool)
	}
	return out
}

// parseQueryTerms splits a free-form query into required (+term) and
// optional terms. Terms are lower-cased; CamelCase identifiers are split
// into their constituent words so a query like `read file` matches
// `read_mcp_resource` and `ReadFile`.
func parseQueryTerms(q string) (required, optional []string) {
	tokens := strings.Fields(q)
	for _, t := range tokens {
		isRequired := false
		if strings.HasPrefix(t, "+") {
			isRequired = true
			t = strings.TrimPrefix(t, "+")
		}
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if isRequired {
			required = append(required, t)
		} else {
			optional = append(optional, t)
		}
	}
	return required, optional
}

// normalizeForSearch lower-cases a name and expands CamelCase / snake_case
// so substring matches behave like word matches. We don't strip the raw
// form — the result includes both the original lowered string and the
// space-separated word stream so a single Contains call covers both.
func normalizeForSearch(name string) string {
	lower := strings.ToLower(name)
	// Split CamelCase into words.
	var words []rune
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			words = append(words, ' ')
		}
		words = append(words, unicode.ToLower(r))
	}
	expanded := strings.ReplaceAll(string(words), "_", " ")
	expanded = strings.ReplaceAll(expanded, "-", " ")
	return lower + " " + expanded
}

// toolSchemaJSON renders a fantasy.AgentTool as a JSON object the model
// can consume directly (name, description, parameters, required).
func toolSchemaJSON(tool fantasy.AgentTool) map[string]any {
	info := tool.Info()
	params := info.Parameters
	if params == nil {
		params = map[string]any{}
	}
	required := info.Required
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"name":        info.Name,
		"description": info.Description,
		"parameters":  params,
		"required":    required,
	}
}

// pendingMCPServers returns the names of MCP servers that are configured
// but not yet ready. Their tools (if any) will surface on a later turn
// once initialization completes.
func pendingMCPServers() []string {
	var pending []string
	for name, info := range mcp.GetStates() {
		if info.State == mcp.StateConnected {
			continue
		}
		if info.State == mcp.StateDisabled {
			continue
		}
		pending = append(pending, name)
	}
	sort.Strings(pending)
	return pending
}

// renderToolSearchResponse formats the matched tools as a <functions>
// block plus a <connecting_mcp_servers> block when relevant. The
// <functions> envelope keeps the structure recognizable to LLMs that have
// seen Claude-style tool definitions before.
func renderToolSearchResponse(matches []fantasy.AgentTool, pending []string) string {
	var sb strings.Builder
	if len(matches) == 0 {
		sb.WriteString("No deferred tools matched the query.\n")
	} else {
		sb.WriteString("<functions>\n")
		for _, t := range matches {
			b, err := json.Marshal(toolSchemaJSON(t))
			if err != nil {
				sb.WriteString(fmt.Sprintf("// failed to marshal %s: %v\n", t.Info().Name, err))
				continue
			}
			sb.Write(b)
			sb.WriteString("\n")
		}
		sb.WriteString("</functions>\n")
	}
	if len(pending) > 0 {
		sb.WriteString("\n<connecting_mcp_servers>\n")
		b, _ := json.Marshal(pending)
		sb.Write(b)
		sb.WriteString("\n</connecting_mcp_servers>\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
