package tools

import (
	"context"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// fakeTool is a minimal AgentTool implementation used to populate the
// deferred registry without dragging in real MCP or LSP plumbing.
type fakeTool struct {
	info fantasy.ToolInfo
}

func newFakeTool(name, description string, params map[string]any, required []string) fantasy.AgentTool {
	return &fakeTool{info: fantasy.ToolInfo{
		Name:        name,
		Description: description,
		Parameters:  params,
		Required:    required,
	}}
}

func (f *fakeTool) Info() fantasy.ToolInfo { return f.info }
func (f *fakeTool) Run(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextResponse("ran " + f.info.Name), nil
}
func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions        { return nil }
func (f *fakeTool) SetProviderOptions(opts fantasy.ProviderOptions) {}

func TestToolSearchSelectSyntax(t *testing.T) {
	reg := NewDeferredRegistry()
	reg.Register(newFakeTool("mcp_foo_search", "Search Foo records", map[string]any{
		"query": map[string]any{"type": "string"},
	}, []string{"query"}), "foo")
	reg.Register(newFakeTool("mcp_bar_list", "List Bar items", map[string]any{}, nil), "bar")
	reg.Register(newFakeTool("mcp_baz_get", "Fetch a Baz by id", map[string]any{
		"id": map[string]any{"type": "string"},
	}, []string{"id"}), "baz")

	got := searchDeferred(reg, "select:mcp_foo_search,mcp_baz_get", 5)
	require.Len(t, got, 2)
	require.Equal(t, "mcp_foo_search", got[0].Info().Name)
	require.Equal(t, "mcp_baz_get", got[1].Info().Name)

	// Unknown names are silently dropped, known ones survive.
	got = searchDeferred(reg, "select:mcp_bar_list,does_not_exist", 5)
	require.Len(t, got, 1)
	require.Equal(t, "mcp_bar_list", got[0].Info().Name)
}

func TestToolSearchKeywordRanking(t *testing.T) {
	reg := NewDeferredRegistry()
	// Name match should outrank description match.
	reg.Register(newFakeTool("github_search", "Look up issues and pull requests in a repository.", nil, nil), "github")
	reg.Register(newFakeTool("mcp_other_thing", "Search for related GitHub items via a side channel.", nil, nil), "other")
	reg.Register(newFakeTool("unrelated_tool", "Pull weather forecasts from NOAA.", nil, nil), "weather")

	got := searchDeferred(reg, "github search", 5)
	require.GreaterOrEqual(t, len(got), 2)
	// First result has both terms in its name.
	require.Equal(t, "github_search", got[0].Info().Name)
}

func TestToolSearchRequiredTerm(t *testing.T) {
	reg := NewDeferredRegistry()
	reg.Register(newFakeTool("ToolReadFile", "Read a file from disk.", nil, nil), "fs")
	reg.Register(newFakeTool("ToolReadHTTP", "Fetch a URL over HTTP.", nil, nil), "net")
	reg.Register(newFakeTool("ToolWriteFile", "Write a file to disk.", nil, nil), "fs")

	// +file forces the term; "read" is optional scoring.
	got := searchDeferred(reg, "+file read", 5)
	require.Len(t, got, 2)
	// ToolReadFile scores higher (matches both terms in name); ToolWriteFile only matches +file.
	require.Equal(t, "ToolReadFile", got[0].Info().Name)
	require.Equal(t, "ToolWriteFile", got[1].Info().Name)
}

func TestToolSearchMaxResultsCap(t *testing.T) {
	reg := NewDeferredRegistry()
	for _, n := range []string{"alpha_search", "beta_search", "gamma_search", "delta_search"} {
		reg.Register(newFakeTool(n, "Common description with search keyword.", nil, nil), "")
	}
	got := searchDeferred(reg, "search", 2)
	require.Len(t, got, 2)
}

func TestToolSearchToolRunActivates(t *testing.T) {
	reg := NewDeferredRegistry()
	reg.Register(newFakeTool("mcp_alpha", "alpha description", map[string]any{
		"x": map[string]any{"type": "integer"},
	}, []string{"x"}), "alpha")
	reg.Register(newFakeTool("mcp_beta", "beta description", nil, nil), "beta")

	tool := NewToolSearchTool(reg)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  ToolSearchToolName,
		Input: `{"query":"select:mcp_alpha"}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.True(t, strings.Contains(resp.Content, "<functions>"))
	require.True(t, strings.Contains(resp.Content, `"name":"mcp_alpha"`))
	require.True(t, reg.IsActivated("mcp_alpha"))
	require.False(t, reg.IsActivated("mcp_beta"))

	// Tools that are still deferred should not appear in ActivatedTools.
	activated := reg.ActivatedTools()
	require.Len(t, activated, 1)
	require.Equal(t, "mcp_alpha", activated[0].Info().Name)
}

func TestToolSearchEmptyQueryErrors(t *testing.T) {
	reg := NewDeferredRegistry()
	reg.Register(newFakeTool("any_tool", "any", nil, nil), "")
	tool := NewToolSearchTool(reg)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  ToolSearchToolName,
		Input: `{"query":""}`,
	})
	require.NoError(t, err)
	require.True(t, resp.IsError)
}

func TestDeferredRegistryStubAndHash(t *testing.T) {
	reg := NewDeferredRegistry()
	reg.Register(newFakeTool("t1", "first tool", map[string]any{"a": map[string]any{"type": "string"}}, []string{"a"}), "")
	reg.Register(newFakeTool("t2", "second tool", nil, nil), "")

	stubs := reg.SnapshotStubs()
	require.Len(t, stubs, 2)
	for _, s := range stubs {
		info := s.Info()
		require.Empty(t, info.Parameters, "stub %q must not leak schema", info.Name)
		require.True(t, strings.Contains(info.Description, "schema deferred"))
		// Stub Run must surface a guidance error rather than executing.
		resp, err := s.Run(context.Background(), fantasy.ToolCall{ID: "x", Name: info.Name})
		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.True(t, strings.Contains(resp.Content, "ToolSearch"))
	}

	hashBefore := reg.DeferredHash()
	reg.Activate("t1")
	hashAfter := reg.DeferredHash()
	require.NotEqual(t, hashBefore, hashAfter, "activation must shift the deferred set hash")
	require.Equal(t, []string{"t2"}, reg.DeferredNames())
}

func TestParseQueryTermsCaseAndPlus(t *testing.T) {
	required, optional := parseQueryTerms("+Foo BAR baz +qux")
	require.Equal(t, []string{"foo", "qux"}, required)
	require.Equal(t, []string{"bar", "baz"}, optional)
}

func TestNormalizeForSearchCamelAndSnake(t *testing.T) {
	got := normalizeForSearch("ReadMCPResource_v2")
	require.Contains(t, got, "read")
	require.Contains(t, got, "mcp")
	require.Contains(t, got, "resource")
	require.Contains(t, got, "v2")
}
