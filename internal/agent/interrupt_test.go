package agent

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
)

// helpers ---------------------------------------------------------------------

func userMsg(text string) message.Message {
	return message.Message{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
}

func assistantMsg(reason message.FinishReason) message.Message {
	return message.Message{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.Finish{Reason: reason}},
	}
}

func toolMsg() message.Message {
	return message.Message{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "x", Name: "bash", Content: "ok"},
		},
	}
}

// tests -----------------------------------------------------------------------

func TestWasPreviousTurnCancelled(t *testing.T) {
	tests := []struct {
		name string
		msgs []message.Message
		want bool
	}{
		{
			name: "empty history",
			msgs: nil,
			want: false,
		},
		{
			name: "only user messages",
			msgs: []message.Message{
				userMsg("hello"),
			},
			want: false,
		},
		{
			name: "last assistant finished cleanly",
			msgs: []message.Message{
				userMsg("hi"),
				assistantMsg(message.FinishReasonEndTurn),
			},
			want: false,
		},
		{
			name: "last assistant cancelled",
			msgs: []message.Message{
				userMsg("hi"),
				assistantMsg(message.FinishReasonCanceled),
			},
			want: true,
		},
		{
			name: "cancelled assistant followed by tool message — still cancelled",
			msgs: []message.Message{
				userMsg("hi"),
				assistantMsg(message.FinishReasonCanceled),
				toolMsg(),
			},
			want: true,
		},
		{
			name: "older cancel superseded by later clean turn",
			msgs: []message.Message{
				userMsg("first"),
				assistantMsg(message.FinishReasonCanceled),
				userMsg("retry"),
				assistantMsg(message.FinishReasonEndTurn),
			},
			want: false,
		},
		{
			name: "no assistant message at all",
			msgs: []message.Message{
				userMsg("hi"),
				toolMsg(),
			},
			want: false,
		},
		{
			name: "assistant error finish is not cancelled",
			msgs: []message.Message{
				userMsg("hi"),
				assistantMsg(message.FinishReasonError),
			},
			want: false,
		},
		{
			name: "max_tokens finish is not cancelled",
			msgs: []message.Message{
				userMsg("hi"),
				assistantMsg(message.FinishReasonMaxTokens),
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wasPreviousTurnCancelled(tc.msgs)
			if got != tc.want {
				t.Errorf("wasPreviousTurnCancelled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInterruptMarker_Shape(t *testing.T) {
	// Guard against accidentally renaming/rewording the marker — if it
	// changes, brain.md.tpl's <interruption_handling> rule 5 has to be
	// updated in lockstep. Test exists to flag the divergence early.
	if !strings.Contains(interruptMarker, "Previous turn was interrupted by user") {
		t.Fatalf("interrupt marker shape changed; update brain.md.tpl: %q", interruptMarker)
	}
	if !strings.HasPrefix(interruptMarker, "[") || !strings.HasSuffix(interruptMarker, "]") {
		t.Errorf("interrupt marker must be wrapped in square brackets: %q", interruptMarker)
	}
}
