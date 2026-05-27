package tools

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
)

const (
	SSHExecToolName          = "ssh_exec"
	SSHSessionStartToolName  = "ssh_session_start"
	SSHSessionOutputToolName = "ssh_session_output"
	SSHSessionSendToolName   = "ssh_session_send"
	SSHSessionKillToolName   = "ssh_session_kill"
	SSHMountToolName         = "ssh_mount"
	SSHUnmountToolName       = "ssh_unmount"

	defaultSSHTimeoutSeconds      = 120
	defaultSSHConnectTimeout      = "ConnectTimeout=10"
	defaultSSHSessionCaptureLines = 200
)

//go:embed ssh_exec.md
var sshExecDescription string

//go:embed ssh_session_start.md
var sshSessionStartDescription string

//go:embed ssh_session_output.md
var sshSessionOutputDescription string

//go:embed ssh_session_send.md
var sshSessionSendDescription string

//go:embed ssh_session_kill.md
var sshSessionKillDescription string

//go:embed ssh_mount.md
var sshMountDescription string

//go:embed ssh_unmount.md
var sshUnmountDescription string

type SSHExecParams struct {
	Host           string `json:"host" description:"OpenSSH host alias or user@host target. Prefer ~/.ssh/config aliases."`
	Command        string `json:"command" description:"Remote command to execute"`
	WorkingDir     string `json:"working_dir,omitempty" description:"Remote working directory. If set, the command runs from this directory."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"Max seconds before the local ssh process is killed (default 120)"`
}

type SSHSessionStartParams struct {
	Host       string `json:"host" description:"OpenSSH host alias or user@host target. Prefer ~/.ssh/config aliases."`
	Session    string `json:"session,omitempty" description:"Remote tmux session name. Defaults to a generated crush_<timestamp> name."`
	Command    string `json:"command,omitempty" description:"Command to start inside the remote tmux PTY. Defaults to the remote login shell."`
	WorkingDir string `json:"working_dir,omitempty" description:"Remote working directory for the tmux session."`
}

type SSHSessionOutputParams struct {
	Host    string `json:"host" description:"OpenSSH host alias or user@host target"`
	Session string `json:"session" description:"Remote tmux session name returned by ssh_session_start"`
	Lines   int    `json:"lines,omitempty" description:"Number of recent pane lines to capture (default 200)"`
}

type SSHSessionSendParams struct {
	Host       string `json:"host" description:"OpenSSH host alias or user@host target"`
	Session    string `json:"session" description:"Remote tmux session name returned by ssh_session_start"`
	Text       string `json:"text,omitempty" description:"Literal text to send into the remote PTY"`
	Enter      bool   `json:"enter,omitempty" description:"Send Enter after text"`
	Key        string `json:"key,omitempty" description:"Single tmux key name to send, for example C-c, Escape, Up, Down, Enter"`
	ReadAfter  bool   `json:"read_after,omitempty" description:"Capture recent output after sending"`
	OutputLine int    `json:"output_lines,omitempty" description:"Recent lines to capture when read_after is true"`
}

type SSHSessionKillParams struct {
	Host    string `json:"host" description:"OpenSSH host alias or user@host target"`
	Session string `json:"session" description:"Remote tmux session name returned by ssh_session_start"`
}

type SSHMountParams struct {
	Host       string `json:"host" description:"OpenSSH host alias or user@host target"`
	RemotePath string `json:"remote_path" description:"Remote directory path to mount via sshfs"`
	MountPath  string `json:"mount_path,omitempty" description:"Local mount path. Defaults under the workspace data directory."`
}

type SSHUnmountParams struct {
	MountPath string `json:"mount_path" description:"Local sshfs mount path returned by ssh_mount"`
}

type SSHResponseMetadata struct {
	Host       string `json:"host,omitempty"`
	Session    string `json:"session,omitempty"`
	Command    string `json:"command,omitempty"`
	MountPath  string `json:"mount_path,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`
	ExitCode   int    `json:"exit_code"`
}

