package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

type NimDocumentSymbolsParams struct {
	FilePath string `json:"file_path" description:"Absolute or relative path to the Nim source file."`
}

const NimDocumentSymbolsToolName = "nim_document_symbols"

//go:embed nim_document_symbols.md
var nimDocumentSymbolsDescription string

func NewNimDocumentSymbolsTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		NimDocumentSymbolsToolName,
		nimDocumentSymbolsDescription,
		func(ctx context.Context, params NimDocumentSymbolsParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			absPath, err := filepath.Abs(params.FilePath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to get absolute path: %s", err)), nil
			}

			client, err := pickClientForFile(lspManager, absPath)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			if err := client.OpenFileOnDemand(ctx, absPath); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to open file on LSP: %s", err)), nil
			}

			lspParams := protocol.DocumentSymbolParams{
				TextDocument: protocol.TextDocumentIdentifier{
					URI: protocol.DocumentURI(protocol.URIFromPath(absPath)),
				},
			}

			// Response is DocumentSymbol[] (hierarchical) or SymbolInformation[]
			// (flat, deprecated). Try hierarchical first.
			var raw json.RawMessage
			if err := client.CallCustom(ctx, "textDocument/documentSymbol", lspParams, &raw); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP documentSymbol request failed: %s", err)), nil
			}
			if len(raw) == 0 || string(raw) == "null" {
				return fantasy.NewTextResponse("No symbols found in file."), nil
			}

			var b strings.Builder
			var hier []protocol.DocumentSymbol
			if err := json.Unmarshal(raw, &hier); err == nil && len(hier) > 0 {
				fmt.Fprintf(&b, "Symbols in %s:\n", absPath)
				for _, sym := range hier {
					renderDocumentSymbol(&b, sym, 0)
				}
				return fantasy.NewTextResponse(b.String()), nil
			}

			var flat []protocol.SymbolInformation
			if err := json.Unmarshal(raw, &flat); err == nil && len(flat) > 0 {
				fmt.Fprintf(&b, "Symbols in %s:\n", absPath)
				for _, sym := range flat {
					fmt.Fprintf(&b, "  %s %s [%d:%d]\n",
						symbolKindString(sym.Kind), sym.Name,
						sym.Location.Range.Start.Line+1, sym.Location.Range.Start.Character+1)
				}
				return fantasy.NewTextResponse(b.String()), nil
			}

			return fantasy.NewTextResponse("No symbols found in file."), nil
		},
	)
}

func renderDocumentSymbol(b *strings.Builder, sym protocol.DocumentSymbol, depth int) {
	indent := strings.Repeat("  ", depth+1)
	detail := ""
	if sym.Detail != "" {
		detail = " — " + sym.Detail
	}
	fmt.Fprintf(b, "%s%s %s [%d:%d]%s\n",
		indent,
		symbolKindString(sym.Kind),
		sym.Name,
		sym.SelectionRange.Start.Line+1,
		sym.SelectionRange.Start.Character+1,
		detail,
	)
	for _, child := range sym.Children {
		renderDocumentSymbol(b, child, depth+1)
	}
}

// symbolKindString maps LSP SymbolKind enums to short readable names.
// LSP spec values: https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#symbolKind
func symbolKindString(k protocol.SymbolKind) string {
	switch k {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "func"
	case 13:
		return "var"
	case 14:
		return "const"
	case 15:
		return "string"
	case 16:
		return "number"
	case 17:
		return "bool"
	case 18:
		return "array"
	case 19:
		return "object"
	case 20:
		return "key"
	case 21:
		return "null"
	case 22:
		return "enum_member"
	case 23:
		return "struct"
	case 24:
		return "event"
	case 25:
		return "operator"
	case 26:
		return "type_param"
	default:
		return fmt.Sprintf("kind%d", k)
	}
}
