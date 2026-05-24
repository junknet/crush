package iodriver

import "sync"

// URIRegistry holds per-session workspace URI overrides. It is the
// runtime store the `set_workspace` tool writes to and the coordinator
// reads from when attaching a driver to ctx.
//
// Storage is in-memory and resets on restart by design: the URI
// determines where the agent's tools touch the filesystem, and it's
// cheaper / safer to make the user explicitly opt back into a remote
// host on each launch than to risk silently running edits against an
// old SSH workspace whose credentials may have rotated.
//
// Concurrency-safe; intended as a single process-wide instance owned
// by the coordinator.
type URIRegistry struct {
	mu sync.RWMutex
	m  map[string]string // sessionID → URI ("" means local default)
}

// NewURIRegistry returns an empty registry.
func NewURIRegistry() *URIRegistry {
	return &URIRegistry{m: map[string]string{}}
}

// Set assigns uri to sessionID. Empty uri removes the override (the
// session falls back to local).
func (r *URIRegistry) Set(sessionID, uri string) {
	if r == nil || sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if uri == "" {
		delete(r.m, sessionID)
		return
	}
	r.m[sessionID] = uri
}

// Get returns the URI for sessionID, or "" if none is set (caller
// should treat that as local).
func (r *URIRegistry) Get(sessionID string) string {
	if r == nil || sessionID == "" {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[sessionID]
}

// All returns a copy of every registered (sessionID, URI) pair. Used
// by diagnostics / the future status pill.
func (r *URIRegistry) All() map[string]string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.m))
	for k, v := range r.m {
		out[k] = v
	}
	return out
}
