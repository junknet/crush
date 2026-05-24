package suggestion

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
)

type fakeLister struct {
	msgs []message.Message
	err  error
}

func (f *fakeLister) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	return f.msgs, f.err
}

func userMsg(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func assistantMsg(text string) message.Message {
	return message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func TestSanitize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"run the tests", "run the tests"},
		{"  run the tests  ", "run the tests"},
		{"`now refactor it to use channels`", "now refactor it to use channels"},
		{`"show me the diff"`, "show me the diff"},
		{"Suggestion: run the tests", "run the tests"},
		{"thanks", ""},                    // filler
		{"looks good", ""},                // filler
		{"thanks!", ""},                   // filler with trailing punct
		{"ok", ""},                        // single word + filler
		{"one", ""},                       // below minWords
		{strings.Repeat("word ", 15), ""}, // above maxWords
		{"first line\nsecond line that would make this longer", "first line"},                                // first line only after newline strip
		{"refactor agent.go to extract the dispatch loop", "refactor agent.go to extract the dispatch loop"}, // legit instruction
		{"", ""}, // empty
		{"-> show me the diff", "show me the diff"}, // arrow prefix
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := sanitize(tc.in)
			if got != tc.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHistoryReady(t *testing.T) {
	s := &Service{}
	if s.historyReady(nil) {
		t.Fatalf("empty history should not be ready")
	}
	if s.historyReady([]message.Message{userMsg("hi")}) {
		t.Fatalf("user-only history should not be ready")
	}
	if s.historyReady([]message.Message{assistantMsg("hi")}) {
		t.Fatalf("assistant-only history should not be ready")
	}
	if !s.historyReady([]message.Message{userMsg("hi"), assistantMsg("hi back")}) {
		t.Fatalf("user+assistant history should be ready")
	}
}

func TestBuildPromptOrdersOldestFirst(t *testing.T) {
	msgs := []message.Message{
		userMsg("first user"),
		assistantMsg("first reply"),
		userMsg("second user"),
		assistantMsg("second reply"),
	}
	got := buildPrompt(msgs)
	if got == "" {
		t.Fatalf("expected non-empty prompt")
	}
	idxFirstUser := strings.Index(got, "first user")
	idxSecondReply := strings.Index(got, "second reply")
	if idxFirstUser < 0 || idxSecondReply < 0 {
		t.Fatalf("prompt missing messages: %q", got)
	}
	if idxFirstUser > idxSecondReply {
		t.Fatalf("prompt should be oldest-first; got %q", got)
	}
	if !strings.Contains(got, "Predict the user's next message") {
		t.Fatalf("prompt missing tail instruction; got %q", got)
	}
}

func TestGenerateSkipsWhenDisabled(t *testing.T) {
	svc := New(&fakeLister{}, nil, true)
	defer svc.Shutdown()
	out, err := svc.Generate(context.Background(), "s1")
	if err != nil || out != "" {
		t.Fatalf("disabled: want (\"\", nil); got (%q, %v)", out, err)
	}
}

func TestGenerateSkipsThinHistory(t *testing.T) {
	// User-only history (no assistant turn yet) — historyReady gates this
	// so the model is never invoked, hence a nil ModelProvider is safe.
	svc := New(&fakeLister{msgs: []message.Message{userMsg("hi")}}, nil, false)
	defer svc.Shutdown()
	out, err := svc.Generate(context.Background(), "s1")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty suggestion for thin history, got %q", out)
	}
}

func TestClearAndLatest(t *testing.T) {
	svc := New(nil, nil, true) // disabled is fine — we only test Clear/Latest plumbing
	defer svc.Shutdown()
	if v, ok := svc.Latest("s1"); ok || v != "" {
		t.Fatalf("expected no latest initially")
	}
	svc.latest.Set("s1", "run tests")
	v, ok := svc.Latest("s1")
	if !ok || v != "run tests" {
		t.Fatalf("Latest returned (%q,%v)", v, ok)
	}
	svc.Clear("s1")
	if _, ok := svc.Latest("s1"); ok {
		t.Fatalf("expected Clear to drop latest")
	}
}

func TestMarkAcceptedClears(t *testing.T) {
	svc := New(nil, nil, true)
	defer svc.Shutdown()
	svc.latest.Set("s1", "run tests")
	svc.MarkAccepted("s1", "tab", 9)
	if _, ok := svc.Latest("s1"); ok {
		t.Fatalf("MarkAccepted should clear latest")
	}
}
