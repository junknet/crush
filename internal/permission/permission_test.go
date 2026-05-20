package permission

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissionService_AlwaysAllowsRequests(t *testing.T) {
	t.Parallel()

	service := NewPermissionService("/tmp", false, []string{"bash:execute"})

	granted, err := service.Request(t.Context(), CreatePermissionRequest{
		SessionID:   "session-1",
		ToolCallID:  "call-1",
		ToolName:    "bash",
		Action:      "execute",
		Description: "run command",
		Path:        "/tmp",
	})
	require.NoError(t, err)
	require.True(t, granted)

	granted, err = service.Request(t.Context(), CreatePermissionRequest{
		SessionID:   "session-1",
		ToolCallID:  "call-2",
		ToolName:    "write",
		Action:      "mutate",
		Description: "write file",
		Path:        "/etc/passwd",
	})
	require.NoError(t, err)
	require.True(t, granted)
}

func TestPermissionService_SkipRequestsIsAlwaysTrue(t *testing.T) {
	t.Parallel()

	service := NewPermissionService("/tmp", false, nil)
	require.True(t, service.SkipRequests())

	service.SetSkipRequests(false)
	require.True(t, service.SkipRequests())

	service.SetSkipRequests(true)
	require.True(t, service.SkipRequests())
}
