package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
)

type RunParams struct {
	Language       string `json:"language,omitempty" description:"Script language: shell (default), python, or node"`
	Script         string `json:"script" description:"Source code to execute"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"Timeout in seconds, default 60, max 300"`
}

const RunToolName = "run"

func executeRunScript(ctx context.Context, workingDir, language, script string) (string, string, error) {
	if language == "shell" {
		var stdout, stderr bytes.Buffer
		err := shell.Run(ctx, shell.RunOptions{
			Command:      script,
			Cwd:          workingDir,
			Env:          os.Environ(),
			Stdout:       &stdout,
			Stderr:       &stderr,
			BlockFuncs:   blockFuncs(),
			RewriteFuncs: bashRewriteFuncs(),
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
