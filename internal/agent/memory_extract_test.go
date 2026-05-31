package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasMemoryWrites(t *testing.T) {
	memoryDir := "/workspace/project/memory"

	// 1. No writes
	msgs1 := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hi"},
			},
		},
	}
	assert.False(t, hasMemoryWrites(msgs1, memoryDir))

	// 2. Normal writes outside memoryDir
	msgs2 := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Write file"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					Name:  "Write",
					Input: `{"file_path":"/workspace/project/src/main.go"}`,
				},
			},
		},
	}
	assert.False(t, hasMemoryWrites(msgs2, memoryDir))

	// 3. Write inside memoryDir
	msgs3 := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Write memory"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					Name:  "Write",
					Input: `{"file_path":"/workspace/project/memory/file.md"}`,
				},
			},
		},
	}
	assert.True(t, hasMemoryWrites(msgs3, memoryDir))

	msgs4 := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Write adjacent file"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					Name:  "Write",
					Input: `{"file_path":"/workspace/project/memory_backup/file.md"}`,
				},
			},
		},
	}
	assert.False(t, hasMemoryWrites(msgs4, memoryDir))
}

func TestShouldTriggerWorkspaceMemoryExtraction(t *testing.T) {
	t.Parallel()

	t.Run("skips short ordinary turns", func(t *testing.T) {
		t.Parallel()

		msgs := []message.Message{
			{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "what changed?"}}},
			{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "nothing durable"}}},
		}

		assert.False(t, shouldTriggerWorkspaceMemoryExtraction(msgs, workspaceMemoryExtractionState{}, 500))
	})

	t.Run("explicit durable prompt bypasses token threshold", func(t *testing.T) {
		t.Parallel()

		msgs := []message.Message{
			{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "以后都不要用 Python，记住这个偏好"}}},
			{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "已记录"}}},
		}

		assert.True(t, shouldTriggerWorkspaceMemoryExtraction(msgs, workspaceMemoryExtractionState{}, 500))
	})

	t.Run("requires token growth before background extraction", func(t *testing.T) {
		t.Parallel()

		msgs := []message.Message{
			{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "continue"}}},
			{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{Name: "rg", Input: "{}"}}},
		}
		state := workspaceMemoryExtractionState{
			SessionID:              "s1",
			LastMessageID:          "u1",
			TokensAtLastExtraction: 9_000,
			Initialized:            true,
		}

		assert.False(t, shouldTriggerWorkspaceMemoryExtraction(msgs, state, 12_000))
	})

	t.Run("triggers at natural break after enough context growth", func(t *testing.T) {
		t.Parallel()

		msgs := []message.Message{
			{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "continue"}}},
			{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "finished a large investigation"}}},
		}
		state := workspaceMemoryExtractionState{
			SessionID:              "s1",
			LastMessageID:          "u1",
			TokensAtLastExtraction: 8_000,
			Initialized:            true,
		}

		assert.True(t, shouldTriggerWorkspaceMemoryExtraction(msgs, state, 14_000))
	})

	t.Run("triggers after enough tool activity and context growth", func(t *testing.T) {
		t.Parallel()

		msgs := []message.Message{
			{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "audit this"}}},
			{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{
				message.ToolCall{Name: "rg", Input: "{}"},
				message.ToolCall{Name: "Read", Input: "{}"},
				message.ToolCall{Name: "agent", Input: "{}"},
			}},
		}
		state := workspaceMemoryExtractionState{
			SessionID:              "s1",
			LastMessageID:          "u1",
			TokensAtLastExtraction: 8_000,
			Initialized:            true,
		}

		assert.True(t, shouldTriggerWorkspaceMemoryExtraction(msgs, state, 14_000))
	})
}

func TestWorkspaceMemoryExtractionStateIsSessionScoped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, writeWorkspaceMemoryExtractionState(dir, workspaceMemoryExtractionState{
		SessionID:              "session-a",
		LastMessageID:          "message-a",
		TokensAtLastExtraction: 12_000,
		Initialized:            true,
	}))

	assert.True(t, readWorkspaceMemoryExtractionState(dir, "session-a").Initialized)
	assert.False(t, readWorkspaceMemoryExtractionState(dir, "session-b").Initialized)
}

