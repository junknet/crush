package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"charm.land/fantasy"
)

// DeferredRegistry is a per-agent registry of tools whose JSON Schemas are
// withheld from the model on the initial tool list. The model sees a stub
// (name + short description) and must call `tool_search` to load the full
// schema before invoking the tool.
//
// This dramatically shrinks the system tool-list footprint when many MCP
// servers are configured: each server can register hundreds of tools, but
// only the handful actually needed for a given task get expanded.
//
// Concurrency: safe for concurrent registration and lookup. Each coordinator
// creates one registry instance and shares it with the session agent and the
// tool_search tool implementation.
type DeferredRegistry struct {
	mu sync.RWMutex
	// real holds the full AgentTool indexed by tool name. The schema lives
	// here; we only surface it after activation.
	real map[string]fantasy.AgentTool
	// activated tracks tool names whose schemas have been promoted to the
	// active tool set via tool_search. Once activated, a tool stays
	// activated for the rest of the session.
	activated map[string]struct{}
	// searchHints carries optional extra search keywords per tool name.
	// Currently we just derive them from name + description, so this is
	// reserved for future use (e.g. MCP server tags).
	searchHints map[string]string
}

// NewDeferredRegistry constructs an empty registry.
func NewDeferredRegistry() *DeferredRegistry {
	return &DeferredRegistry{
		real:        map[string]fantasy.AgentTool{},
		activated:   map[string]struct{}{},
		searchHints: map[string]string{},
	}
}

// Register adds a deferred tool. The full AgentTool (with schema) is held
// for later promotion; the model only sees the stub until tool_search
// activates the tool. searchHint may be empty.
func (r *DeferredRegistry) Register(tool fantasy.AgentTool, searchHint string) {
	if tool == nil {
		return
	}
	name := tool.Info().Name
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.real[name] = tool
	if searchHint != "" {
		r.searchHints[name] = searchHint
	}
}

// Activate promotes one or more tools to the active set. Tools not present
// in the registry are silently ignored. Returns the names that were newly
// activated (subset of input).
func (r *DeferredRegistry) Activate(names ...string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var promoted []string
	for _, name := range names {
		if _, known := r.real[name]; !known {
			continue
		}
		if _, ok := r.activated[name]; ok {
			continue
		}
		r.activated[name] = struct{}{}
		promoted = append(promoted, name)
	}
	return promoted
}

// IsActivated reports whether a tool has been promoted out of deferred state.
func (r *DeferredRegistry) IsActivated(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.activated[name]
	return ok
}

// Names returns all registered tool names, sorted.
func (r *DeferredRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.real))
	for n := range r.real {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DeferredNames returns the names of tools that are still deferred (i.e.
// registered but not yet activated), sorted. This is what gets surfaced to
// the model in the system-reminder block.
func (r *DeferredRegistry) DeferredNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.real))
	for n := range r.real {
		if _, ok := r.activated[n]; ok {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns the full AgentTool for the named entry, if registered.
func (r *DeferredRegistry) Get(name string) (fantasy.AgentTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.real[name]
	return t, ok
}

// SearchHint returns the optional extra search hint for a tool.
func (r *DeferredRegistry) SearchHint(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.searchHints[name]
}

// ActivatedTools returns the live AgentTool slice for all activated entries,
// sorted by name. This is what the session agent merges into the active
// tool list each PrepareStep so that schemas the model has loaded stay
// reachable for invocation.
func (r *DeferredRegistry) ActivatedTools() []fantasy.AgentTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.activated))
	for n := range r.activated {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]fantasy.AgentTool, 0, len(names))
	for _, n := range names {
		if t, ok := r.real[n]; ok {
			out = append(out, t)
		}
	}
	return out
}

// DeferredHash returns a stable hash of the current set of deferred tool
// names. The session agent uses this to detect changes between turns so it
// only emits the deferred-tools system-reminder when the visible list
// actually changed (e.g. an MCP server just finished connecting, or
// tool_search just activated something).
func (r *DeferredRegistry) DeferredHash() string {
	names := r.DeferredNames()
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SnapshotStubs returns proxy tools for every still-deferred entry. These
// are what gets surfaced to the model on the initial tool list: name and
// description only, with a Run that errors out telling the model to call
// tool_search first.
func (r *DeferredRegistry) SnapshotStubs() []fantasy.AgentTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	stubs := make([]fantasy.AgentTool, 0, len(r.real))
	for name, tool := range r.real {
		if _, ok := r.activated[name]; ok {
			continue
		}
		info := tool.Info()
		stubs = append(stubs, &proxyDeferredTool{info: info})
	}
	sort.Slice(stubs, func(i, j int) bool {
		return stubs[i].Info().Name < stubs[j].Info().Name
	})
	return stubs
}

// proxyDeferredTool surfaces a deferred tool to the model with the schema
// stripped. Calling it triggers an error that nudges the model toward
// tool_search.
type proxyDeferredTool struct {
	info            fantasy.ToolInfo
	providerOptions fantasy.ProviderOptions
}

func (p *proxyDeferredTool) Info() fantasy.ToolInfo {
	// Strip parameters but keep name + description so the model can decide
	// whether the tool is worth loading via tool_search.
	descr := p.info.Description
	descr = strings.TrimSpace(descr)
	if descr == "" {
		descr = "(no description)"
	}
	descr = "[schema deferred — call ToolSearch to load before invoking] " + descr
	return fantasy.ToolInfo{
		Name:        p.info.Name,
		Description: descr,
		Parameters:  map[string]any{},
		Required:    []string{},
		Parallel:    p.info.Parallel,
	}
}

func (p *proxyDeferredTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	msg := fmt.Sprintf(
		"Tool %q schema is not loaded. Call ToolSearch with query \"select:%s\" to load the schema, then re-invoke the tool.",
		p.info.Name, p.info.Name,
	)
	return fantasy.NewTextErrorResponse(msg), nil
}

func (p *proxyDeferredTool) ProviderOptions() fantasy.ProviderOptions { return p.providerOptions }
func (p *proxyDeferredTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	p.providerOptions = opts
}
