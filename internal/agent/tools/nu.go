package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
)

type NuParams struct {
	Command string `json:"command" description:"The Nushell command to execute"`
}

const NuToolName = "nu"

//go:embed nu.md.tpl
var nuDescription string

func NewNuTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		NuToolName,
		nuDescription,
		func(ctx context.Context, params NuParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("missing command"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing nu command")
			}

			p, err := permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        workingDir,
					ToolCallID:  call.ID,
					ToolName:    NuToolName,
					Action:      "execute",
					Description: fmt.Sprintf("Execute nu command: %s", params.Command),
					Params:      params,
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p {
				return NewPermissionDeniedResponse(), nil
			}

			cmd := exec.CommandContext(ctx, "nu", "-c", params.Command)
			cmd.Dir = workingDir
			output, err := cmd.CombinedOutput()

			outStr := string(output)
			if err != nil {
				return fantasy.NewTextResponse(fmt.Sprintf("Error: %v\nOutput: %s", err, outStr)), nil
			}

			// Try to parse as JSON to satisfy the "JSON-parsed object if possible" requirement.
			// If it's valid JSON, we return it as is, and the model will interpret it.
			var jsonData any
			if err := json.Unmarshal(output, &jsonData); err == nil {
				// Re-marshal to ensure it's clean JSON string if needed,
				// but since fantasy.NewTextResponse takes a string, we just use outStr.
				// However, the prompt says "as a JSON-parsed object if possible".
				// This is ambiguous given fantasy.ToolResponse.Content is a string.
				// We'll return the string as-is if it's JSON.
				return fantasy.NewTextResponse(strings.TrimSpace(outStr)), nil
			}

			return fantasy.NewTextResponse(outStr), nil
		},
	)
}
