package tools

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/shell"
)

type RunParams struct {
	Language       string `json:"language,omitempty" description:"Script language: shell (default), python, or node"`
	Script         string `json:"script" description:"Source code to execute"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"Timeout in seconds, default 60, max 300"`
}

const RunToolName = "run"

//go:embed run.md
var runDescription string

func NewRunTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		RunToolName,
		runDescription,
		func(ctx context.Context, params RunParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if strings.TrimSpace(params.Script) == "" {
				return fantasy.NewTextErrorResponse("missing script"), nil
			}
			if message, blocked := blockForegroundSleep(params.Script); blocked {
				return fantasy.NewTextErrorResponse(message), nil
			}

			language := strings.ToLower(strings.TrimSpace(params.Language))
			if language == "" {
				language = "shell"
			}
			if language != "shell" && language != "python" && language != "node" {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("run supports only shell/python/node, not %q. For a compiled language (rust/go/etc.), use the bash tool to invoke its toolchain (e.g. `cargo run`, `go run`).", params.Language)), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing run script")
			}

			p, err := permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        workingDir,
					ToolCallID:  call.ID,
					ToolName:    RunToolName,
					Action:      "execute",
					Description: fmt.Sprintf("Execute %s script", language),
					Params:      params,
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p {
				return NewPermissionDeniedResponse(), nil
			}

			timeout := runTimeout(params.TimeoutSeconds)
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			stdout, stderr, err := executeRunScript(runCtx, workingDir, language, params.Script)
			output := formatOutput(stdout, stderr, err)
			if runCtx.Err() == context.DeadlineExceeded {
				output = strings.TrimSpace(output + "\nrun timed out after " + timeout.String())
			}
			if output == "" {
				output = BashNoOutput
			}
			return fantasy.NewTextResponse(output), nil
		},
	)
}

func runTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 60 * time.Second
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func executeRunScript(ctx context.Context, workingDir, language, script string) (string, string, error) {
	if language == "shell" {
		var stdout, stderr bytes.Buffer
		err := shell.Run(ctx, shell.RunOptions{
			Command:    script,
			Cwd:        workingDir,
			Env:        os.Environ(),
			Stdout:     &stdout,
			Stderr:     &stderr,
			BlockFuncs: blockFuncs(),
		})
		return stdout.String(), stderr.String(), err
	}

	extension := ".js"
	candidates := []string{"node"}
	if language == "python" {
		extension = ".py"
		candidates = []string{"python3", "python"}
	}
	interpreter, err := lookupFirst(candidates)
	if err != nil {
		return "", "", err
	}

	file, err := os.CreateTemp("", "crush-run-*"+extension)
	if err != nil {
		return "", "", fmt.Errorf("create temp script: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(script); err != nil {
		file.Close()
		return "", "", fmt.Errorf("write temp script: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", "", fmt.Errorf("close temp script: %w", err)
	}

	cmd := exec.CommandContext(ctx, interpreter, file.Name())
	cmd.Dir = workingDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func lookupFirst(names []string) (string, error) {
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no interpreter found in PATH: %s", strings.Join(names, ", "))
}
