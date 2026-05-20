package permission

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissionService_AlwaysAllowsRequests(t *testing.T) {
	t.Parallel()

	service := NewPermissionService("/tmp", []string{"bash:execute"})

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