func TestMemExtractToolWrapper(t *testing.T) {
	dummy := &dummyTool{}
	memoryDir := "/workspace/project/memory"
	wrapper := &memExtractToolWrapper{inner: dummy, memoryDir: memoryDir}

	// 1. Run safe filename search tool.
	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.SearchToolName,
		Input: `{"files_only":true}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "Search run", resp.Content)
	assert.False(t, resp.StopTurn)

	// 2. Run write tool targeting memory dir
	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "Write",
		Input: `{"file_path":"/workspace/project/memory/test.md"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "Write run", resp.Content)
	assert.False(t, resp.StopTurn)

	// 3. Run write tool targeting other dir
	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "Write",
		Input: `{"file_path":"/workspace/project/src/test.md"}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "restricted to the memory directory")
	assert.True(t, resp.StopTurn)
}

func TestReadOnlyToolWrapper(t *testing.T) {
	wrapper := &readOnlyToolWrapper{inner: &dummyTool{}}

	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.SearchToolName,
		Input: "{}",
	})
	require.NoError(t, err)
	assert.Equal(t, "Search run", resp.Content)
	assert.False(t, resp.StopTurn)

	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.WriteToolName,
		Input: `{"file_path":"MEMORY.md","content":"x"}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "read-only")
	assert.True(t, resp.StopTurn)

	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  AgentToolName,
		Input: `{"role":"explore","prompt":"inspect"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "Agent run", resp.Content)
	assert.False(t, resp.StopTurn)

	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  AgentToolName,
		Input: `{"role":"worker","prompt":"edit"}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "blocked because this turn is read-only")
	assert.True(t, resp.StopTurn)
}

func TestReadOnlyBatchPolicy(t *testing.T) {
	wrapper := &readOnlyToolWrapper{inner: &dummyTool{}}

	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name: tools.EvidenceBatchToolName,
		Input: `{"nodes":[
			{"id":"files","tool":"rg","pattern":"*.go","files_only":true},
			{"id":"matches","tool":"rg","pattern":"ContextWindow","path":"internal"}
		]}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "Batch run", resp.Content)
	assert.False(t, resp.StopTurn)

	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.EvidenceBatchToolName,
		Input: `{"nodes":[{"id":"shell","tool":"shell","command":"date"}]}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "only allow evidence read nodes")
	assert.True(t, resp.StopTurn)
}

func TestReadOnlyCodeTriagePolicy(t *testing.T) {
	wrapper := &readOnlyToolWrapper{inner: &dummyTool{}}

	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.CodeTriageToolName,
		Input: `{"queries":[{"id":"q","query":"TODO"}],"check_commands":[{"name":"quick","command":"go test ./..."}]}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "CodeTriage run", resp.Content)
	assert.False(t, resp.StopTurn)

	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.CodeTriageToolName,
		Input: `{"check_commands":[{"name":"unsafe","command":"rm -rf /tmp/foo"}]}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "Code triage check command")
	assert.True(t, resp.StopTurn)
}

func TestPromptReadOnlyAndExplorePreflightDetection(t *testing.T) {
	t.Parallel()

	require.True(t, promptRequestsReadOnly("只做评估，不修改文件。定位 LLVM IR bug"))
	require.True(t, promptRequestsReadOnly("read-only: diagnose this repo"))
	require.False(t, promptRequestsReadOnly("修理好去"))

	require.True(t, promptNeedsExplorePreflight("/home/junknet/linege/nim-src 去这里分析bug 定位llvm IR问题设计 这个任务做评估"))
	require.False(t, promptNeedsExplorePreflight("改 README 标题"))
}

type dummyTool struct{}

func (d *dummyTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: "dummy"}
}

func (d *dummyTool) ProviderOptions() fantasy.ProviderOptions {
	return fantasy.ProviderOptions{}
}

func (d *dummyTool) SetProviderOptions(fantasy.ProviderOptions) {}

func (d *dummyTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{Content: call.Name + " run"}, nil
}
