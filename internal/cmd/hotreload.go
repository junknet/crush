//go:build !windows

package cmd

import (
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/crush/internal/config"
)

// hotReloadCheckInterval is how often we poll for changes to the binary or
// known config files. Polling (instead of fsnotify) keeps the implementation
// dependency-light and survives editors that delete-and-rewrite the file.
const hotReloadCheckInterval = 3 * time.Second

// startHotReload spawns a goroutine that watches the running binary and the
// user's config files. When any of them are modified on disk, the server
// gracefully shuts down, then re-execs itself with the same argv/env so the
// new binary or new config takes effect. SIGHUP / SIGUSR2 trigger an
// immediate reload check, regardless of mtime — handy for `kill -HUP <pid>`.
//
// shutdown is a hook called before re-exec so that the http listener and
// background workers get a chance to flush; it must return quickly.
func startHotReload(shutdown func()) {
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("hot reload disabled: cannot resolve binary path", "error", err)
		return
	}
	// Resolve symlinks so a wrapper like ~/.local/bin/crush -> .cache/.../crush
	// still tracks the real binary's mtime.
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}

	watched := append([]string{exe}, hotReloadConfigPaths()...)

	var (
		mu       sync.Mutex
		baseline = make(map[string]time.Time, len(watched))
		fired    bool
	)
	for _, p := range watched {
		if fi, err := os.Stat(p); err == nil {
			baseline[p] = fi.ModTime()
		}
	}

	trigger := func(reason string) {
		mu.Lock()
		if fired {
			mu.Unlock()
			return
		}
		fired = true
		mu.Unlock()

		slog.Info("hot reload triggered", "reason", reason, "binary", exe)
		if shutdown != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Warn("shutdown hook panicked during hot reload", "recover", r)
					}
				}()
				shutdown()
			}()
		}
		argv := append([]string{exe}, os.Args[1:]...)
		env := os.Environ()
		if err := syscall.Exec(exe, argv, env); err != nil {
			slog.Error("hot reload exec failed; exiting so supervisor can restart us",
				"error", err)
			os.Exit(0)
		}
	}

	// SIGHUP / SIGUSR2: manual trigger.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGUSR2)
	go func() {
		for s := range sigCh {
			trigger("signal:" + s.String())
		}
	}()

	// Polling loop.
	go func() {
		t := time.NewTicker(hotReloadCheckInterval)
		defer t.Stop()
		for range t.C {
			for _, p := range watched {
				fi, err := os.Stat(p)
				if err != nil {
					continue
				}
				mu.Lock()
				prev, ok := baseline[p]
				mu.Unlock()
				if !ok {
					mu.Lock()
					baseline[p] = fi.ModTime()
					mu.Unlock()
					continue
				}
				if !fi.ModTime().Equal(prev) {
					trigger("mtime:" + p)
					return
				}
			}
		}
	}()
}

// hotReloadConfigPaths returns existing config files we care about. We don't
// fail if they're absent — they may be created later, in which case the next
// poll picks them up via baseline backfill.
func hotReloadConfigPaths() []string {
	var paths []string
	add := func(p string) {
		if p == "" {
			return
		}
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	// Config lives in a single location: the declarative crush.{yaml,json}
	// and the runtime state.{yaml,yml} next to it.
	if base := config.GlobalConfig(); base != "" {
		dir := filepath.Dir(base)
		add(filepath.Join(dir, "crush.yaml"))
		add(filepath.Join(dir, "crush.yml"))
		add(filepath.Join(dir, "crush.json"))
		add(filepath.Join(dir, "state.yaml"))
		add(filepath.Join(dir, "state.yml"))
	}
	return paths
}
