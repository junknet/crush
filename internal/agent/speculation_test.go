package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpeculativeSession_PrepareAndPromote(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	// 1. Create a parent session
	parent, err := env.sessions.Create(ctx, "Test Session", session.ModeExecute)
	require.NoError(t, err)

	// 2. Add some messages to the parent session
	m1, err := env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "Hello"}},
	})
	require.NoError(t, err)

	m2, err := env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "Hi there"}},
	})
	require.NoError(t, err)

	// Flush all messages to DB
	err = env.messages.FlushAll(ctx)
	require.NoError(t, err)

	specID := parent.ID + "-speculate"

	// 3. Prepare speculative session
	err = env.sessions.PrepareSpeculativeSession(ctx, parent.ID, specID)
	require.NoError(t, err)

	// 4. Verify speculative session exists
	specSession, err := env.sessions.Get(ctx, specID)
	require.NoError(t, err)
	assert.Equal(t, "Speculative: Test Session", specSession.Title)

	// 5. Verify copied messages exist with suffix "-speculate"
	specMsgs, err := env.messages.List(ctx, specID)
	require.NoError(t, err)
	require.Len(t, specMsgs, 2)
	assert.Equal(t, m1.ID+"-speculate", specMsgs[0].ID)
	assert.Equal(t, m2.ID+"-speculate", specMsgs[1].ID)

	// 6. Simulate speculative execution adding new messages
	mNew, err := env.messages.Create(ctx, specID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "I am speculating"}},
	})
	require.NoError(t, err)

	err = env.messages.FlushAll(ctx)
	require.NoError(t, err)

	// 7. Promote speculative session
	err = env.sessions.PromoteSpeculativeSession(ctx, parent.ID)
	require.NoError(t, err)

	// 8. Verify speculative session and its copied messages are cleaned up
	_, err = env.sessions.Get(ctx, specID)
	assert.Error(t, err) // Should not exist

	// 9. Verify new message was promoted to the parent session
	parentMsgs, err := env.messages.List(ctx, parent.ID)
	require.NoError(t, err)
	require.Len(t, parentMsgs, 3)
	assert.Equal(t, m1.ID, parentMsgs[0].ID)
	assert.Equal(t, m2.ID, parentMsgs[1].ID)
	assert.Equal(t, mNew.ID, parentMsgs[2].ID)
	assert.Equal(t, parent.ID, parentMsgs[2].SessionID)
}

func TestSpeculativeToolWrapper(t *testing.T) {
	// Setup dummy tool
	dummy := &dummyTool{}
	wrapper := &speculativeToolWrapper{inner: dummy}

	// 1. Run safe tool (e.g. view)
	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "view",
		Input: "{}",
	})
	require.NoError(t, err)
	assert.Equal(t, "view run", resp.Content)
	assert.False(t, resp.StopTurn)

	// 2. Run unsafe tool (e.g. write)
	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "write",
		Input: "{}",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "not allowed during speculative execution")
	assert.True(t, resp.StopTurn)
}

type dummyTool struct{}

func (d *dummyTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: "dummy"}
}
func (d *dummyTool) ProviderOptions() fantasy.ProviderOptions        { return fantasy.ProviderOptions{} }
func (d *dummyTool) SetProviderOptions(opts fantasy.ProviderOptions) {}
func (d *dummyTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{Content: call.Name + " run"}, nil
}
