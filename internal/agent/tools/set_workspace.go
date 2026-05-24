// Tool: set_workspace — switches the active session's workspace to a
// local directory or a remote SSH host. Subsequent file/edit/view/grep
// tool calls in this session transparently route to the new target.
//
// Examples the LLM is expected to construct:
//
//	{"uri": "local"}                                  -- back to default
//	{"uri": "local:/home/user/proj"}                  -- local subdir
//	{"uri": "ssh://root@10.0.0.5/srv/app"}            -- remote, agent auth
//	{"uri": "ssh://me@host:2222/srv/app?identity_file=~/.ssh/work"}
//
// On success the tool validates the URI, opens the driver (which for
// SSH actually dials the host), runs a quick sanity ping, and reports
// the resolved working directory plus connection metadata.
package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/iodriver"
)

// SetWorkspaceToolName is the tool's registered name.
const SetWorkspaceToolName = "set_workspace"

// SetWorkspaceParams matches the JSON schema below.
type SetWorkspaceParams struct {
	// URI is the workspace target. See package doc for accepted forms.
	URI string `json:"uri"`
	// Validate, when true, runs a sanity ping (cd + pwd) before
	// returning so the user gets immediate feedback that auth /
	// network / cwd actually work.
	Validate bool `json:"validate,omitempty"`
}

// setWorkspaceDescription is the LLM-facing tool description.
const setWorkspaceDescription = `Switch the active session's workspace between the local machine and a remote SSH host. Subsequent file/edit/view/grep/bash tools transparently operate on the chosen target — local middle, remote tool execution.

Parameters
- uri (string, required):
    "local"                                        — default local working directory
    "local:/abs/path"                              — pin a different local directory
    "ssh://user@host[:port]/path[?key=value]"      — remote host via SSH
      Supported query params:
        identity_file=~/.ssh/key   PEM private key (no passphrase)
- validate (bool, optional, default false): dial + pwd on the target before returning so failures surface immediately.

Authentication order for SSH: explicit identity_file → SSH_AUTH_SOCK → ~/.ssh/id_{ed25519,rsa,ecdsa}. Host-key verification is currently permissive (TOFU); treat unfamiliar hosts with caution.`

// NewSetWorkspaceTool builds the tool. The registry is the in-memory
// per-session map the coordinator reads when injecting a driver into
// each task's ctx. The factory is reused so two sessions pointing at
// the same SSH host share one connection.
func NewSetWorkspaceTool(registry *iodriver.URIRegistry, factory *iodriver.Factory) fantasy.AgentTool {
	// Hand the embedded rg bytes to iodriver so SSH bootstrap can push
	// it to remote hosts that lack ripgrep. On platforms where rg is
	// not embedded (any non-linux-amd64 build) EmbeddedRgBytes returns
	// nil and the SSH driver gracefully falls back to `grep -rn`.
	iodriver.RegisterRgBytes(EmbeddedRgBytes())

	return fantasy.NewAgentTool(
		SetWorkspaceToolName,
		setWorkspaceDescription,
		func(ctx context.Context, params SetWorkspaceParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.NewTextErrorResponse("set_workspace requires a session context"), nil
			}
			uri := strings.TrimSpace(params.URI)
			if uri == "" {
				return fantasy.NewTextErrorResponse("uri is required"), nil
			}

			kind, _, cwd, err := iodriver.ParseURI(uri, "")
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid workspace URI: %v", err)), nil
			}

			report := map[string]any{
				"session_id": sessionID,
				"uri":        uri,
				"kind":       string(kind),
				"working_dir": cwd,
			}

			if params.Validate {
				driver, dErr := factory.Get(ctx, uri)
				if dErr != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("workspace probe failed: %v", dErr)), nil
				}
				stdout, stderr, code, runErr := driver.Exec(ctx, []string{"sh", "-c", "pwd && uname -srm"}, nil)
				if runErr != nil || code != 0 {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("workspace probe failed (exit=%d): %v %s", code, runErr, string(stderr))), nil
				}
				probe := strings.SplitN(strings.TrimSpace(string(stdout)), "\n", 2)
				if len(probe) > 0 {
					report["remote_pwd"] = probe[0]
					report["working_dir"] = probe[0]
				}
				if len(probe) > 1 {
					report["remote_uname"] = probe[1]
				}
			}

			// Commit the switch only after validation (if requested).
			registry.Set(sessionID, uri)

			out, _ := json.MarshalIndent(report, "", "  ")
			return fantasy.NewTextResponse(string(out)), nil
		},
	)
}
