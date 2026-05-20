package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
)

const NimRestartToolName = "nim_restart"

//go:embed nim_restart.md
var nimRestartDescription string

type NimRestartParams struct {
	// Name is the optional name of a specific LSP client to restart.
	// If empty, all LSP clients will be restarted.
	Name string `json:"name,omitempty"`
}

func NewNimRestartTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		NimRestartToolName,
		nimRestartDescription,
		func(ctx context.Context, params NimRestartParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if lspManager.Clients().Len() == 0 {
				return fantasy.NewTextErrorResponse("no LSP clients available to restart"), nil
			}

			clientsToRestart := make(map[string]*lsp.Client)
			if params.Name == "" {
				maps.Insert(clientsToRestart, lspManager.Clients().Seq2())
			} else {
				client, exists := lspManager.Clients().Get(params.Name)
				if !exists {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP client '%s' not found", params.Name)), nil
				}
				clientsToRestart[params.Name] = client
			}

			var restarted []string
			var failed []string
			var mu sync.Mutex
			var wg sync.WaitGroup
			for name, client := range clientsToRestart {
				wg.Go(func() {
					if err := client.Restart(); err != nil {
						slog.Error("Failed to restart LSP client", "name", name, "error", err)
						mu.Lock()
						failed = append(failed, name)
						mu.Unlock()
						return
					}
					mu.Lock()
					restarted = append(restarted, name)
					mu.Unlock()
				})
			}

			wg.Wait()

			var output string
			if len(restarted) > 0 {
				output = fmt.Sprintf("Successfully restarted %d LSP client(s): %s\n", len(restarted), strings.Join(restarted, ", "))
			}
			if len(failed) > 0 {
				output += fmt.Sprintf("Failed to restart %d LSP client(s): %s\n", len(failed), strings.Join(failed, ", "))
				return fantasy.NewTextErrorResponse(output), nil
			}

			return fantasy.NewTextResponse(output), nil
		},
	)
}