type sshToolEnv struct {
	permissions permission.Service
	dataDir     string
}

func NewSSHExecTool(permissions permission.Service) fantasy.AgentTool {
	env := sshToolEnv{permissions: permissions}
	return fantasy.NewAgentTool(
		SSHExecToolName,
		sshExecDescription,
		func(ctx context.Context, params SSHExecParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Host == "" {
				return fantasy.NewTextErrorResponse("ssh_exec: missing host"), nil
			}
			if err := validateSSHTarget(params.Host); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if strings.TrimSpace(params.Command) == "" {
				return fantasy.NewTextErrorResponse("ssh_exec: missing command"), nil
			}
			if ok, err := env.request(ctx, call, params.Host, "execute", params); err != nil || !ok {
				return permissionDeniedResponse(err), nil
			}

			timeout := sshTimeout(params.TimeoutSeconds)
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			remoteCommand := buildRemoteShellCommand(params.WorkingDir, params.Command)
			output, exitCode, err := runSSHCommand(runCtx, params.Host, remoteCommand)
			metadata := SSHResponseMetadata{
				Host:     params.Host,
				Command:  remoteCommand,
				ExitCode: exitCode,
			}
			if err != nil {
				return sshToolResponse(outputWithError(output, err), metadata, true), nil
			}
			return sshToolResponse(outputOrNoOutput(output), metadata, false), nil
		},
	)
}

func NewSSHSessionStartTool(permissions permission.Service) fantasy.AgentTool {
	env := sshToolEnv{permissions: permissions}
	return fantasy.NewAgentTool(
		SSHSessionStartToolName,
		sshSessionStartDescription,
		func(ctx context.Context, params SSHSessionStartParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Host == "" {
				return fantasy.NewTextErrorResponse("ssh_session_start: missing host"), nil
			}
			if err := validateSSHTarget(params.Host); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			session := params.Session
			if session == "" {
				session = fmt.Sprintf("crush_%d", time.Now().Unix())
			}
			if !validTmuxSessionName(session) {
				return fantasy.NewTextErrorResponse("ssh_session_start: session must match [A-Za-z0-9_.-]+"), nil
			}
			if ok, err := env.request(ctx, call, params.Host, "start_session", params); err != nil || !ok {
				return permissionDeniedResponse(err), nil
			}

			command := params.Command
			if strings.TrimSpace(command) == "" {
				command = "${SHELL:-sh} -l"
			}
			remoteCommand := buildTmuxStartCommand(session, params.WorkingDir, command)
			output, exitCode, err := runSSHCommand(ctx, params.Host, remoteCommand)
			metadata := SSHResponseMetadata{
				Host:     params.Host,
				Session:  session,
				Command:  remoteCommand,
				ExitCode: exitCode,
			}
			if err != nil {
				return sshToolResponse(outputWithError(output, err), metadata, true), nil
			}
			result := fmt.Sprintf("Started remote tmux session %s on %s.", session, params.Host)
			if strings.TrimSpace(output) != "" {
				result += "\n\n" + output
			}
			return sshToolResponse(result, metadata, false), nil
		},
	)
}

