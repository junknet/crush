package backend

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/charmbracelet/crush/internal/projects"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
)

// SessionDriverConnected records that a driver client (a TUI running this
// session) attached. The session is "alive" while at least one driver is on it.
func (b *Backend) SessionDriverConnected(sid string) {
	if sid == "" {
		return
	}
	if b.sessionDrivers.GetOrSet(sid, func() *atomic.Int64 { return &atomic.Int64{} }).Add(1) == 1 {
		// First driver: the session just went live — nudge observers (the
		// phone) to refresh so it appears in their list right away.
		b.nudgeSessionListeners(context.Background(), sid)
	}
}

// SessionDriverDisconnected records that a driver detached. When the last one
// leaves (the TUI exited) the session is no longer live, so observers (the
// phone) are nudged to refresh and drop it from their list — the session itself
// stays in the database untouched.
func (b *Backend) SessionDriverDisconnected(ctx context.Context, sid string) {
	if sid == "" {
		return
	}
	c, ok := b.sessionDrivers.Get(sid)
	if !ok {
		return
	}
	if c.Add(-1) > 0 {
		return
	}
	b.nudgeSessionListeners(ctx, sid)
}

// nudgeSessionListeners republishes a session as an UpdatedEvent so observers
// (the phone) refresh their session list and pick up its new liveness — whether
// it just went live (first driver) or just went dead (last driver left). The
// session row itself is unchanged; this only triggers a re-read.
func (b *Backend) nudgeSessionListeners(ctx context.Context, sid string) {
	ws, err := b.GetSessionRuntime(ctx, sid)
	if err != nil {
		return
	}
	if sess, err := ws.Sessions.Get(ctx, sid); err == nil {
		ws.SendEvent(pubsub.Event[session.Session]{Type: pubsub.UpdatedEvent, Payload: sess})
	}
}

// IsSessionAlive reports whether a driver (TUI) is currently attached.
func (b *Backend) IsSessionAlive(sid string) bool {
	if c, ok := b.sessionDrivers.Get(sid); ok {
		return c.Load() > 0
	}
	return false
}

// SessionInfo is a session enriched with its filesystem path and live state —
// the session-primary view that supersedes the workspace-keyed model. The
// session is the unit of work; Path is merely where it runs; the per-path
// runtime (app.App) is an internal, lazily-pooled detail (see runtimeForPath).
type SessionInfo struct {
	session.Session
	Path  string `json:"path"`
	Alive bool   `json:"alive"`
}

// runtimeForPath returns the running per-path runtime, reusing an existing one
// for that path or lazily creating it. This is what "workspace" actually is: a
// per-directory engine (config + db + agents + lsp + mcp) shared by every
// session under that path. Deduping by path means two sessions in the same repo
// share one engine instead of spinning up a second.
func (b *Backend) runtimeForPath(path string) (*Workspace, error) {
	if path == "" {
		return nil, ErrPathRequired
	}
	for _, ws := range b.workspaces.Seq2() {
		if ws.Path == path {
			return ws, nil
		}
	}
	ws, _, err := b.CreateWorkspace(proto.Workspace{Path: path})
	if err != nil {
		return nil, fmt.Errorf("failed to start runtime for path %q: %w", path, err)
	}
	return ws, nil
}

// ListAllSessions returns every session across all known projects as a flat,
// session-primary list — each tagged with its path and whether a client is
// currently attached. It also (re)builds the sessionID -> path index. Paths
// come from the projects registry (~/.local/share/crush/projects.json).
func (b *Backend) ListAllSessions(ctx context.Context) ([]SessionInfo, error) {
	projectList, err := projects.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	var out []SessionInfo
	for _, project := range projectList {
		ws, err := b.runtimeForPath(project.Path)
		if err != nil {
			// A broken/missing project must not sink the whole listing.
			continue
		}
		sessions, err := ws.Sessions.List(ctx)
		if err != nil {
			continue
		}
		for _, sess := range sessions {
			b.sessionPaths.Set(sess.ID, project.Path)
			out = append(out, SessionInfo{Session: sess, Path: project.Path, Alive: b.IsSessionAlive(sess.ID)})
		}
	}
	return out, nil
}

// GetSessionRuntime resolves a sessionID to its per-path runtime, consulting the
// index first and falling back to a full project scan (which also refreshes the
// index) on a miss.
func (b *Backend) GetSessionRuntime(ctx context.Context, sessionID string) (*Workspace, error) {
	if path, ok := b.sessionPaths.Get(sessionID); ok {
		return b.runtimeForPath(path)
	}
	if _, err := b.ListAllSessions(ctx); err != nil {
		return nil, err
	}
	if path, ok := b.sessionPaths.Get(sessionID); ok {
		return b.runtimeForPath(path)
	}
	return nil, fmt.Errorf("session %q not found in any known project", sessionID)
}
