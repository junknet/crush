package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentprompt "github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestExplorePromptEmphasizesCompressionAndEvidence(t *testing.T) {
	workDir := t.TempDir()

	store, err := config.Init(workDir, "", false)
	require.NoError(t, err)

	fixedTime := func() time.Time {
		return time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	}

	prompt, err := explorePrompt(
		agentprompt.WithTimeFunc(fixedTime),
		agentprompt.WithPlatform("linux"),
		agentprompt.WithWorkingDir(filepath.ToSlash(workDir)),
	)
	require.NoError(t, err)

	text, err := prompt.Build(context.Background(), "", "", store)
	require.NoError(t, err)

	require.Contains(t, text, "fast, read-only repository inspector and evidence collector")
	require.Contains(t, text, "smallest set of durable facts")
	require.Contains(t, text, "strategy, state, tool, or compression bugs")
	require.Contains(t, text, "COMPRESSION")
	require.Contains(t, text, "Absolute file paths")
}
