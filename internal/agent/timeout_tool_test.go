package agent

import (
	"context"
	"testing"
	"time"

	"charm.land/fantasy"
)

type sleepTool struct {
	name  string
	sleep time.Duration
	err   error
}

func (s *sleepTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: s.name}
}

func (s *sleepTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (s *sleepTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

func (s *sleepTool) Run(ctx context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if s.sleep > 0 {
		select {
		case <-time.After(s.sleep):
		case <-ctx.Done():
			return fantasy.ToolResponse{}, ctx.Err()
		}
	}
	if s.err != nil {
		return fantasy.ToolResponse{}, s.err
	}
	return fantasy.NewTextResponse("success"), nil
}

func TestTimeoutTool(t *testing.T) {
	t.Run("completes within timeout", func(t *testing.T) {
		inner := &sleepTool{name: "fast", sleep: 10 * time.Millisecond}
		wrapped := newTimeoutTool(inner, 100*time.Millisecond)

		resp, err := wrapped.Run(context.Background(), fantasy.ToolCall{Name: "fast"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "success" {
			t.Errorf("expected success, got %q", resp.Content)
		}
		if resp.IsError {
			t.Errorf("expected no error, got error response")
		}
	})

	t.Run("times out", func(t *testing.T) {
		inner := &sleepTool{name: "slow", sleep: 200 * time.Millisecond}
		wrapped := newTimeoutTool(inner, 50*time.Millisecond)

		resp, err := wrapped.Run(context.Background(), fantasy.ToolCall{Name: "slow"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.IsError {
			t.Errorf("expected error response on timeout")
		}
		expectedMsg := "slow execution timed out after 50ms"
		if !contains(resp.Content, expectedMsg) {
			t.Errorf("expected msg to contain %q, got %q", expectedMsg, resp.Content)
		}
	})

	t.Run("parent context canceled", func(t *testing.T) {
		inner := &sleepTool{name: "slow", sleep: 200 * time.Millisecond}
		wrapped := newTimeoutTool(inner, 100*time.Millisecond)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := wrapped.Run(ctx, fantasy.ToolCall{Name: "slow"})
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})
}

// Agent is an event-driven agent loop, not a quick tool: it must NOT be wrapped
// with a wall-clock timeout. It runs to natural completion (foreground) or is
// driven via its handle — agent_job_id + Monitor/JobOutput/JobKill (background).
// A hard 2-minute kill was the wrong model. Genuine quick tools stay wrapped.
func TestWrapToolsWithTimeoutLeavesAgentUnwrapped(t *testing.T) {
	t.Parallel()

	wrapped := wrapToolsWithTimeout([]fantasy.AgentTool{
		&sleepTool{name: AgentToolName},
		&sleepTool{name: "Edit"},
	}, 50*time.Millisecond)

	if _, isTimeout := wrapped[0].(*timeoutTool); isTimeout {
		t.Fatalf("Agent must NOT be timeout-wrapped — it is an event-driven agent loop, not a quick tool")
	}
	if _, isTimeout := wrapped[1].(*timeoutTool); !isTimeout {
		t.Fatalf("expected quick tool Edit to be timeout-wrapped, got %T", wrapped[1])
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
