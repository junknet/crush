package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

type mockModel struct {
	provider   string
	modelName  string
	streamFunc func(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error)
}

var _ fantasy.LanguageModel = (*mockModel)(nil)

func (m *mockModel) Provider() string { return m.provider }
func (m *mockModel) Model() string    { return m.modelName }

func (m *mockModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if m.streamFunc != nil {
		return m.streamFunc(ctx, call)
	}
	return nil, errors.New("not implemented")
}

func (m *mockModel) GenerateObject(ctx context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) StreamObject(ctx context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func singleDeltaStream(text string) fantasy.StreamResponse {
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{
			Type:  fantasy.StreamPartTypeTextDelta,
			Delta: text,
		})
		yield(fantasy.StreamPart{
			Type:         fantasy.StreamPartTypeFinish,
			FinishReason: fantasy.FinishReasonStop,
		})
	}
}

func TestModelFailoverAndRetry(t *testing.T) {
	env := testEnv(t)

	var primaryCalls int
	primaryModel := Model{
		Model: &mockModel{
			provider:  "primary-provider",
			modelName: "primary-model",
			streamFunc: func(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
				primaryCalls++
				return nil, &fantasy.ProviderError{
					StatusCode: 429,
					Message:    "primary model rate limited or timed out",
				}
			},
		},
		CatwalkCfg: catwalk.Model{
			Name:             "primary-model",
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}

	fallbackModel := Model{
		Model: &mockModel{
			provider:  "fallback-provider",
			modelName: "fallback-model",
			streamFunc: func(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
				return singleDeltaStream("Hello from fallback model"), nil
			},
		},
		CatwalkCfg: catwalk.Model{
			Name:             "fallback-model",
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}

	agent := NewSessionAgent(SessionAgentOptions{
		PrimaryModel:   primaryModel,
		FallbackModels: []Model{fallbackModel},
		TitleModel:     fallbackModel,
		Sessions:       env.sessions,
		Messages:       env.messages,
		WorkingDir:     env.workingDir,
		RetryBackoff: func(attempt int) time.Duration {
			return 0
		},
	})

	sess, err := env.sessions.Create(t.Context(), "Test Session", session.ModeExecute)
	require.NoError(t, err)

	res, err := agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Hello",
		SessionID:       sess.ID,
		MaxOutputTokens: 1000,
	})

	// The agent run should succeed by falling back
	require.NoError(t, err)
	require.NotNil(t, res)

	// Verify primary model was retried 3 times (1 first call + 2 retries)
	assert.Equal(t, 3, primaryCalls)

	// Verify messages in DB
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)

	// There should be user prompt and assistant response
	require.Len(t, msgs, 2)
	assert.Equal(t, message.User, msgs[0].Role)
	assert.Equal(t, message.Assistant, msgs[1].Role)
	assert.Equal(t, "Hello from fallback model", msgs[1].Content().Text)
}
