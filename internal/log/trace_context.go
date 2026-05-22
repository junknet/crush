package log

import (
	"context"
	"log/slog"
)

// traceIDKey and sessionIDKey are private context keys used to thread
// correlation identifiers through a single agent turn so that every
// observability surface (slog lines, provider HTTP dumps, IPC dumps) can be
// joined by the same id.
type (
	traceIDKey   struct{}
	sessionIDKey struct{}
)

// WithTraceID returns a context carrying the given turn-level trace id. The id
// is read back by [TraceIDFromContext], stamped into provider/IPC HTTP dumps,
// and attached as a "trace_id" attribute to any slog record logged via the
// *Context APIs once [TraceContextHandler] is installed.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromContext returns the trace id stored by [WithTraceID], or "" if
// none is present.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithSessionID returns a context carrying the conversation session id, used
// as the join key between turn-level trace ids and the DAG trace JSONL (which
// already records session_id).
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFromContext returns the session id stored by [WithSessionID], or ""
// if none is present.
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return v
	}
	return ""
}

// TraceContextHandler is a slog.Handler middleware that copies the trace_id and
// session_id from the record's context onto the record as attributes. It only
// affects records logged through the *Context slog APIs (e.g. slog.DebugContext)
// since plain slog.Debug calls carry a background context.
type TraceContextHandler struct {
	next slog.Handler
}

// NewTraceContextHandler wraps next so that correlation ids present in the log
// context are emitted as structured attributes.
func NewTraceContextHandler(next slog.Handler) *TraceContextHandler {
	return &TraceContextHandler{next: next}
}

func (h *TraceContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *TraceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if traceID := TraceIDFromContext(ctx); traceID != "" {
		r.AddAttrs(slog.String("trace_id", traceID))
	}
	if sessionID := SessionIDFromContext(ctx); sessionID != "" {
		r.AddAttrs(slog.String("session_id", sessionID))
	}
	return h.next.Handle(ctx, r)
}

func (h *TraceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceContextHandler{next: h.next.WithAttrs(attrs)}
}

func (h *TraceContextHandler) WithGroup(name string) slog.Handler {
	return &TraceContextHandler{next: h.next.WithGroup(name)}
}
