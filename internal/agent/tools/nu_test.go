package tools

import (
	"context"
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

type mockNuPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockNuPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}
func (m *mockNuPermissionService) Grant(req permission.PermissionRequest)           {}
func (m *mockNuPermissionService) Deny(req permission.PermissionRequest)            {}
func (m *mockNuPermissionService) GrantPersistent(req permission.PermissionRequest) {}
func (m *mockNuPermissionService) AutoApproveSession(sessionID string)              {}
func (m *mockNuPermissionService) ActiveRequest() *permission.PermissionRequest     { return nil }
func (m *mockNuPermissionService) SetSkipRequests(skip bool)                        {}
func (m *mockNuPermissionService) SkipRequests() bool                               { return false }
func (m *mockNuPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func TestNuTool(t *testing.T) {
	perm := &mockNuPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	tool := NewNuTool(perm, ".")

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	t.Run("basic command", func(t *testing.T) {
		params := NuParams{Command: "echo 'hello'"}
		input, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, fantasy.ToolCall{Input: string(input)})
		require.NoError(t, err)
		require.Contains(t, resp.Content, "hello")
	})

	t.Run("json output", func(t *testing.T) {
		// Nushell 'ls' outputs a table, we can convert to json
		params := NuParams{Command: "ls | first 1 | to json"}
		input, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, fantasy.ToolCall{Input: string(input)})
		require.NoError(t, err)

		var jsonData any
		err = json.Unmarshal([]byte(resp.Content), &jsonData)
		require.NoError(t, err, "Output should be valid JSON")
	})

	t.Run("error command", func(t *testing.T) {
		params := NuParams{Command: "nonexistent_command"}
		input, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, fantasy.ToolCall{Input: string(input)})
		require.NoError(t, err)
		// Nushell error output usually contains "Error"
		require.Contains(t, resp.Content, "Error")
	})
}