func NewSSHSessionOutputTool(permissions permission.Service) fantasy.AgentTool {
	env := sshToolEnv{permissions: permissions}
	return fantasy.NewAgentTool(
		SSHSessionOutputToolName,
		sshSessionOutputDescription,
		func(ctx context.Context, params SSHSessionOutputParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Host == "" {
				return fantasy.NewTextErrorResponse("ssh_session_output: missing host"), nil
			}
			if err := validateSSHTarget(params.Host); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if params.Session == "" {
				return fantasy.NewTextErrorResponse("ssh_session_output: missing session"), nil
			}
			if !validTmuxSessionName(params.Session) {
				return fantasy.NewTextErrorResponse("ssh_session_output: invalid session name"), nil
			}
			if ok, err := env.request(ctx, call, params.Host, "read_session", params); err != nil || !ok {
				return permissionDeniedResponse(err), nil
			}
			lines := params.Lines
			if lines <= 0 {
				lines = defaultSSHSessionCaptureLines
			}
			remoteCommand := fmt.Sprintf("tmux capture-pane -pt %s -S -%d", shellQuote(params.Session), lines)
			output, exitCode, err := runSSHCommand(ctx, params.Host, remoteCommand)
			metadata := SSHResponseMetadata{Host: params.Host, Session: params.Session, Command: remoteCommand, ExitCode: exitCode}
			if err != nil {
				return sshToolResponse(outputWithError(output, err), metadata, true), nil
			}
			return sshToolResponse(outputOrNoOutput(output), metadata, false), nil
		},
	)
}

func NewSSHSessionSendTool(permissions permission.Service) fantasy.AgentTool {
	env := sshToolEnv{permissions: permissions}
	return fantasy.NewAgentTool(
		SSHSessionSendToolName,
		sshSessionSendDescription,
		func(ctx context.Context, params SSHSessionSendParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Host == "" {
				return fantasy.NewTextErrorResponse("ssh_session_send: missing host"), nil
			}
			if err := validateSSHTarget(params.Host); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if params.Session == "" {
				return fantasy.NewTextErrorResponse("ssh_session_send: missing session"), nil
			}
			if params.Text == "" && params.Key == "" && !params.Enter {
				return fantasy.NewTextErrorResponse("ssh_session_send: provide text, key, or enter=true"), nil
			}
			if !validTmuxSessionName(params.Session) {
				return fantasy.NewTextErrorResponse("ssh_session_send: invalid session name"), nil
			}
			if ok, err := env.request(ctx, call, params.Host, "send_session_input", params); err != nil || !ok {
				return permissionDeniedResponse(err), nil
			}

			remoteCommand := buildTmuxSendCommand(params)
			output, exitCode, err := runSSHCommand(ctx, params.Host, remoteCommand)
			metadata := SSHResponseMetadata{Host: params.Host, Session: params.Session, Command: remoteCommand, ExitCode: exitCode}
			if err != nil {
				return sshToolResponse(outputWithError(output, err), metadata, true), nil
			}
			if params.ReadAfter {
				lines := params.OutputLine
				if lines <= 0 {
					lines = defaultSSHSessionCaptureLines
				}
				readCommand := fmt.Sprintf("tmux capture-pane -pt %s -S -%d", shellQuote(params.Session), lines)
				readOutput, readExit, readErr := runSSHCommand(ctx, params.Host, readCommand)
				metadata.Command = remoteCommand + " && " + readCommand
				metadata.ExitCode = readExit
				if readErr != nil {
					return sshToolResponse(outputWithError(readOutput, readErr), metadata, true), nil
				}
				return sshToolResponse(outputOrNoOutput(readOutput), metadata, false), nil
			}
			return sshToolResponse(outputOrNoOutput(output), metadata, false), nil
		},
	)
}

func NewSSHSessionKillTool(permissions permission.Service) fantasy.AgentTool {
	env := sshToolEnv{permissions: permissions}
	return fantasy.NewAgentTool(
		SSHSessionKillToolName,
		sshSessionKillDescription,
		func(ctx context.Context, params SSHSessionKillParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Host == "" {
				return fantasy.NewTextErrorResponse("ssh_session_kill: missing host"), nil
			}
			if err := validateSSHTarget(params.Host); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if params.Session == "" {
				return fantasy.NewTextErrorResponse("ssh_session_kill: missing session"), nil
			}
			if !validTmuxSessionName(params.Session) {
				return fantasy.NewTextErrorResponse("ssh_session_kill: invalid session name"), nil
			}
			if ok, err := env.request(ctx, call, params.Host, "kill_session", params); err != nil || !ok {
				return permissionDeniedResponse(err), nil
			}
			remoteCommand := fmt.Sprintf("tmux kill-session -t %s", shellQuote(params.Session))
			output, exitCode, err := runSSHCommand(ctx, params.Host, remoteCommand)
			metadata := SSHResponseMetadata{Host: params.Host, Session: params.Session, Command: remoteCommand, ExitCode: exitCode}
			if err != nil {
				return sshToolResponse(outputWithError(output, err), metadata, true), nil
			}
			return sshToolResponse(fmt.Sprintf("Killed remote tmux session %s on %s.", params.Session, params.Host), metadata, false), nil
		},
	)
}

