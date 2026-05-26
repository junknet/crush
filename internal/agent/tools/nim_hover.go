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

type NimHoverParams struct {
	FilePath  string `json:"file_path" description:"Absolute or relative path to the Nim source file."`
	Line      int    `json:"line" description:"1-based line number of the cursor on the symbol."`
	Character int    `json:"character" description:"1-based column of the cursor on the symbol."`
}

// AgentHoverPayload mirrors the JSON contract in nimlsp AGENT_GUIDE §10.
// The nimlsp server packs this into MarkupContent.Value as plaintext.
type AgentHoverPayload struct {
	Signature  string `json:"signature"`
	Kind       string `json:"kind"`
	SrcFile    string `json:"srcFile"`
	SrcLine    int    `json:"srcLine"`
	DocOneline string `json:"docOneline"`
}

const NimHoverToolName = "nim_hover"

//go:embed nim_hover.md
var nimHoverDescription string

func NewNimHoverTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		NimHoverToolName,
		nimHoverDescription,
		func(ctx context.Context, params NimHoverParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
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

			lspParams := protocol.HoverParams{
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

			var hover protocol.Hover
			if err := client.CallCustom(ctx, "textDocument/hover", lspParams, &hover); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP hover request failed: %s", err)), nil
			}

			if hover.Contents.Value == "" {
				return fantasy.NewTextResponse("No hover info at the given position."), nil
			}

			// nimlsp ships compact JSON in plaintext MarkupContent. Try parse;
			// if it isn't our expected payload, fall back to raw value.
			var payload AgentHoverPayload
			if err := json.Unmarshal([]byte(hover.Contents.Value), &payload); err == nil && payload.Signature != "" {
				var b strings.Builder
				fmt.Fprintf(&b, "Signature: %s\n", payload.Signature)
				if payload.Kind != "" {
					fmt.Fprintf(&b, "Kind: %s\n", payload.Kind)
				}
				if payload.SrcFile != "" {
					fmt.Fprintf(&b, "Source: %s:%d\n", payload.SrcFile, payload.SrcLine)
				}
				if payload.DocOneline != "" {
					fmt.Fprintf(&b, "Doc: %s\n", payload.DocOneline)
				}
				return fantasy.NewTextResponse(b.String()), nil
			}

			return fantasy.NewTextResponse(hover.Contents.Value), nil
		},
	)
}
