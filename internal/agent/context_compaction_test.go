package agent

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestCompactRedundantToolResults(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test", session.ModeExecute)
	require.NoError(t, err)

	// Create a sequence of messages with redundant tool calls.
	// 1. Assistant calls ls
	// 2. Tool returns ls result
	// 3. Assistant calls ls (again)
	// 4. Tool returns ls result (again)
	// 5. Assistant calls view
	// 6. Tool returns view result
	// 7. Assistant calls view (again)
	// 8. Tool returns view result (again)
	// 9. Extra tool messages to push older ones out of the protection window.
	
	// Turn 1: ls /
	a1, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
	})
	require.NoError(t, err)
	a1.AddToolCall(message.ToolCall{ID: "call1", Name: "ls", Input: `{"path":"/"}`})
	require.NoError(t, env.messages.Update(ctx, a1))

	tr1, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
	})
	require.NoError(t, err)
	tr1.AddToolResult(message.ToolResult{ToolCallID: "call1", Name: "ls", Content: "file1\nfile2"})
	require.NoError(t, env.messages.Update(ctx, tr1))

	// Turn 2: ls / (redundant)
	a2, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
	})
	require.NoError(t, err)
	a2.AddToolCall(message.ToolCall{ID: "call2", Name: "ls", Input: `{"path":"/"}`})
	require.NoError(t, env.messages.Update(ctx, a2))

	tr2, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
	})
	require.NoError(t, err)
	tr2.AddToolResult(message.ToolResult{ToolCallID: "call2", Name: "ls", Content: "file1\nfile2"})
	require.NoError(t, env.messages.Update(ctx, tr2))

	// Turn 3-5: extra tool messages to ensure we exceed compactionRedundancyProtectRecent (3)
	for i := 3; i <= 5; i++ {
		callID := fmt.Sprintf("call%d", i)
		ax, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
		require.NoError(t, err)
		ax.AddToolCall(message.ToolCall{ID: callID, Name: "bash", Input: fmt.Sprintf(`{"command":"echo %d"}`, i)})
		require.NoError(t, env.messages.Update(ctx, ax))

		trx, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
		require.NoError(t, err)
		trx.AddToolResult(message.ToolResult{ToolCallID: callID, Name: "bash", Content: fmt.Sprintf("%d", i)})
		require.NoError(t, env.messages.Update(ctx, trx))
	}

	// Now run compaction.
	agent.compactRedundantToolResults(ctx, sess.ID)

	// Verify results.
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	// tr1 should be compacted because tr2 is a newer ls / result.
	// Wait, we need to find tr1 in the msgs list.
	var tr1Found, tr2Found bool
	for _, m := range msgs {
		results := m.ToolResults()
		if len(results) == 0 {
			continue
		}
		if results[0].ToolCallID == "call1" {
			tr1Found = true
			require.Contains(t, results[0].Content, "Tool output omitted as redundant")
		}
		if results[0].ToolCallID == "call2" {
			tr2Found = true
			require.Equal(t, "file1\nfile2", results[0].Content)
		}
	}
	require.True(t, tr1Found)
	require.True(t, tr2Found)

	// Turn 6: view foo.txt
	a6, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	a6.AddToolCall(message.ToolCall{ID: "call6", Name: "view", Input: `{"file_path":"foo.txt"}`})
	require.NoError(t, env.messages.Update(ctx, a6))

	tr6, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
	require.NoError(t, err)
	tr6.AddToolResult(message.ToolResult{ToolCallID: "call6", Name: "view", Content: "content of foo"})
	require.NoError(t, env.messages.Update(ctx, tr6))

	// Turn 7: view foo.txt (redundant)
	a7, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	a7.AddToolCall(message.ToolCall{ID: "call7", Name: "view", Input: `{"file_path":"foo.txt"}`})
	require.NoError(t, env.messages.Update(ctx, a7))

	tr7, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
	require.NoError(t, err)
	tr7.AddToolResult(message.ToolResult{ToolCallID: "call7", Name: "view", Content: "content of foo"})
	require.NoError(t, env.messages.Update(ctx, tr7))

	// Add more tool messages to push tr6 out of protection.
	for i := 8; i <= 10; i++ {
		callID := fmt.Sprintf("call%d", i)
		ax, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
		require.NoError(t, err)
		ax.AddToolCall(message.ToolCall{ID: callID, Name: "bash", Input: fmt.Sprintf(`{"command":"echo %d"}`, i)})
		require.NoError(t, env.messages.Update(ctx, ax))

		trx, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
		require.NoError(t, err)
		trx.AddToolResult(message.ToolResult{ToolCallID: callID, Name: "bash", Content: fmt.Sprintf("%d", i)})
		require.NoError(t, env.messages.Update(ctx, trx))
	}

	agent.compactRedundantToolResults(ctx, sess.ID)

	msgs, err = env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	var tr6Found, tr7Found bool
	for _, m := range msgs {
		results := m.ToolResults()
		if len(results) == 0 {
			continue
		}
		if results[0].ToolCallID == "call6" {
			tr6Found = true
			require.Contains(t, results[0].Content, "Tool output omitted as redundant")
		}
		if results[0].ToolCallID == "call7" {
			tr7Found = true
			require.Equal(t, "content of foo", results[0].Content)
		}
	}
	require.True(t, tr6Found)
	require.True(t, tr7Found)

	// Turn 11: edit foo.txt
	a11, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	a11.AddToolCall(message.ToolCall{ID: "call11", Name: "edit", Input: `{"file_path":"foo.txt", "old_string":"a", "new_string":"b"}`})
	require.NoError(t, env.messages.Update(ctx, a11))

	tr11, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
	require.NoError(t, err)
	tr11.AddToolResult(message.ToolResult{ToolCallID: "call11", Name: "edit", Content: "Success"})
	require.NoError(t, env.messages.Update(ctx, tr11))

	// Turn 12: edit foo.txt (redundant)
	a12, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	a12.AddToolCall(message.ToolCall{ID: "call12", Name: "edit", Input: `{"file_path":"foo.txt", "old_string":"b", "new_string":"c"}`})
	require.NoError(t, env.messages.Update(ctx, a12))

	tr12, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
	require.NoError(t, err)
	tr12.AddToolResult(message.ToolResult{ToolCallID: "call12", Name: "edit", Content: "Success"})
	require.NoError(t, env.messages.Update(ctx, tr12))

	// Add more tool messages to push tr11 out of protection.
	for i := 13; i <= 15; i++ {
		callID := fmt.Sprintf("call%d", i)
		ax, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
		require.NoError(t, err)
		ax.AddToolCall(message.ToolCall{ID: callID, Name: "bash", Input: fmt.Sprintf(`{"command":"echo %d"}`, i)})
		require.NoError(t, env.messages.Update(ctx, ax))

		trx, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
		require.NoError(t, err)
		trx.AddToolResult(message.ToolResult{ToolCallID: callID, Name: "bash", Content: fmt.Sprintf("%d", i)})
		require.NoError(t, env.messages.Update(ctx, trx))
	}

	agent.compactRedundantToolResults(ctx, sess.ID)

	msgs, err = env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	var tr11Found, tr12Found bool
	for _, m := range msgs {
		results := m.ToolResults()
		if len(results) == 0 {
			continue
		}
		if results[0].ToolCallID == "call11" {
			tr11Found = true
			require.Contains(t, results[0].Content, "Tool output omitted as redundant")
		}
		if results[0].ToolCallID == "call12" {
			tr12Found = true
			require.Equal(t, "Success", results[0].Content)
		}
	}
	require.True(t, tr11Found)
	require.True(t, tr12Found)

	// Turn 16: evidence_batch
	a16, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	a16.AddToolCall(message.ToolCall{ID: "call16", Name: "evidence_batch", Input: `{"nodes":[{"id":"n1","kind":"ls","path":"/"}]}`})
	require.NoError(t, env.messages.Update(ctx, a16))

	tr16, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
	require.NoError(t, err)
	tr16.AddToolResult(message.ToolResult{ToolCallID: "call16", Name: "evidence_batch", Content: "file1\nfile2"})
	require.NoError(t, env.messages.Update(ctx, tr16))

	// Turn 17: evidence_batch (redundant, different whitespace/order in JSON)
	a17, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	a17.AddToolCall(message.ToolCall{ID: "call17", Name: "evidence_batch", Input: `{"nodes": [ { "kind": "ls", "id": "n1", "path": "/" } ]}`})
	require.NoError(t, env.messages.Update(ctx, a17))

	tr17, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
	require.NoError(t, err)
	tr17.AddToolResult(message.ToolResult{ToolCallID: "call17", Name: "evidence_batch", Content: "file1\nfile2"})
	require.NoError(t, env.messages.Update(ctx, tr17))

	// Add more tool messages to push tr16 out of protection.
	for i := 18; i <= 20; i++ {
		callID := fmt.Sprintf("call%d", i)
		ax, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Assistant})
		require.NoError(t, err)
		ax.AddToolCall(message.ToolCall{ID: callID, Name: "bash", Input: fmt.Sprintf(`{"command":"echo %d"}`, i)})
		require.NoError(t, env.messages.Update(ctx, ax))

		trx, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{Role: message.Tool})
		require.NoError(t, err)
		trx.AddToolResult(message.ToolResult{ToolCallID: callID, Name: "bash", Content: fmt.Sprintf("%d", i)})
		require.NoError(t, env.messages.Update(ctx, trx))
	}

	agent.compactRedundantToolResults(ctx, sess.ID)

	msgs, err = env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	var tr16Found, tr17Found bool
	for _, m := range msgs {
		results := m.ToolResults()
		if len(results) == 0 {
			continue
		}
		if results[0].ToolCallID == "call16" {
			tr16Found = true
			require.Contains(t, results[0].Content, "Tool output omitted as redundant")
		}
		if results[0].ToolCallID == "call17" {
			tr17Found = true
			require.Equal(t, "file1\nfile2", results[0].Content)
		}
	}
	require.True(t, tr16Found)
	require.True(t, tr17Found)
}

func TestNormalizeToolInput(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		input    string
		expected string
	}{
		{
			"json_normalized",
			"ls",
			`{"path": "/", "depth": 1}`,
			`{"depth":1,"path":"/"}`, // alphabetical order by json.Marshal
		},
		{
			"json_with_whitespace",
			"ls",
			`{  "path" :   "/"   }`,
			`{"path":"/"}`,
		},
		{
			"non_json",
			"bash",
			"ls -la",
			"ls -la",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, normalizeToolInput(tc.toolName, tc.input))
		})
	}
}
