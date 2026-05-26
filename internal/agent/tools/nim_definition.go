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

type NimDefinitionParams struct {
	FilePath  string `json:"file_path" description:"Absolute or relative path to the Nim source file."`
	Line      int    `json:"line" description:"1-based line number of the cursor on the symbol."`
	Character int    `json:"character" description:"1-based column of the cursor on the symbol."`
}

const NimDefinitionToolName = "nim_definition"

//go:embed nim_definition.md
var nimDefinitionDescription string

func NewNimDefinitionTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		NimDefinitionToolName,
		nimDefinitionDescription,
		func(ctx context.Context, params NimDefinitionParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}
			if params.Line <= 0 {
				return fantasy.NewTextErrorResponse("line must be greater than 0"), nil
			}
			if params.Character <= 0 {
				return fantasy.NewTextErrorResponse("character must be greater than 0"), nil
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

			lspParams := protocol.DefinitionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{
						URI: protocol.DocumentURI(protocol.URIFromPath(absPath)),
					},
					Position: protocol.Position{
						Line:      uint32(params.Line - 1),
						Character: uint32(params.Character - 1),
					},
				},
			}

			// LSP spec says response may be Location | Location[] | LocationLink[].
			// Decode as raw and try each shape.
			var raw json.RawMessage
			if err := client.CallCustom(ctx, "textDocument/definition", lspParams, &raw); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP definition request failed: %s", err)), nil
			}

			locations := decodeDefinitionResponse(raw)
			if len(locations) == 0 {
				return fantasy.NewTextResponse("No definition found at the given position."), nil
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Found %d definition(s):\n", len(locations))
			for _, loc := range locations {
				path := strings.TrimPrefix(string(loc.URI), "file://")
				fmt.Fprintf(&b, "  - %s:%d:%d\n", path, loc.Range.Start.Line+1, loc.Range.Start.Character+1)
			}
			return fantasy.NewTextResponse(b.String()), nil
		},
	)
}

// decodeDefinitionResponse handles the three shapes LSP can return.
func decodeDefinitionResponse(raw json.RawMessage) []protocol.Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// Try single Location
	var single protocol.Location
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []protocol.Location{single}
	}
	// Try []Location
	var arr []protocol.Location
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 && arr[0].URI != "" {
		return arr
	}
	// Try []LocationLink
	var links []protocol.LocationLink
	if err := json.Unmarshal(raw, &links); err == nil {
		out := make([]protocol.Location, 0, len(links))
		for _, l := range links {
			out = append(out, protocol.Location{URI: l.TargetURI, Range: l.TargetRange})
		}
		return out
	}
	return nil
}

// pickClientForFile finds the first LSP client that claims the given file.
// Returns a human-friendly error if no client is available.
func pickClientForFile(manager *lsp.Manager, absPath string) (*lsp.Client, error) {
	for c := range manager.Clients().Seq() {
		if c.HandlesFile(absPath) {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no LSP client is handling file: %s", absPath)
}
