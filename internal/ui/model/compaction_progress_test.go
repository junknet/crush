package model

import (
	"testing"

	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/ui/chat"
)

func TestCompactionProgressPercent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		trace agentruntime.TaskTrace
		want  int
	}{
		{
			name:  "no budget suppresses bar",
			trace: agentruntime.TaskTrace{MaxOutputTokens: 0, OutputBytes: 4000},
			want:  -1,
		},
		{
			name:  "started with no output shows empty bar",
			trace: agentruntime.TaskTrace{MaxOutputTokens: 8000, OutputBytes: 0},
			want:  0,
		},
		{
			name:  "tiny output clamps to minimum visible",
			trace: agentruntime.TaskTrace{MaxOutputTokens: 8000, OutputBytes: 4},
			want:  1,
		},
		{
			// 16000 bytes / 4 = 4000 est tokens; 4000/8000 = 50%.
			name:  "midstream estimate from bytes over budget",
			trace: agentruntime.TaskTrace{MaxOutputTokens: 8000, OutputBytes: 16000},
			want:  50,
		},
		{
			// Would exceed 100%; running value caps at 95.
			name:  "near-full output caps below 100",
			trace: agentruntime.TaskTrace{MaxOutputTokens: 4096, OutputBytes: 200000},
			want:  95,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := compactionProgressPercent(tt.trace)
			if got != tt.want {
				t.Fatalf("compactionProgressPercent(%+v) = %d, want %d", tt.trace, got, tt.want)
			}
		})
	}
}

func TestCompactionActivitySnapshotProgress(t *testing.T) {
	t.Parallel()

	running := compactionActivitySnapshot("sess-1", agentruntime.TaskTrace{
		Kind:            agentruntime.TraceKindConversationCompactionProgress,
		MaxOutputTokens: 8000,
		OutputBytes:     16000,
	})
	if running.Status != chat.RuntimeActivityRunning {
		t.Fatalf("running status = %q, want running", running.Status)
	}
	if running.ProgressPercent != 50 {
		t.Fatalf("running progress = %d, want 50", running.ProgressPercent)
	}

	finished := compactionActivitySnapshot("sess-1", agentruntime.TaskTrace{
		Kind:            agentruntime.TraceKindConversationCompactionFinished,
		MaxOutputTokens: 8000,
		OutputBytes:     16000,
	})
	if finished.Status != chat.RuntimeActivityDone {
		t.Fatalf("finished status = %q, want done", finished.Status)
	}
	if finished.ProgressPercent != 100 {
		t.Fatalf("finished progress = %d, want 100", finished.ProgressPercent)
	}

	failed := compactionActivitySnapshot("sess-1", agentruntime.TaskTrace{
		Kind:            agentruntime.TraceKindConversationCompactionFailed,
		MaxOutputTokens: 8000,
		OutputBytes:     16000,
		Error:           "boom",
	})
	if failed.Status != chat.RuntimeActivityFailed {
		t.Fatalf("failed status = %q, want failed", failed.Status)
	}
	if failed.ProgressPercent != -1 {
		t.Fatalf("failed progress = %d, want -1 (suppressed)", failed.ProgressPercent)
	}
}
