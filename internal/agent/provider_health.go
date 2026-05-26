package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

type ProviderHealthState struct {
	ActiveProvider    string               `yaml:"active_provider"`
	UnhealthyUntil    map[string]time.Time `yaml:"unhealthy_until,omitempty"`
	ProbingInProgress bool                 `yaml:"probing_in_progress"`
	ProbingProvider   string               `yaml:"probing_provider,omitempty"`
	ProbingLeaderPID  int                  `yaml:"probing_leader_pid,omitempty"`
}

var (
	healthMu sync.Mutex
)

func GetProviderHealthPath() string {
	base := config.GlobalConfig()
	if base == "" {
		return filepath.Join(os.Getenv("HOME"), ".config", "crush", "provider-health.yaml")
	}
	return filepath.Join(filepath.Dir(base), "provider-health.yaml")
}

func lockProviderHealth() (*os.File, error) {
	healthPath := GetProviderHealthPath()
	dir := filepath.Dir(healthPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, "provider-health.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func unlockProviderHealth(f *os.File) {
	if f != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

func ReadProviderHealth() (*ProviderHealthState, error) {
	healthPath := GetProviderHealthPath()
	data, err := os.ReadFile(healthPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProviderHealthState{
				UnhealthyUntil: make(map[string]time.Time),
			}, nil
		}
		return nil, err
	}
	var state ProviderHealthState
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.UnhealthyUntil == nil {
		state.UnhealthyUntil = make(map[string]time.Time)
	}
	return &state, nil
}

func WriteProviderHealth(state *ProviderHealthState) error {
	healthPath := GetProviderHealthPath()
	dir := filepath.Dir(healthPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(state)
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, "provider-health.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, healthPath)
}

func MarkProviderUnhealthy(provider string, duration time.Duration) error {
	healthMu.Lock()
	defer healthMu.Unlock()

	lock, err := lockProviderHealth()
	if err != nil {
		return err
	}
	defer unlockProviderHealth(lock)

	state, err := ReadProviderHealth()
	if err != nil {
		return err
	}

	state.UnhealthyUntil[provider] = time.Now().Add(duration)
	return WriteProviderHealth(state)
}

func MarkProviderActive(provider string) error {
	healthMu.Lock()
	defer healthMu.Unlock()

	lock, err := lockProviderHealth()
	if err != nil {
		return err
	}
	defer unlockProviderHealth(lock)

	state, err := ReadProviderHealth()
	if err != nil {
		return err
	}

	state.ActiveProvider = provider
	delete(state.UnhealthyUntil, provider)
	return WriteProviderHealth(state)
}

func AcquireProbingLock(provider string) (bool, error) {
	healthMu.Lock()
	defer healthMu.Unlock()

	lock, err := lockProviderHealth()
	if err != nil {
		return false, err
	}
	defer unlockProviderHealth(lock)

	state, err := ReadProviderHealth()
	if err != nil {
		return false, err
	}

	if state.ProbingInProgress && state.ProbingProvider == provider {
		return false, nil // someone else is already probing
	}

	state.ProbingInProgress = true
	state.ProbingProvider = provider
	state.ProbingLeaderPID = os.Getpid()
	err = WriteProviderHealth(state)
	return err == nil, err
}

func ReleaseProbingLock(provider string) error {
	healthMu.Lock()
	defer healthMu.Unlock()

	lock, err := lockProviderHealth()
	if err != nil {
		return err
	}
	defer unlockProviderHealth(lock)

	state, err := ReadProviderHealth()
	if err != nil {
		return err
	}

	if state.ProbingProvider == provider && state.ProbingLeaderPID == os.Getpid() {
		state.ProbingInProgress = false
		state.ProbingProvider = ""
		state.ProbingLeaderPID = 0
		return WriteProviderHealth(state)
	}
	return nil
}

func WaitForProbing(ctx context.Context, provider string) (string, error) {
	healthPath := GetProviderHealthPath()
	watcher, err := fsnotify.NewWatcher()
	if err == nil {
		_ = watcher.Add(filepath.Dir(healthPath))
		defer watcher.Close()
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			state, err := ReadProviderHealth()
			if err == nil {
				if !state.ProbingInProgress || state.ProbingProvider != provider {
					return state.ActiveProvider, nil
				}
			}
		case event, ok := <-watcher.Events:
			if !ok {
				continue
			}
			if filepath.Base(event.Name) == filepath.Base(healthPath) &&
				(event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
				state, err := ReadProviderHealth()
				if err == nil {
					if !state.ProbingInProgress || state.ProbingProvider != provider {
						return state.ActiveProvider, nil
					}
				}
			}
		}
	}
}

func PrioritizeModels(primary Model, fallbacks []Model) []Model {
	state, err := ReadProviderHealth()
	if err != nil {
		return append([]Model{primary}, fallbacks...)
	}

	isHealthy := func(p string) bool {
		if state.UnhealthyUntil == nil {
			return true
		}
		until, ok := state.UnhealthyUntil[p]
		return !ok || time.Now().After(until)
	}

	primaryHealthy := isHealthy(primary.ModelCfg.Provider)
	if primaryHealthy {
		return append([]Model{primary}, fallbacks...)
	}

	var healthyFallbacks []Model
	var unhealthyFallbacks []Model
	var activeModel *Model

	for _, fb := range fallbacks {
		p := fb.ModelCfg.Provider
		if isHealthy(p) {
			if state.ActiveProvider != "" && p == state.ActiveProvider {
				fbCopy := fb
				activeModel = &fbCopy
			} else {
				healthyFallbacks = append(healthyFallbacks, fb)
			}
		} else {
			unhealthyFallbacks = append(unhealthyFallbacks, fb)
		}
	}

	var prioritized []Model
	if activeModel != nil {
		prioritized = append(prioritized, *activeModel)
	}
	prioritized = append(prioritized, healthyFallbacks...)
	prioritized = append(prioritized, unhealthyFallbacks...)
	prioritized = append(prioritized, primary)

	return prioritized
}

func WatchProviderHealth(ctx context.Context, currentProvider string, candidateModels []Model, onFailover func()) {
	healthPath := GetProviderHealthPath()
	dir := filepath.Dir(healthPath)

	watcher, err := fsnotify.NewWatcher()
	if err == nil {
		_ = watcher.Add(dir)
		defer watcher.Close()
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastActive string
	if state, err := ReadProviderHealth(); err == nil {
		lastActive = state.ActiveProvider
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if state, err := ReadProviderHealth(); err == nil {
				if state.ActiveProvider != lastActive {
					lastActive = state.ActiveProvider
					if shouldFailover(currentProvider, state, candidateModels) {
						slog.InfoContext(ctx, "Provider failover triggered via polling", "old", currentProvider, "new", state.ActiveProvider)
						onFailover()
						return
					}
				}
			}
		case event, ok := <-watcher.Events:
			if !ok {
				continue
			}
			if filepath.Base(event.Name) == filepath.Base(healthPath) &&
				(event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
				if state, err := ReadProviderHealth(); err == nil {
					if state.ActiveProvider != lastActive {
						lastActive = state.ActiveProvider
						if shouldFailover(currentProvider, state, candidateModels) {
							slog.InfoContext(ctx, "Provider failover triggered via file watch", "old", currentProvider, "new", state.ActiveProvider)
							onFailover()
							return
						}
					}
				}
			}
		case <-watcher.Errors:
		}
	}
}

func shouldFailover(currentProvider string, state *ProviderHealthState, candidateModels []Model) bool {
	if state.ActiveProvider == "" {
		return false
	}
	if currentProvider == state.ActiveProvider {
		return false
	}

	currentIdx := -1
	activeIdx := -1
	for i, m := range candidateModels {
		p := m.ModelCfg.Provider
		if p == currentProvider {
			currentIdx = i
		}
		if p == state.ActiveProvider {
			activeIdx = i
		}
	}

	if activeIdx == -1 {
		return false
	}

	if state.UnhealthyUntil != nil {
		if until, ok := state.UnhealthyUntil[currentProvider]; ok && time.Now().Before(until) {
			return true
		}
	}

	if activeIdx < currentIdx {
		return true
	}

	return false
}
