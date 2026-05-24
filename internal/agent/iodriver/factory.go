package iodriver

import (
	"context"
	"fmt"
	"sync"
)

// Factory builds and caches Driver instances by URI so that multiple
// sessions pointed at the same SSH host reuse the same connection /
// persistent shell. Concurrency-safe.
//
// Local URIs are always fresh because they hold no real resources;
// caching them would risk surprising users who change working dir.
type Factory struct {
	fallbackCwd string

	mu      sync.Mutex
	drivers map[string]Driver // keyed by canonical URI (scheme + user@host:port for ssh)
}

// NewFactory returns a Factory whose blank/local lookups pin to
// fallbackCwd. Pass the workspace's WorkingDir.
func NewFactory(fallbackCwd string) *Factory {
	return &Factory{fallbackCwd: fallbackCwd, drivers: map[string]Driver{}}
}

// Get returns a Driver for uri, creating one if necessary. For local
// URIs a fresh LocalDriver is returned. For ssh URIs the first call
// dials and starts the persistent PTY shell; subsequent calls for the
// same host return the cached driver.
func (f *Factory) Get(ctx context.Context, uri string) (Driver, error) {
	kind, u, cwd, err := ParseURI(uri, f.fallbackCwd)
	if err != nil {
		return nil, fmt.Errorf("iodriver: parse %q: %w", uri, err)
	}
	switch kind {
	case KindLocal:
		return NewLocalDriver(cwd), nil
	case KindSSH:
		key := "ssh://" + u.User.Username() + "@" + u.Host
		f.mu.Lock()
		defer f.mu.Unlock()
		if d, ok := f.drivers[key]; ok {
			return d, nil
		}
		d, err := dialSSH(ctx, u, cwd)
		if err != nil {
			return nil, err
		}
		f.drivers[key] = d
		return d, nil
	}
	return nil, fmt.Errorf("iodriver: unsupported kind %q", kind)
}

// Close shuts down every cached driver. Safe to call multiple times.
func (f *Factory) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var first error
	for k, d := range f.drivers {
		if err := d.Close(); err != nil && first == nil {
			first = err
		}
		delete(f.drivers, k)
	}
	return first
}