func NewSSHMountTool(permissions permission.Service, dataDir string) fantasy.AgentTool {
	env := sshToolEnv{permissions: permissions, dataDir: dataDir}
	return fantasy.NewAgentTool(
		SSHMountToolName,
		sshMountDescription,
		func(ctx context.Context, params SSHMountParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Host == "" {
				return fantasy.NewTextErrorResponse("ssh_mount: missing host"), nil
			}
			if err := validateSSHTarget(params.Host); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if params.RemotePath == "" {
				return fantasy.NewTextErrorResponse("ssh_mount: missing remote_path"), nil
			}
			if ok, err := env.request(ctx, call, params.Host, "mount", params); err != nil || !ok {
				return permissionDeniedResponse(err), nil
			}
			sshfsPath, err := exec.LookPath("sshfs")
			if err != nil {
				return fantasy.NewTextErrorResponse("ssh_mount: sshfs not found in PATH"), nil
			}
			mountPath := params.MountPath
			if mountPath == "" {
				mountPath = filepath.Join(env.dataDir, "remotes", safePathComponent(params.Host), safePathComponent(params.RemotePath))
			}
			absMountPath, err := filepath.Abs(mountPath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("ssh_mount: invalid mount_path: %v", err)), nil
			}
			if err := os.MkdirAll(absMountPath, 0o755); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("ssh_mount: create mount_path: %v", err)), nil
			}

			target := params.Host + ":" + params.RemotePath
			cmd := exec.CommandContext(ctx, sshfsPath,
				"-o", "BatchMode=yes",
				"-o", defaultSSHConnectTimeout,
				target,
				absMountPath,
			)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err = cmd.Run()
			exitCode := commandExitCode(err)
			metadata := SSHResponseMetadata{
				Host:       params.Host,
				RemotePath: params.RemotePath,
				MountPath:  absMountPath,
				ExitCode:   exitCode,
			}
			if err != nil {
				return sshToolResponse(outputWithError(out.String(), err), metadata, true), nil
			}
			return sshToolResponse(fmt.Sprintf("Mounted %s at %s. Use local tools like rg/view/edit against that path.", target, absMountPath), metadata, false), nil
		},
	)
}

func NewSSHUnmountTool(permissions permission.Service) fantasy.AgentTool {
	env := sshToolEnv{permissions: permissions}
	return fantasy.NewAgentTool(
		SSHUnmountToolName,
		sshUnmountDescription,
		func(ctx context.Context, params SSHUnmountParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.MountPath == "" {
				return fantasy.NewTextErrorResponse("ssh_unmount: missing mount_path"), nil
			}
			absMountPath, err := filepath.Abs(params.MountPath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("ssh_unmount: invalid mount_path: %v", err)), nil
			}
			if ok, err := env.request(ctx, call, absMountPath, "unmount", params); err != nil || !ok {
				return permissionDeniedResponse(err), nil
			}
			cmdName, args := unmountCommand(absMountPath)
			if cmdName == "" {
				return fantasy.NewTextErrorResponse("ssh_unmount: no unmount command found (tried fusermount3, fusermount, umount)"), nil
			}
			cmd := exec.CommandContext(ctx, cmdName, args...)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err = cmd.Run()
			exitCode := commandExitCode(err)
			metadata := SSHResponseMetadata{MountPath: absMountPath, ExitCode: exitCode}
			if err != nil {
				return sshToolResponse(outputWithError(out.String(), err), metadata, true), nil
			}
			return sshToolResponse(fmt.Sprintf("Unmounted %s.", absMountPath), metadata, false), nil
		},
	)
}

