package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHExecBuildsRemoteWorkingDirectoryCommand(t *testing.T) {
	binDir := prependFakeCommand(t, "ssh", `#!/bin/sh
printf 'host=%s\ncmd=%s\n' "$5" "$6"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool := NewSSHExecTool(nil)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  SSHExecToolName,
		Input: `{"host":"ecs-app","working_dir":"/srv/app","command":"go test ./..."}`,
	})

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "host=ecs-app")
	require.Contains(t, resp.Content, "cmd=cd -- '/srv/app' && go test ./...")
	require.Contains(t, resp.Metadata, `"host":"ecs-app"`)
	require.Contains(t, resp.Metadata, `"exit_code":0`)
}

func TestSSHSessionStartUsesRemoteTmux(t *testing.T) {
	binDir := prependFakeCommand(t, "ssh", `#!/bin/sh
printf '%s\n' "$6"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool := NewSSHSessionStartTool(nil)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:   "call-1",
		Name: SSHSessionStartToolName,
		Input: `{
			"host":"ecs-app",
			"session":"crush_demo",
			"working_dir":"/srv/app",
			"command":"./server"
		}`,
	})

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "Started remote tmux session crush_demo on ecs-app.")
	require.Contains(t, resp.Content, "tmux new-session -d -s 'crush_demo'")
	require.Contains(t, resp.Content, "'cd -- '\\''/srv/app'\\'' && ./server'")
}

func TestSSHSessionSendBuildsLiteralTextAndEnter(t *testing.T) {
	binDir := prependFakeCommand(t, "ssh", `#!/bin/sh
printf '%s\n' "$6"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool := NewSSHSessionSendTool(nil)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:   "call-1",
		Name: SSHSessionSendToolName,
		Input: `{
			"host":"ecs-app",
			"session":"crush_demo",
			"text":"echo hello",
			"enter":true
		}`,
	})

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "tmux send-keys -t 'crush_demo' -l 'echo hello'")
	require.Contains(t, resp.Content, "tmux send-keys -t 'crush_demo' Enter")
}

func TestSSHMountUsesSSHFSAndDefaultMountRoot(t *testing.T) {
	binDir := prependFakeCommand(t, "sshfs", `#!/bin/sh
target=""
mount=""
for arg do
  target="$mount"
  mount="$arg"
done
printf '%s\n%s\n' "$target" "$mount" > "$SSHFS_LOG"
mkdir -p "$mount"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(t.TempDir(), "sshfs.log")
	t.Setenv("SSHFS_LOG", logPath)
	dataDir := t.TempDir()

	tool := NewSSHMountTool(nil, dataDir)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  SSHMountToolName,
		Input: `{"host":"ecs-app","remote_path":"/srv/app"}`,
	})

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "Mounted ecs-app:/srv/app")
	require.Contains(t, resp.Content, filepath.Join(dataDir, "remotes", "ecs-app", "srv_app"))

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logBytes), "ecs-app:/srv/app")
}

func TestSSHUnmountUsesAvailableUnmountCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sshfs unmount helpers are POSIX-only")
	}

	binDir := prependFakeCommand(t, "fusermount3", `#!/bin/sh
printf '%s\n%s\n' "$1" "$2" > "$UNMOUNT_LOG"
`)
	t.Setenv("PATH", binDir)
	logPath := filepath.Join(t.TempDir(), "unmount.log")
	t.Setenv("UNMOUNT_LOG", logPath)

	tool := NewSSHUnmountTool(nil)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  SSHUnmountToolName,
		Input: `{"mount_path":"/tmp/crush-remote"}`,
	})

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "Unmounted /tmp/crush-remote.")

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logBytes), "-u\n/tmp/crush-remote")
}

func TestSSHIntegrationExecAndRemoteTmux(t *testing.T) {
	host := os.Getenv("CRUSH_SSH_INTEGRATION_HOST")
	if host == "" {
		t.Skip("Set CRUSH_SSH_INTEGRATION_HOST to run the real SSH integration test.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	session := fmt.Sprintf("crush_it_%d", time.Now().UnixNano())
	defer func() {
		_, _, _ = runSSHCommand(context.Background(), host, "tmux kill-session -t "+shellQuote(session)+" 2>/dev/null || true")
	}()

	execTool := NewSSHExecTool(nil)
	execResp, err := execTool.Run(ctx, fantasy.ToolCall{
		ID:   "call-ssh-exec",
		Name: SSHExecToolName,
		Input: fmt.Sprintf(
			`{"host":%q,"command":"printf 'crush-ssh-exec-ok\\n'; hostname","timeout_seconds":10}`,
			host,
		),
	})
	require.NoError(t, err)
	require.False(t, execResp.IsError, execResp.Content)
	require.Contains(t, execResp.Content, "crush-ssh-exec-ok")

	startTool := NewSSHSessionStartTool(nil)
	startResp, err := startTool.Run(ctx, fantasy.ToolCall{
		ID:   "call-ssh-session-start",
		Name: SSHSessionStartToolName,
		Input: fmt.Sprintf(
			`{"host":%q,"session":%q,"command":"${SHELL:-sh} -l"}`,
			host,
			session,
		),
	})
	require.NoError(t, err)
	require.False(t, startResp.IsError, startResp.Content)
	require.Contains(t, startResp.Content, "Started remote tmux session "+session)

	sendTool := NewSSHSessionSendTool(nil)
	sendResp, err := sendTool.Run(ctx, fantasy.ToolCall{
		ID:   "call-ssh-session-send",
		Name: SSHSessionSendToolName,
		Input: fmt.Sprintf(
			`{"host":%q,"session":%q,"text":"printf 'crush-ssh-pty-ok\\n'; pwd","enter":true}`,
			host,
			session,
		),
	})
	require.NoError(t, err)
	require.False(t, sendResp.IsError, sendResp.Content)

	outputTool := NewSSHSessionOutputTool(nil)
	var outputResp fantasy.ToolResponse
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var runErr error
		outputResp, runErr = outputTool.Run(ctx, fantasy.ToolCall{
			ID:   "call-ssh-session-output",
			Name: SSHSessionOutputToolName,
			Input: fmt.Sprintf(
				`{"host":%q,"session":%q,"lines":80}`,
				host,
				session,
			),
		})
		require.NoError(collect, runErr)
		require.False(collect, outputResp.IsError, outputResp.Content)
		require.Contains(collect, outputResp.Content, "crush-ssh-pty-ok")
	}, 10*time.Second, 500*time.Millisecond)

	killTool := NewSSHSessionKillTool(nil)
	killResp, err := killTool.Run(ctx, fantasy.ToolCall{
		ID:   "call-ssh-session-kill",
		Name: SSHSessionKillToolName,
		Input: fmt.Sprintf(
			`{"host":%q,"session":%q}`,
			host,
			session,
		),
	})
	require.NoError(t, err)
	require.False(t, killResp.IsError, killResp.Content)
	require.True(t, strings.Contains(killResp.Content, "Killed remote tmux session "+session))
}

func prependFakeCommand(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte(script), 0o755)
	require.NoError(t, err)
	return dir
}
