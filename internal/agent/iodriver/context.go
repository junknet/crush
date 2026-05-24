package iodriver

import "context"

// driverKey is the context key under which a Driver instance is stored.
type driverKey struct{}

// WithDriver returns ctx augmented with the given Driver. Tools should
// call FromContext(ctx) on every invocation rather than caching the
// driver at construction time, so that mid-session `set_workspace`
// switches take effect on the very next tool call.
func WithDriver(ctx context.Context, d Driver) context.Context {
	if d == nil {
		return ctx
	}
	return context.WithValue(ctx, driverKey{}, d)
}

// FromContext returns the Driver attached to ctx, or nil if none is set.
// Callers should fall back to a LocalDriver pinned to their constructor-
// supplied workingDir when nil is returned, so that callers without
// driver-aware plumbing (tests, legacy code paths) keep working.
func FromContext(ctx context.Context) Driver {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(driverKey{}).(Driver)
	return d
}

// FromContextOrLocal returns FromContext(ctx) if set, otherwise a fresh
// LocalDriver pinned to fallbackWorkingDir. This is the recommended
// entry point for tool implementations.
func FromContextOrLocal(ctx context.Context, fallbackWorkingDir string) Driver {
	if d := FromContext(ctx); d != nil {
		return d
	}
	return NewLocalDriver(fallbackWorkingDir)
}
