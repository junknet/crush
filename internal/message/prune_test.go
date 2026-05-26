package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestService_Prune(t *testing.T) {
	svc, sessionID := newTestService(t, WithDebounce(0))

	// 1. Create a message with large binary data.
	largeData := make([]byte, 20*1024) // 20KB
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role: User,
	})
	require.NoError(t, err)
	msg.AddBinary("application/octet-stream", largeData)
	require.NoError(t, svc.Update(t.Context(), msg))

	// 2. Create another message that is not seen yet (latest assistant message).
	assistantMsg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role: Assistant,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Update(t.Context(), assistantMsg))

	// 3. Prune should remove the large data because it's 'seen'.
	require.NoError(t, svc.Prune(t.Context()))

	// 4. Verify the data is pruned.
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Len(t, got.BinaryContent(), 1)
	require.Nil(t, got.BinaryContent()[0].Data, "Large binary data should be pruned")

	// 5. Test ToolResult pruning.
	msg2, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role: Tool,
	})
	require.NoError(t, err)
	largeDataStr := string(make([]byte, 20*1024)) // 20KB
	msg2.AddToolResult(ToolResult{
		ToolCallID: "tc1",
		Data:       largeDataStr,
	})
	require.NoError(t, svc.Update(t.Context(), msg2))

	// Prune again. msg2 is 'seen' because it's before the last assistant message?
	// Let's create another assistant message to make msg2 'seen'.
	assistantMsg2, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role: Assistant,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Update(t.Context(), assistantMsg2))

	require.NoError(t, svc.Prune(t.Context()))

	got2, err := svc.Get(t.Context(), msg2.ID)
	require.NoError(t, err)
	require.Len(t, got2.ToolResults(), 1)
	require.Equal(t, "(pruned)", got2.ToolResults()[0].Data)
}
