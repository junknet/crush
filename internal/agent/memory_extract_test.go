package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
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

	// 1. Run safe tool (e.g. glob)
	resp, err := wrapper.Run(context.Background(), fantasy.ToolCall{
		Name:  "glob",
		Input: "{}",
	})
	require.NoError(t, err)
	assert.Equal(t, "glob run", resp.Content)
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
