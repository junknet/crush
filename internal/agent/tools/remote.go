package tools

import (
	"context"
	"fmt"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/iodriver"
)

const (
	// RemoteAttachToolName attaches the session to a remote host so all
	// file/exec tools transparently operate there.
	RemoteAttachToolName = "RemoteAttach"
	// RemoteDetachToolName reverts the session to local operation.
	RemoteDetachToolName = "RemoteDetach"
)

// RemoteAttachParams selects the host (and optional remote working dir) to make
// the active backend for this session.
type RemoteAttachParams struct {
	Host string `json:"host" description:"SSH host or user@host (prefer a ~/.ssh/config alias). Keys must be configured; no password prompt."`
	Path string `json:"path,omitempty" description:"Optional remote working directory to use as the default root. Defaults to the remote home directory."`
}

// NewRemoteAttachTool wires RemoteAttach against the shared per-session backend
// registry. After a successful attach, bash/Edit/Read/Write/Grep in this session
// run on the remote host as if local, over a persistent daemon channel.
func NewRemoteAttachTool(backends *csync.Map[string, iodriver.Backend], dataDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		RemoteAttachToolName,
		remoteAttachDescription,
		func(ctx context.Context, params RemoteAttachParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Host == "" {
				return fantasy.NewTextErrorResponse("remote_attach: missing host"), nil
			}
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.NewTextErrorResponse("remote_attach: no session in context"), nil
			}

			backend, err := iodriver.DialSSH(ctx, dataDir, params.Host, params.Path)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("remote_attach: %v", err)), nil
			}

			// Replace any prior attachment for this session, tearing the old one
			// down so its daemon channel does not leak.
			if old, ok := backends.Get(sessionID); ok && old != nil {
				_ = old.Close()
			}
			backends.Set(sessionID, backend)

			return fantasy.NewTextResponse(fmt.Sprintf(
				"Attached to %s. File and shell tools now operate on the remote host (root: %s). Use RemoteDetach to return to local.",
				backend.Kind(), backend.Root(),
			)), nil
		},
	)
}

// NewRemoteDetachTool wires RemoteDetach against the same registry.
func NewRemoteDetachTool(backends *csync.Map[string, iodriver.Backend]) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		RemoteDetachToolName,
		"Detach the current session from its remote host and return all file/shell tools to local operation. No-op if not attached.",
		func(ctx context.Context, _ struct{}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.NewTextErrorResponse("remote_detach: no session in context"), nil
			}
			backend, ok := backends.Get(sessionID)
			if !ok || backend == nil {
				return fantasy.NewTextResponse("Not attached to any remote host; tools already operate locally."), nil
			}
			kind := backend.Kind()
			_ = backend.Close()
			backends.Del(sessionID)
			return fantasy.NewTextResponse(fmt.Sprintf("Detached from %s. Tools now operate locally.", kind)), nil
		},
	)
}

const remoteAttachDescription = `Attach this session to a remote host over SSH so that every subsequent file and shell tool — bash, Edit, Read, Write, Grep — operates on the remote machine transparently, as if it were local.

This deploys a small daemon (the crush binary itself) to the host on first use and connects to it over a persistent, connection-multiplexed SSH channel. Prefer this over one-shot SSH tools for any remote work spanning more than a single command: it keeps one channel, preserves the working directory across commands, and routes file edits to the remote filesystem with exact-byte fidelity.

Requires key-based SSH access to the host (no password prompt) and a matching architecture/OS between local and remote. Call RemoteDetach to return to local operation.`
