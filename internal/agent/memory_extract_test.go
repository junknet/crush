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
					Name:  "write",
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
					Name:  "write",
					Input: `{"file_path":"/workspace/project/memory/file.md"}`,
				},
			},
		},
	}
	assert.True(t, hasMemoryWrites(msgs3, memoryDir))
}

func TestMemExtractToolWrapper(t *testing.T) {
	dummy := &dummyTool{}
	memoryDir := "/workspace/project/memory"
	wrapper := &memExtractToolWrapper{inner: dummy, memoryDir: memoryDir}

	// 1. Run safe filename search tool.
	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "rg",
		Input: `{"files_only":true}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "rg run", resp.Content)
	assert.False(t, resp.StopTurn)

	// 2. Run write tool targeting memory dir
	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "write",
		Input: `{"file_path":"/workspace/project/memory/test.md"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "write run", resp.Content)
	assert.False(t, resp.StopTurn)

	// 3. Run write tool targeting other dir
	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "write",
		Input: `{"file_path":"/workspace/project/src/test.md"}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "restricted to the memory directory")
	assert.True(t, resp.StopTurn)
}

func TestReadOnlyToolWrapper(t *testing.T) {
	wrapper := &readOnlyToolWrapper{inner: &dummyTool{}}

	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.RgToolName,
		Input: "{}",
	})
	require.NoError(t, err)
	assert.Equal(t, "rg run", resp.Content)
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
	assert.Equal(t, "agent run", resp.Content)
	assert.False(t, resp.StopTurn)

	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  AgentToolName,
		Input: `{"role":"worker","prompt":"edit"}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "blocked because this turn is read-only")
	assert.True(t, resp.StopTurn)
}

func TestReadOnlyDagRunPolicy(t *testing.T) {
	wrapper := &readOnlyToolWrapper{inner: &dummyTool{}}

	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name: tools.DagRunToolName,
		Input: `{"nodes":[
			{"id":"files","tool":"rg","pattern":"*.go","files_only":true},
			{"id":"matches","tool":"rg","pattern":"ContextWindow","path":"internal"}
		]}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "dag_run run", resp.Content)
	assert.False(t, resp.StopTurn)

	resp, err = wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  tools.DagRunToolName,
		Input: `{"nodes":[{"id":"shell","tool":"shell","command":"date"}]}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "only allow rg and view")
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
