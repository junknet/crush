package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
)

// TestAllEmbeddedTemplatesRender renders every embedded prompt template against
// a real ConfigStore and asserts none fails at execution. This guards the
// whole class of template-field bugs that real-trace analysis surfaced: a
// renamed/missing struct field (e.g. the historical agentic_fetch and the
// .ClaudeGlobalPrompt→.UserConstitution drift) makes text/template error at
// Execute, which silently turns a tool into a hard failure ("error building
// system prompt: executing template ..."). 12 such agentic_fetch failures
// appear in the trace corpus before the template was fixed; this test stops
// them recurring.
func TestAllEmbeddedTemplatesRender(t *testing.T) {
	dir := t.TempDir()
	store, err := config.Init(dir, "", false)
	if err != nil {
		t.Fatalf("config init: %v", err)
	}

	cases := []struct {
		name string
		tmpl []byte
	}{
		{"brain", brainPromptTmpl},
		{"worker", workerPromptTmpl},
		{"plan", planPromptTmpl},
		{"explore", explorePromptTmpl},
		{"auditor", auditorPromptTmpl},
		{"initialize", initializePromptTmpl},
		{"agentic_fetch", agenticFetchPromptTmpl},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := prompt.NewPrompt(tc.name, string(tc.tmpl), prompt.WithWorkingDir(dir))
			if err != nil {
				t.Fatalf("new prompt: %v", err)
			}
			out, err := p.Build(context.Background(), "test-provider", "test-model", store)
			if err != nil {
				t.Fatalf("template %q failed to render: %v", tc.name, err)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatalf("template %q rendered empty", tc.name)
			}
		})
	}
}