func (s sshToolEnv) request(ctx context.Context, call fantasy.ToolCall, path, action string, params any) (bool, error) {
	if s.permissions == nil {
		return true, nil
	}
	return s.permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   GetSessionFromContext(ctx),
		ToolCallID:  call.ID,
		ToolName:    call.Name,
		Description: fmt.Sprintf("%s %s", call.Name, path),
		Action:      action,
		Params:      params,
		Path:        path,
	})
}

func runSSHCommand(ctx context.Context, host, remoteCommand string) (string, int, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return "", 127, fmt.Errorf("ssh not found in PATH")
	}
	cmd := exec.CommandContext(ctx, sshPath,
		"-o", "BatchMode=yes",
		"-o", defaultSSHConnectTimeout,
		host,
		remoteCommand,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return TruncateOutput(out.String()), commandExitCode(err), err
}

func sshToolResponse(content string, metadata SSHResponseMetadata, isError bool) fantasy.ToolResponse {
	resp := fantasy.WithResponseMetadata(fantasy.NewTextResponse(content), metadata)
	if isError {
		resp = fantasy.WithResponseMetadata(fantasy.NewTextErrorResponse(content), metadata)
	}
	return resp
}

func permissionDeniedResponse(err error) fantasy.ToolResponse {
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error())
	}
	return fantasy.NewTextErrorResponse("permission denied")
}

func sshTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = defaultSSHTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func buildRemoteShellCommand(workingDir, command string) string {
	if strings.TrimSpace(workingDir) == "" {
		return command
	}
	return "cd -- " + shellQuote(workingDir) + " && " + command
}

func buildTmuxStartCommand(session, workingDir, command string) string {
	command = buildRemoteShellCommand(workingDir, command)
	return fmt.Sprintf("tmux new-session -d -s %s %s", shellQuote(session), shellQuote(command))
}

func buildTmuxSendCommand(params SSHSessionSendParams) string {
	target := shellQuote(params.Session)
	commands := make([]string, 0, 3)
	if params.Text != "" {
		commands = append(commands, fmt.Sprintf("tmux send-keys -t %s -l %s", target, shellQuote(params.Text)))
	}
	if params.Key != "" {
		commands = append(commands, fmt.Sprintf("tmux send-keys -t %s %s", target, shellQuote(params.Key)))
	}
	if params.Enter {
		commands = append(commands, fmt.Sprintf("tmux send-keys -t %s Enter", target))
	}
	return strings.Join(commands, " && ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

var tmuxSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validTmuxSessionName(name string) bool {
	return tmuxSessionNamePattern.MatchString(name)
}

var safePathPattern = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func safePathComponent(value string) string {
	value = strings.Trim(safePathPattern.ReplaceAllString(value, "_"), "_")
	if value == "" {
		return "root"
	}
	return value
}

func outputOrNoOutput(output string) string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return BashNoOutput
	}
	return output
}

func outputWithError(output string, err error) string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return err.Error()
	}
	return output + "\n\nError: " + err.Error()
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func unmountCommand(mountPath string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "", nil
	}
	if path, err := exec.LookPath("fusermount3"); err == nil {
		return path, []string{"-u", mountPath}
	}
	if path, err := exec.LookPath("fusermount"); err == nil {
		return path, []string{"-u", mountPath}
	}
	if path, err := exec.LookPath("umount"); err == nil {
		return path, []string{mountPath}
	}
	return "", nil
}

func validateSSHTarget(host string) error {
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("ssh target must not start with '-'")
	}
	if strings.ContainsAny(host, "\x00\r\n") {
		return fmt.Errorf("ssh target contains invalid control characters")
	}
	return nil
}
