package tools

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

type LSPSafeToDeleteParams struct {
	FilePath  string `json:"file_path" description:"The absolute or relative path to the Nim source file."`
	Line      int    `json:"line" description:"The 1-based line number of the cursor."`
	Character int    `json:"character" description:"The 1-based character/column number of the cursor."`
}

type SafeToDeleteParams struct {
	TextDocument protocol.TextDocumentIdentifier `json:"textDocument"`
	Position     protocol.Position               `json:"position"`
}

type SafeToDeleteResult struct {
	Safe             bool                `json:"safe"`
	Refs             []protocol.Location `json:"refs"`
	BlastRadiusFiles int                 `json:"blastRadiusFiles"`
	Reasons          []string            `json:"reasons"`
}

const LSPSafeToDeleteToolName = "lsp_safe_to_delete"

//go:embed lsp_safe_to_delete.md
var lspSafeToDeleteDescription string

func NewLSPSafeToDeleteTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		LSPSafeToDeleteToolName,
		lspSafeToDeleteDescription,
		func(ctx context.Context, params LSPSafeToDeleteParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
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
			lspParams := SafeToDeleteParams{
				TextDocument: protocol.TextDocumentIdentifier{
					URI: protocol.DocumentURI(uri),
				},
				Position: protocol.Position{
					Line:      uint32(params.Line - 1),
					Character: uint32(params.Character - 1),
				},
			}

			var result SafeToDeleteResult
			err = client.CallCustom(ctx, "nimlsp/safeToDelete", lspParams, &result)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP custom safe-to-delete check failed: %s", err)), nil
			}

			var b strings.Builder
			if result.Safe {
				fmt.Fprintf(&b, "Safe to delete: Yes\n")
			} else {
				fmt.Fprintf(&b, "Safe to delete: No\n")
			}
			fmt.Fprintf(&b, "Blast Radius Files: %d\n", result.BlastRadiusFiles)

			if len(result.Reasons) > 0 {
				fmt.Fprintln(&b, "Reasons:")
				for _, reason := range result.Reasons {
					fmt.Fprintf(&b, "  - %s\n", reason)
				}
			}

			if len(result.Refs) > 0 {
				fmt.Fprintln(&b, "References:")
				for _, ref := range result.Refs {
					path := string(ref.URI)
					if strings.HasPrefix(path, "file://") {
						path = strings.TrimPrefix(path, "file://")
					}
					fmt.Fprintf(&b, "  - %s:%d:%d\n", path, ref.Range.Start.Line+1, ref.Range.Start.Character+1)
				}
			}

			return fantasy.NewTextResponse(b.String()), nil
		},
	)
}
