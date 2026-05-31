package prompt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/stretchr/testify/require"
)

// newTestStore returns an initialized config store rooted at a temp working dir.
func newTestStore(t *testing.T) *config.ConfigStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "crush-test-wd-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	store, err := config.Init(tmpDir, "", false)
	require.NoError(t, err)
	return store
}

func buildConstitution(t *testing.T, store *config.ConfigStore, role string) string {
	t.Helper()
	p, err := NewPrompt(role, "template")
	require.NoError(t, err)
	data, err := p.promptData(context.Background(), "provider", "model", store)
	require.NoError(t, err)
	return data.UserConstitution
}

// A principle is injected only into the roles named in its Roles list; a role
// not listed (e.g. explore) receives nothing.
func TestConstitutionPerPrincipleTargeting(t *testing.T) {
	store := newTestStore(t)
	store.Config().Options.Constitution = []config.ConstitutionPrinciple{
		{Roles: []string{config.AgentBrain, config.AgentPlan, config.AgentAuditor}, Text: "架构优先：第一性原理"},
		{Roles: []string{config.AgentBrain, config.AgentWorker, config.AgentAuditor}, Text: "测试红线：E2E+集成,单测不算完成"},
	}

	brain := buildConstitution(t, store, config.AgentBrain)
	require.Contains(t, brain, "架构优先")
	require.Contains(t, brain, "测试红线")

	worker := buildConstitution(t, store, config.AgentWorker)
	require.NotContains(t, worker, "架构优先", "worker is not targeted by 架构优先")
	require.Contains(t, worker, "测试红线")

	plan := buildConstitution(t, store, config.AgentPlan)
	require.Contains(t, plan, "架构优先")
	require.NotContains(t, plan, "测试红线", "plan is not targeted by 测试红线")

	require.Empty(t, buildConstitution(t, store, config.AgentExplore), "explore is targeted by no principle")
}

// Matched principles are concatenated in declaration order for a role.
func TestConstitutionOrderedConcatenation(t *testing.T) {
	store := newTestStore(t)
	store.Config().Options.Constitution = []config.ConstitutionPrinciple{
		{Roles: []string{config.AgentBrain}, Text: "FIRST"},
		{Roles: []string{config.AgentWorker}, Text: "WORKER-ONLY"},
		{Roles: []string{config.AgentBrain}, Text: "SECOND"},
	}
	brain := buildConstitution(t, store, config.AgentBrain)
	require.Equal(t, "FIRST\n\nSECOND", brain)
	require.NotContains(t, brain, "WORKER-ONLY")
}

// A principle Text that resolves to a readable file is loaded as that file.
func TestConstitutionPrincipleFromFile(t *testing.T) {
	store := newTestStore(t)
	dir, err := os.MkdirTemp("", "crush-test-const-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "principle.md")
	require.NoError(t, os.WriteFile(path, []byte("目录洁癖：不产临时文件"), 0o644))

	store.Config().Options.Constitution = []config.ConstitutionPrinciple{
		{Roles: []string{config.AgentWorker}, Text: path},
	}
	got := buildConstitution(t, store, config.AgentWorker)
	require.Contains(t, got, "目录洁癖")
	require.Contains(t, got, "<file path=")
}

// With no constitution configured, nothing is injected — and a legacy
// ~/.claude/CLAUDE.md must NOT be picked up (the old hardcoded source is gone).
func TestConstitutionEmptyAndIgnoresLegacyClaudeMd(t *testing.T) {
	mockHome, err := os.MkdirTemp("", "crush-test-home-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(mockHome) })

	home.SetDir(mockHome)
	t.Cleanup(home.ResetDir)

	claudeDir := filepath.Join(mockHome, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("Legacy Constitution"), 0o644))

	store := newTestStore(t)
	store.Config().Options.Constitution = nil

	require.Empty(t, buildConstitution(t, store, config.AgentBrain), "no legacy ~/.claude/CLAUDE.md injection")
}
