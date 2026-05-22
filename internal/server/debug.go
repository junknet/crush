package server

import (
	"net/http"
	"time"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/lsp"
)

// debugWorkspaceState is the per-workspace agent snapshot in the debug dump.
type debugWorkspaceState struct {
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	IsBusy      bool   `json:"is_busy"`
	IsReady     bool   `json:"is_ready"`
}

// debugLSPState is a single LSP server's snapshot.
type debugLSPState struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	DiagnosticCount int    `json:"diagnostic_count"`
	ConnectedAt     string `json:"connected_at,omitempty"`
	Error           string `json:"error,omitempty"`
}

// debugMCPState is a single MCP server's snapshot.
type debugMCPState struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	ConnectedAt string `json:"connected_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

// debugStateResponse is the full process-wide observability snapshot returned
// by GET /v1/debug/state. It exists so a developer can dump live coordinator,
// LSP, and MCP state in one request instead of inferring it from logs.
type debugStateResponse struct {
	Time       string                `json:"time"`
	Workspaces []debugWorkspaceState `json:"workspaces"`
	LSP        []debugLSPState       `json:"lsp"`
	MCP        []debugMCPState       `json:"mcp"`
}

func lspStateName(s lsp.ServerState) string {
	switch s {
	case lsp.StateUnstarted:
		return "unstarted"
	case lsp.StateStarting:
		return "starting"
	case lsp.StateReady:
		return "ready"
	case lsp.StateError:
		return "error"
	case lsp.StateStopped:
		return "stopped"
	case lsp.StateDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// handleGetDebugState returns a one-shot snapshot of live internal state across
// all workspaces: agent model/busy/ready, LSP connections, and MCP connections.
//
//	@Summary		Debug state snapshot
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	debugStateResponse
//	@Router			/debug/state [get]
func (c *controllerV1) handleGetDebugState(w http.ResponseWriter, _ *http.Request) {
	resp := debugStateResponse{
		Time:       time.Now().UTC().Format(time.RFC3339Nano),
		Workspaces: []debugWorkspaceState{},
		LSP:        []debugLSPState{},
		MCP:        []debugMCPState{},
	}

	for _, ws := range c.backend.ListWorkspaces() {
		state := debugWorkspaceState{WorkspaceID: ws.ID, Path: ws.Path}
		if info, err := c.backend.GetAgentInfo(ws.ID); err == nil {
			state.Model = info.Model.Name
			state.Provider = info.ModelCfg.Provider
			state.IsBusy = info.IsBusy
			state.IsReady = info.IsReady
		}
		resp.Workspaces = append(resp.Workspaces, state)
	}

	for name, info := range app.GetLSPStates() {
		entry := debugLSPState{
			Name:            name,
			State:           lspStateName(info.State),
			DiagnosticCount: info.DiagnosticCount,
		}
		if !info.ConnectedAt.IsZero() {
			entry.ConnectedAt = info.ConnectedAt.UTC().Format(time.RFC3339)
		}
		if info.Error != nil {
			entry.Error = info.Error.Error()
		}
		resp.LSP = append(resp.LSP, entry)
	}

	for name, info := range mcp.GetStates() {
		entry := debugMCPState{
			Name:  name,
			State: info.State.String(),
		}
		if !info.ConnectedAt.IsZero() {
			entry.ConnectedAt = info.ConnectedAt.UTC().Format(time.RFC3339)
		}
		if info.Error != nil {
			entry.Error = info.Error.Error()
		}
		resp.MCP = append(resp.MCP, entry)
	}

	jsonEncode(w, resp)
}
