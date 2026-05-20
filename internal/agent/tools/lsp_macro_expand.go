package tools

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

type LSPMacroExpandParams struct {
	FilePath  string `json:"file_path" description:"The absolute or relative path to the Nim source file."`
	Line      int    `json:"line" description:"The 1-based line number of the cursor."`
	Character int    `json:"character" description:"The 1-based character/column number of the cursor."`
	Level     *int   `json:"level,omitempty" description:"The level depth of expansion (e.g. -1 for all, default is -1)."`
}

type ExpandTextDocumentPositionParams struct {
	TextDocument protocol.TextDocumentIdentifier `json:"textDocument"`
	Position     protocol.Position               `json:"position"`
	Level        *int                            `json:"level,omitempty"`
}

type ExpandResult struct {
	Content string         `json:"content"`
	Range   protocol.Range `json:"range"`
}

const LSPMacroExpandToolName = "lsp_macro_expand"

//go:embed lsp_macro_expand.md
var lspMacroExpandDescription string

func NewLSPMacroExpandTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		LSPMacroExpandToolName,
		lspMacroExpandDescription,
		func(ctx context.Context, params LSPMacroExpandParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
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

			var client *lsp.Client
			for c := range lspManager.Clients().Seq() {
				if c.HandlesFile(absPath) {
					client = c
					break
				}
			}

			if client == nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("no LSP client is handling file: %s", absPath)), nil
			}

			if err := client.OpenFileOnDemand(ctx, absPath); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to open file on LSP: %s", err)), nil
			}

			uri := string(protocol.URIFromPath(absPath))
			lspParams := ExpandTextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{
					URI: protocol.DocumentURI(uri),
				},
				Position: protocol.Position{
					Line:      uint32(params.Line - 1),
					Character: uint32(params.Character - 1),
				},
				Level: params.Level,
			}

			var result ExpandResult
			err = client.CallCustom(ctx, "extension/macroExpand", lspParams, &result)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP custom macro expansion failed: %s", err)), nil
			}

			return fantasy.NewTextResponse(result.Content), nil
		},
	)
}
