package tools

import (
	"context"
	_ "embed"
	"fmt"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
)

type LSPProjectMapsParams struct{}

const LSPProjectMapsToolName = "lsp_project_maps"

//go:embed lsp_project_maps.md
var lspProjectMapsDescription string

func NewLSPProjectMapsTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		LSPProjectMapsToolName,
		lspProjectMapsDescription,
		func(ctx context.Context, params LSPProjectMapsParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			var client *lsp.Client
			for c := range lspManager.Clients().Seq() {
				client = c
				break
			}

			if client == nil {
				return fantasy.NewTextErrorResponse("no running LSP clients available to retrieve project maps"), nil
			}

			var result string
			err := client.CallCustom(ctx, "nimlsp/projectMaps", nil, &result)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP custom projectMaps failed: %s", err)), nil
			}

			return fantasy.NewTextResponse(result), nil
		},
	)
}
