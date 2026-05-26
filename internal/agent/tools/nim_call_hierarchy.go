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

type NimCallHierarchyParams struct {
	FilePath  string `json:"file_path" description:"Absolute or relative path to the Nim source file."`
	Line      int    `json:"line" description:"1-based line number of the cursor on the symbol."`
	Character int    `json:"character" description:"1-based column of the cursor on the symbol."`
	Direction string `json:"direction,omitempty" description:"One of \"incoming\", \"outgoing\", or \"both\" (default \"both\")."`
}

const NimCallHierarchyToolName = "nim_call_hierarchy"

//go:embed nim_call_hierarchy.md
var nimCallHierarchyDescription string

func NewNimCallHierarchyTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		NimCallHierarchyToolName,
		nimCallHierarchyDescription,
		func(ctx context.Context, params NimCallHierarchyParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}
			if params.Line <= 0 {
				return fantasy.NewTextErrorResponse("line must be greater than 0"), nil
			}
			if params.Character <= 0 {
				return fantasy.NewTextErrorResponse("character must be greater than 0"), nil
			}

			direction := strings.ToLower(strings.TrimSpace(params.Direction))
			if direction == "" {
				direction = "both"
			}
			wantIncoming := direction == "incoming" || direction == "both"
			wantOutgoing := direction == "outgoing" || direction == "both"
			if !wantIncoming && !wantOutgoing {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("direction must be \"incoming\", \"outgoing\", or \"both\" (got %q)", params.Direction)), nil
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

			prepareParams := protocol.CallHierarchyPrepareParams{
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

			var items []protocol.CallHierarchyItem
			if err := client.CallCustom(ctx, "textDocument/prepareCallHierarchy", prepareParams, &items); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("LSP prepareCallHierarchy failed: %s", err)), nil
			}
			if len(items) == 0 {
				return fantasy.NewTextResponse("No callable symbol at the given position."), nil
			}

			var out strings.Builder
			for i, item := range items {
				if i > 0 {
					out.WriteString("\n")
				}
				renderCallHierarchyHeader(&out, item)

				if wantIncoming {
					var incoming []protocol.CallHierarchyIncomingCall
					inParams := protocol.CallHierarchyIncomingCallsParams{Item: item}
					if err := client.CallCustom(ctx, "callHierarchy/incomingCalls", inParams, &incoming); err != nil {
						fmt.Fprintf(&out, "  incoming: error: %s\n", err)
					} else {
						renderIncomingCalls(&out, incoming)
					}
				}

				if wantOutgoing {
					var outgoing []protocol.CallHierarchyOutgoingCall
					outParams := protocol.CallHierarchyOutgoingCallsParams{Item: item}
					if err := client.CallCustom(ctx, "callHierarchy/outgoingCalls", outParams, &outgoing); err != nil {
						fmt.Fprintf(&out, "  outgoing: error: %s\n", err)
					} else {
						renderOutgoingCalls(&out, outgoing)
					}
				}
			}

			return fantasy.NewTextResponse(out.String()), nil
		},
	)
}

func renderCallHierarchyHeader(b *strings.Builder, item protocol.CallHierarchyItem) {
	path := strings.TrimPrefix(string(item.URI), "file://")
	detail := ""
	if item.Detail != "" {
		detail = " — " + item.Detail
	}
	fmt.Fprintf(b, "%s %s @ %s:%d:%d%s\n",
		symbolKindString(item.Kind),
		item.Name,
		path,
		item.SelectionRange.Start.Line+1,
		item.SelectionRange.Start.Character+1,
		detail,
	)
}

func renderIncomingCalls(b *strings.Builder, calls []protocol.CallHierarchyIncomingCall) {
	if len(calls) == 0 {
		b.WriteString("  incoming: (none)\n")
		return
	}
	fmt.Fprintf(b, "  incoming (%d):\n", len(calls))
	for _, c := range calls {
		path := strings.TrimPrefix(string(c.From.URI), "file://")
		fmt.Fprintf(b, "    - %s %s @ %s:%d:%d\n",
			symbolKindString(c.From.Kind),
			c.From.Name,
			path,
			c.From.SelectionRange.Start.Line+1,
			c.From.SelectionRange.Start.Character+1,
		)
		for _, r := range c.FromRanges {
			fmt.Fprintf(b, "        call site: %d:%d\n", r.Start.Line+1, r.Start.Character+1)
		}
	}
}

func renderOutgoingCalls(b *strings.Builder, calls []protocol.CallHierarchyOutgoingCall) {
	if len(calls) == 0 {
		b.WriteString("  outgoing: (none)\n")
		return
	}
	fmt.Fprintf(b, "  outgoing (%d):\n", len(calls))
	for _, c := range calls {
		path := strings.TrimPrefix(string(c.To.URI), "file://")
		fmt.Fprintf(b, "    - %s %s @ %s:%d:%d\n",
			symbolKindString(c.To.Kind),
			c.To.Name,
			path,
			c.To.SelectionRange.Start.Line+1,
			c.To.SelectionRange.Start.Character+1,
		)
		for _, r := range c.FromRanges {
			fmt.Fprintf(b, "        call site: %d:%d\n", r.Start.Line+1, r.Start.Character+1)
		}
	}
}
