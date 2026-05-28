package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/memdir"
)

// fixturePromptStore builds a ConfigStore + seeded MEMORY.md so the prompt
// renderer has both an available_skills surface (from builtin embeds) and a
// non-empty auto_memory section to potentially inject. Both signals are
// observable in the rendered output and are exactly what thin-agent guards
// must suppress.
func fixturePromptStore(t *testing.T) (*config.ConfigStore, string) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{
		Options: &config.Options{
			DataDirectory: tmp,
			ContextPaths:  []string{},
			SkillsPaths:   []string{},
		},
	}
	// Seed a MEMORY.md so IndexPrompt returns non-empty content for the
	// default workspace slug (store.WorkingDir() == "" → "default").
	if err := memdir.EnsureWorkspace(tmp, ""); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if err := os.WriteFile(memdir.IndexPath(tmp, ""), []byte("- [Pref](pref.md) — user prefers Nim\n"), 0o644); err != nil {
		t.Fatalf("seed MEMORY.md: %v", err)
	}
	return config.NewTestStore(cfg), tmp
}

func renderPrompt(t *testing.T, name, tmpl string, store *config.ConfigStore, workingDir string) string {
	t.Helper()
	p, err := NewPrompt(name, tmpl, WithWorkingDir(workingDir))
	if err != nil {
		t.Fatalf("NewPrompt(%s): %v", name, err)
	}
	out, err := p.Build(context.Background(), "test", "test-model", store)
	if err != nil {
		t.Fatalf("Build(%s): %v", name, err)
	}
	return out
}

// minimalTpl exercises both Skill and Memory guards so the test does not
// depend on the production templates' shifting structure.
const minimalTpl = `name={{.Provider}}
{{- if .AvailSkillXML}}
<available_skills>
{{.AvailSkillXML}}
</available_skills>
{{end}}
{{- if .MemoryIndex}}
{{.MemoryIndex}}
{{end}}
END
`

func TestThinAgent_OmitsSkillsAndMemory(t *testing.T) {
	store, tmp := fixturePromptStore(t)
	ws := filepath.Join(tmp, "ws")

	t.Run("brain renders skills but not memory", func(t *testing.T) {
		out := renderPrompt(t, "brain", minimalTpl, store, ws)
		if !strings.Contains(out, "<available_skills>") {
			t.Errorf("brain prompt missing available_skills block: %s", out)
		}
		if strings.Contains(out, "<auto_memory>") {
			t.Errorf("brain prompt unexpectedly contains auto_memory block: %s", out)
		}
	})

	thin := []string{"worker", "explore", "plan", "agentic_fetch", "speculation"}
	for _, name := range thin {
		t.Run(name+" omits both", func(t *testing.T) {
			out := renderPrompt(t, name, minimalTpl, store, ws)
			if strings.Contains(out, "<available_skills>") {
				t.Errorf("%s prompt unexpectedly contains available_skills: %s", name, out)
			}
			if strings.Contains(out, "<auto_memory>") {
				t.Errorf("%s prompt unexpectedly contains auto_memory: %s", name, out)
			}
		})
	}
}
