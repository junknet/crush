package skills

import (
	"sort"
	"sync"
)

// Tracker tracks which skills have been loaded (read) during a session.
// It is safe for concurrent use.
//
// Note: Tracking is name-based and limited to active skills only. If a builtin
// skill is overridden by a user skill, only the user skill (which is active)
// can be marked as loaded. This prevents misattribution when reading builtin
// files that have been overridden.
type Tracker struct {
	mu          sync.RWMutex
	loaded      map[string]bool
	activeNames map[string]string // Mapping from skill name to its SkillFilePath
}

// NewTracker creates a new skill tracker with the given active skill names.
// Only skills in activeSkills can be marked as loaded.
func NewTracker(activeSkills []*Skill) *Tracker {
	activeNames := make(map[string]string, len(activeSkills))
	for _, s := range activeSkills {
		activeNames[s.Name] = s.SkillFilePath
	}
	return &Tracker{
		loaded:      make(map[string]bool),
		activeNames: activeNames,
	}
}

// MarkLoaded marks a skill as having been loaded.
// Only marks as loaded if the skill is in the active set (not overridden/disabled).
func (t *Tracker) MarkLoaded(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Only track if this skill is actually active (not overridden by user skill).
	if _, ok := t.activeNames[name]; ok {
		t.loaded[name] = true
	}
}

// IsLoaded returns true if the skill has been loaded.
func (t *Tracker) IsLoaded(name string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.loaded[name]
}

// LoadedNames returns the names of all skills that have been loaded, sorted
// alphabetically. Safe to call on a nil Tracker (returns nil).
func (t *Tracker) LoadedNames() []string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.loaded) == 0 {
		return nil
	}
	names := make([]string, 0, len(t.loaded))
	for name := range t.loaded {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetPath returns the skill file path for the given skill name.
// Returns empty string if the skill is not active.
func (t *Tracker) GetPath(name string) string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.activeNames[name]
}

// LoadedCount returns the number of unique skills that have been loaded.
// Safe to call on a nil Tracker (returns 0).
func (t *Tracker) LoadedCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.loaded)
}
