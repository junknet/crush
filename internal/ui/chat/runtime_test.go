package chat

import (
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestRuntimeActivityItemRendersCompactionMetadata(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := NewRuntimeActivityItem(&sty, RuntimeActivitySnapshot{
		ID:              "runtime:compaction:session-a",
		Kind:            RuntimeActivityConversationCompaction,
		Status:          RuntimeActivityRunning,
		Title:           "Compacting conversation",
		StartedAt:       time.Now().Add(-3*time.Minute - 6*time.Second),
		Tokens:          5_900,
		TokensAreExact:  true,
		ProgressPercent: -1,
	})

	rendered := ansi.Strip(item.RawRender(100))

	require.Contains(t, rendered, "Compacting conversation")
	require.Contains(t, rendered, "3m 6s")
	require.Contains(t, rendered, "5.9K tokens")
	require.False(t, item.Finished())
}
