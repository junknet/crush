package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

type NimWorkspaceSymbolsParams struct {
	Query string `json:"query" description:"Symbol-name fragment to search for. Relaxed match: case-insensitive, characters in order."`
}

const NimWorkspaceSymbolsToolName = "nim_workspace_symbols"

//go:embed nim_workspace_symbols.md
var nimWorkspaceSymbolsDescription string

func NewNimWorkspaceSymbolsTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		NimWorkspaceSymbolsToolName,
		nimWorkspaceSymbolsDescription,
		func(ctx context.Context, params NimWorkspaceSymbolsParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if lspManager.Clients().Len() == 0 {
				return fantasy.NewTextErrorResponse("no LSP clients available"), nil
			}

			lspParams := protocol.WorkspaceSymbolParams{
				Query: params.Query,
			}

			// Query every active client and merge results. LSP spec allows the
			// server to return SymbolInformation[] or WorkspaceSymbol[].
			var b strings.Builder
			total := 0
			for name, client := range lspManager.Clients().Seq2() {
				var raw json.RawMessage
				if err := client.CallCustom(ctx, "workspace/symbol", lspParams, &raw); err != nil {
					fmt.Fprintf(&b, "[%s] error: %s\n", name, err)
					continue
				}
				if len(raw) == 0 || string(raw) == "null" {
					continue
				}

				// Try SymbolInformation[] first (most common, has full Location).
				var infos []protocol.SymbolInformation
				if err := json.Unmarshal(raw, &infos); err == nil && len(infos) > 0 {
					for _, sym := range infos {
						path := strings.TrimPrefix(string(sym.Location.URI), "file://")
						container := ""
						if sym.ContainerName != "" {
							container = " (" + sym.ContainerName + ")"
						}
						fmt.Fprintf(&b, "  %s %s @ %s:%d:%d%s\n",
							symbolKindString(sym.Kind), sym.Name, path,
							sym.Location.Range.Start.Line+1, sym.Location.Range.Start.Character+1,
							container)
						total++
					}
					continue
				}

				// Fall back to WorkspaceSymbol[] (LSP 3.17 partial form).
				var wsyms []protocol.WorkspaceSymbol
				if err := json.Unmarshal(raw, &wsyms); err == nil && len(wsyms) > 0 {
					for _, sym := range wsyms {
						uri, line, col := extractWorkspaceSymbolLocation(sym)
						path := strings.TrimPrefix(uri, "file://")
						container := ""
						if sym.ContainerName != "" {
							container = " (" + sym.ContainerName + ")"
						}
						fmt.Fprintf(&b, "  %s %s @ %s:%d:%d%s\n",
							symbolKindString(sym.Kind), sym.Name, path, line+1, col+1, container)
						total++
					}
				}
			}

			if total == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf("No symbols matched query %q.", params.Query)), nil
			}

			var out strings.Builder
			fmt.Fprintf(&out, "Found %d symbol(s) matching %q:\n", total, params.Query)
			out.WriteString(b.String())
			return fantasy.NewTextResponse(out.String()), nil
		},
	)
}

// extractWorkspaceSymbolLocation pulls URI + start position out of a
// WorkspaceSymbol. The Location field is a union (Location | { uri: URI })
// in the LSP 3.17 spec, so we round-trip through JSON to read whichever
// shape the server actually sent.
func extractWorkspaceSymbolLocation(sym protocol.WorkspaceSymbol) (uri string, line uint32, character uint32) {
	raw, err := json.Marshal(sym.Location)
	if err != nil {
		return "", 0, 0
	}
	var full protocol.Location
	if err := json.Unmarshal(raw, &full); err == nil && full.URI != "" {
		return string(full.URI), full.Range.Start.Line, full.Range.Start.Character
	}
	var partial struct {
		URI protocol.DocumentURI `json:"uri"`
	}
	if err := json.Unmarshal(raw, &partial); err == nil {
		return string(partial.URI), 0, 0
	}
	return "", 0, 0
}
