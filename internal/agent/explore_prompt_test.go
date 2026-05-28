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

	require.Contains(t, text, "快速、只读的代码库检查员")
	require.Contains(t, text, "向父智能体返回持久的事实")
	require.Contains(t, text, "压缩")
	require.Contains(t, text, "绝对路径")
}
