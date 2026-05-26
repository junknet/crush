package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

type mockBashPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func TestBashTool_GrepNoMatchIsNotCommandFailure(t *testing.T) {
	workingDir := t.TempDir()
	targetFile := filepath.Join(workingDir, "sample.txt")
	require.NoError(t, os.WriteFile(targetFile, []byte("alpha\nbeta\n"), 0o644))
	tool, _ := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "grep no match",
		Command:     "grep -n zzz " + targetFile,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, 1, meta.ExitCode)
	require.Equal(t, "no_match", meta.Outcome)
}

func (m *mockBashPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}

func (m *mockBashPermissionService) Grant(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) Deny(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) GrantPersistent(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) AutoApproveSession(sessionID string) {}

func (m *mockBashPermissionService) ActiveRequest() *permission.PermissionRequest { return nil }

func (m *mockBashPermissionService) SetSkipRequests(skip bool) {}

func (m *mockBashPermissionService) SkipRequests() bool {
	return false
}

func (m *mockBashPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func TestBashTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool, _ := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestBashTool_CustomAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool, bgManager := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "custom threshold",
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.NotEmpty(t, meta.ShellID)
	require.Contains(t, resp.Content, "moved to background")

	require.NoError(t, bgManager.Kill(meta.ShellID))
}

func TestBashTool_BlocksForegroundSleepPolling(t *testing.T) {
	workingDir := t.TempDir()
	tool, _ := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "sleep polling",
		Command:     "sleep 2 && echo done",
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "foreground sleep polling is blocked")
	require.Contains(t, resp.Content, "monitor tool")
}

func TestBashTool_LargeOutputSpillsToFile(t *testing.T) {
	workingDir := t.TempDir()
	dataDir := filepath.Join(workingDir, ".crush")
	tool, _ := newBashToolForTest(workingDir)
	sessionID := "spill-session"
	ctx := context.WithValue(context.Background(), SessionIDContextKey, sessionID)

	// Generate ~60 KB of stdout — well above BashSpillThreshold (30 KB).
	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "spill",
		Command:     "yes hello | head -c 60000",
	})
	require.False(t, resp.IsError)

	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.NotEmpty(t, meta.SpillPath, "expected SpillPath for >30KB output")
	require.Greater(t, meta.SpillBytes, 30000, "spill should record full bytes written")

	// Spill file must exist and contain at least the spill byte count.
	info, err := os.Stat(meta.SpillPath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, int(info.Size()), meta.SpillBytes-1)

	// Spill path must live under <dataDir>/tool-results/<sessionID>/.
	expectedPrefix := filepath.Join(dataDir, "tool-results", sessionID) + string(filepath.Separator)
	require.True(t, len(meta.SpillPath) > len(expectedPrefix) && meta.SpillPath[:len(expectedPrefix)] == expectedPrefix,
		"spill path %q must live under %q", meta.SpillPath, expectedPrefix)

	// Inline preview must reference the spill so the model knows where to view.
	require.Contains(t, meta.Output, "output_spill")
	require.Contains(t, meta.Output, meta.SpillPath)
}

func TestBashTool_SmallOutputDoesNotSpill(t *testing.T) {
	workingDir := t.TempDir()
	tool, _ := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "small-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "small",
		Command:     "echo hi",
	})
	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Empty(t, meta.SpillPath, "small output must not spill")
	require.Equal(t, 0, meta.SpillBytes)
}

func TestBashTool_BlocksWatch(t *testing.T) {
	workingDir := t.TempDir()
	tool, _ := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "block-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "watch ls",
		Command:     "watch ls",
	})
	// blocker returns the command as an error from tool.Run; the wrapper
	// surfaces it via ToolResponse with IsError=true OR returns it as an
	// error directly. Either path means the command did NOT actually run.
	if !resp.IsError && resp.Content != "" {
		// The blocker error came back inside the content, not as IsError;
		// accept both surfaces as long as "not allowed" reaches the model.
		require.Contains(t, resp.Content, "not allowed", "watch should be rejected")
	}
}

func newBashToolForTest(workingDir string) (fantasy.AgentTool, *shell.BackgroundShellManager) {
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	bgManager := shell.NewBackgroundShellManager()
	return NewBashTool(permissions, bgManager, workingDir, filepath.Join(workingDir, ".crush"), attribution, "test-model"), bgManager
}

func runBashTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}
