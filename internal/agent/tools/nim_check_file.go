package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/lsp"
)

type NimCheckFileParams struct {
	FilePath string `json:"file_path" description:"Absolute or relative path to the Nim source file to check."`
}

const NimCheckFileToolName = "nim_check_file"

//go:embed nim_check_file.md
var nimCheckFileDescription string

func NewNimCheckFileTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		NimCheckFileToolName,
		nimCheckFileDescription,
		func(ctx context.Context, params NimCheckFileParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			absPath, err := filepath.Abs(params.FilePath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to get absolute path: %s", err)), nil
			}

			// Ensure LSP is started for this file.
			lspManager.Start(ctx, absPath)

			if lspManager.Clients().Len() == 0 {
				return fantasy.NewTextErrorResponse("no LSP clients available"), nil
			}

			// Reuse the same didChange+wait pipeline that nim_diagnostics uses,
			// scoped to this single file.
			notifyLSPs(ctx, lspManager, absPath)

			fileDiags := collectFileDiagnostics(absPath, lspManager)
			if len(fileDiags) == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf("%s: clean (0 diagnostics).", absPath)), nil
			}

			sortDiagnostics(fileDiags)

			errors := countSeverity(fileDiags, "Error")
			warnings := countSeverity(fileDiags, "Warn")
			hints := countSeverity(fileDiags, "Hint")

			var out strings.Builder
			fmt.Fprintf(&out, "%s — %d error(s), %d warning(s), %d hint(s):\n", absPath, errors, warnings, hints)
			for _, d := range fileDiags {
				out.WriteString(d)
				out.WriteString("\n")
			}
			return fantasy.NewTextResponse(out.String()), nil
		},
	)
}

// collectFileDiagnostics walks every active LSP client and pulls diagnostics
// scoped to the given absolute path only.
func collectFileDiagnostics(absPath string, manager *lsp.Manager) []string {
	var out []string
	for lspName, client := range manager.Clients().Seq2() {
		for location, diags := range client.GetDiagnostics() {
			path, err := location.Path()
			if err != nil {
				slog.Error("Failed to convert diagnostic location URI to path", "uri", location, "error", err)
				continue
			}
			if path != absPath {
				continue
			}
			for _, diag := range diags {
				out = append(out, formatDiagnostic(path, diag, lspName))
			}
		}
	}
	return out
}
