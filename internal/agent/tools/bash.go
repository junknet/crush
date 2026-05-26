package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/shell"
)

type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to execute"`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to current directory)"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in the background. Use job_output to read the output later."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait before automatically moving the command to a background job (default: 60)"`
}

type BashPermissionsParams struct {
	Description         string `json:"description"`
	Command             string `json:"command"`
	WorkingDir          string `json:"working_dir"`
	RunInBackground     bool   `json:"run_in_background"`
	AutoBackgroundAfter int    `json:"auto_background_after"`
}

type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	DurationMs       int64  `json:"duration_ms"`
	Output           string `json:"output"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
	ExitCode         int    `json:"exit_code"`
	Outcome          string `json:"outcome"`
	StdoutBytes      int    `json:"stdout_bytes"`
	StderrBytes      int    `json:"stderr_bytes"`
	Background       bool   `json:"background,omitempty"`
	ShellID          string `json:"shell_id,omitempty"`
	SpillPath        string `json:"spill_path,omitempty"`
	SpillBytes       int    `json:"spill_bytes,omitempty"`
}

const (
	BashToolName = "bash"

	DefaultAutoBackgroundAfter = 60 // Commands taking longer automatically become background jobs
	// BashPreviewBytes is what the model sees inline. Head bytes are the
	// most informative slice of a long capture (banner, args, first error
	// stack frame) — tails are usually repetitive noise. So we keep the
	// head and route the full transcript to disk.
	BashPreviewBytes = 8000
	// BashSpillThreshold is the size at which we stop returning the full
	// transcript inline and write it to disk instead. The number is held
	// in sync with MaxOutputLength so the prompt-embedded byte budget
	// stays stable across the spill rewrite.
	BashSpillThreshold = 30000
	// MaxOutputLength is kept as the legacy name for the inline truncation
	// budget used by callers that do not spill (e.g. job_output snapshots).
	MaxOutputLength = BashSpillThreshold
	BashNoOutput    = "no output"

	// spillGCMaxAge is how long a spilled bash transcript lingers before
	// the lazy GC removes it. A week is enough for one work cycle of
	// review and short enough that .crush/ doesn't accumulate forever.
	spillGCMaxAge = 7 * 24 * time.Hour
)

//go:embed bash.md.tpl
var bashDescriptionTmpl []byte

var bashDescriptionTpl = template.Must(
	template.New("bashDescription").
		Parse(string(bashDescriptionTmpl)),
)

type bashDescriptionData struct {
	BannedCommands  string
	MaxOutputLength int
	Attribution     config.Attribution
	ModelID         string
	RgAvailable     bool
}

var bannedCommands = []string{
	// Network/Download tools
	"alias",
	"aria2c",
	"axel",
	"chrome",
	"curl",
	"curlie",
	"firefox",
	"http-prompt",
	"httpie",
	"links",
	"lynx",
	"nc",
	"safari",
	"scp",
	"ssh",
	"telnet",
	"w3m",
	"wget",
	"xh",

	// System administration
	"doas",
	"su",
	"sudo",

	// Package managers
	"apk",
	"apt",
	"apt-cache",
	"apt-get",
	"dnf",
	"dpkg",
	"emerge",
	"home-manager",
	"makepkg",
	"opkg",
	"pacman",
	"paru",
	"pkg",
	"pkg_add",
	"pkg_delete",
	"portage",
	"rpm",
	"yay",
	"yum",
	"zypper",

	// System modification
	"at",
	"batch",
	"chkconfig",
	"crontab",
	"fdisk",
	"mkfs",
	"mount",
	"parted",
	"service",
	"systemctl",
	"umount",

	// Network configuration
	"firewall-cmd",
	"ifconfig",
	"ip",
	"iptables",
	"netstat",
	"pfctl",
	"route",
	"ufw",

	// Interactive tools that hang non-interactive sessions
	"vi",
	"vim",
	"view",
	"nano",
	"emacs",
	"less",
	"more",
}

func bashDescription(attribution *config.Attribution, modelID string) string {
	bannedCommandsStr := strings.Join(bannedCommands, ", ")
	var out bytes.Buffer
	if err := bashDescriptionTpl.Execute(&out, bashDescriptionData{
		BannedCommands:  bannedCommandsStr,
		MaxOutputLength: MaxOutputLength,
		Attribution:     *attribution,
		ModelID:         modelID,
		RgAvailable:     getRg() != "",
	}); err != nil {
		// this should never happen.
		panic("failed to execute bash description template: " + err.Error())
	}
	return out.String()
}

func blockFuncs() []shell.BlockFunc {
	return []shell.BlockFunc{
		shell.CommandsBlocker(bannedCommands),

		// System package managers
		shell.ArgumentsBlocker("apk", []string{"add"}, nil),
		shell.ArgumentsBlocker("apt", []string{"install"}, nil),
		shell.ArgumentsBlocker("apt-get", []string{"install"}, nil),
		shell.ArgumentsBlocker("dnf", []string{"install"}, nil),
		shell.ArgumentsBlocker("pacman", nil, []string{"-S"}),
		shell.ArgumentsBlocker("pkg", []string{"install"}, nil),
		shell.ArgumentsBlocker("yum", []string{"install"}, nil),
		shell.ArgumentsBlocker("zypper", []string{"install"}, nil),

		// Language-specific package managers
		shell.ArgumentsBlocker("brew", []string{"install"}, nil),
		shell.ArgumentsBlocker("cargo", []string{"install"}, nil),
		shell.ArgumentsBlocker("gem", []string{"install"}, nil),
		shell.ArgumentsBlocker("go", []string{"install"}, nil),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"--global"}),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"-g"}),
		shell.ArgumentsBlocker("pip", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pip3", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"--global"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"-g"}),
		shell.ArgumentsBlocker("yarn", []string{"global", "add"}, nil),

		// `go test -exec` can run arbitrary commands
		shell.ArgumentsBlocker("go", []string{"test"}, []string{"-exec"}),

		// Streaming/follow primitives that wedge the tool loop — the agent
		// should use schedule_wakeup / monitor / job_output instead.
		// (Plain `sleep` is left allowed; it is a legitimate shell
		// primitive and existing tests pipeline it through `&&`.)
		shell.CommandsBlocker([]string{"watch"}),
		shell.ArgumentsBlocker("tail", nil, []string{"-f"}),
		shell.ArgumentsBlocker("tail", nil, []string{"--follow"}),
	}
}

func NewBashTool(permissions permission.Service, bgManager *shell.BackgroundShellManager, workingDir, dataDir string, attribution *config.Attribution, modelID string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		BashToolName,
		string(bashDescription(attribution, modelID)),
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("missing command"), nil
			}

			// Determine working directory
			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)

			isSafeReadOnly := false
			cmdLower := strings.ToLower(params.Command)

			for _, safe := range safeCommands {
				if strings.HasPrefix(cmdLower, safe) {
					if len(cmdLower) == len(safe) || cmdLower[len(safe)] == ' ' || cmdLower[len(safe)] == '-' {
						isSafeReadOnly = true
						break
					}
				}
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing shell command")
			}
			if !isSafeReadOnly {
				p, err := permissions.Request(
					ctx,
					permission.CreatePermissionRequest{
						SessionID:   sessionID,
						Path:        execWorkingDir,
						ToolCallID:  call.ID,
						ToolName:    BashToolName,
						Action:      "execute",
						Description: fmt.Sprintf("Execute command: %s", params.Command),
						Params:      BashPermissionsParams(params),
					},
				)
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !p {
					return NewPermissionDeniedResponse(), nil
				}
			}

			// If explicitly requested as background, start immediately with detached context
			if params.RunInBackground {
				startTime := time.Now()
				bgManager.Cleanup()
				// Use background context so it continues after tool returns
				bgShell, err := bgManager.Start(context.Background(), execWorkingDir, blockFuncs(), params.Command, params.Description, sessionID)
				if err != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error starting background shell: %w", err)
				}

				// Wait a short time to detect fast failures (blocked commands, syntax errors, etc.)
				time.Sleep(1 * time.Second)
				stdout, stderr, done, execErr := bgShell.GetOutput()

				if done {
					// Command failed or completed very quickly
					bgManager.Remove(bgShell.ID)

					interrupted := shell.IsInterrupt(execErr)
					exitCode := shell.ExitCode(execErr)
					if exitCode == 0 && !interrupted && execErr != nil {
						return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
					}

					semantics := deriveCommandSemantics(params.Command, stdout, execErr)
					stdoutBytes := len(stdout)
					stderrBytes := len(stderr)
					spillPath, spillBytes := maybeSpillOutput(dataDir, sessionID, call.ID, stdout, stderr)
					stdout = formatOutput(stdout, stderr, execErr)
					if spillPath != "" {
						stdout += fmt.Sprintf("\n<output_spill bytes=\"%d\" path=\"%s\">Full transcript written to disk; use the view tool on this path for the complete output.</output_spill>", spillBytes, spillPath)
					}
					endTime := time.Now()

					metadata := BashResponseMetadata{
						StartTime:        startTime.UnixMilli(),
						EndTime:          endTime.UnixMilli(),
						DurationMs:       endTime.Sub(startTime).Milliseconds(),
						Output:           stdout,
						Description:      params.Description,
						Background:       params.RunInBackground,
						WorkingDirectory: bgShell.WorkingDir,
						SpillPath:        spillPath,
						SpillBytes:       spillBytes,
						ExitCode:         semantics.ExitCode,
						Outcome:          string(semantics.Outcome),
						StdoutBytes:      stdoutBytes,
						StderrBytes:      stderrBytes,
					}
					appendBashCommandTrace(ctx, call.ID, params, metadata, stdout)
					if stdout == "" {
						return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
					}
					stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
				}

				// Still running after fast-failure check - return as background job.
				// Mark it so its completion later wakes the agent automatically.
				bgShell.MarkBackgrounded()
				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          time.Now().UnixMilli(),
					DurationMs:       time.Since(startTime).Milliseconds(),
					Description:      params.Description,
					WorkingDirectory: bgShell.WorkingDir,
					Background:       true,
					ShellID:          bgShell.ID,
					Outcome:          "background_started",
				}
				response := fmt.Sprintf("Background shell started with ID: %s\n\nIt will keep running; you'll be automatically notified to continue when it finishes. You can also use job_output to check on it or job_kill to terminate.", bgShell.ID)
				appendBashCommandTrace(ctx, call.ID, params, metadata, response)
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
			}

			// Start synchronous execution with auto-background support
			startTime := time.Now()

			// Start with detached context so it can survive if moved to background
			bgManager.Cleanup()
			bgShell, err := bgManager.Start(context.Background(), execWorkingDir, blockFuncs(), params.Command, params.Description, sessionID)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error starting shell: %w", err)
			}

			// Wait for either completion, auto-background threshold, or context cancellation
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			autoBackgroundAfter := cmp.Or(params.AutoBackgroundAfter, DefaultAutoBackgroundAfter)
			autoBackgroundThreshold := time.Duration(autoBackgroundAfter) * time.Second
			timeout := time.After(autoBackgroundThreshold)

			var stdout, stderr string
			var done bool
			var execErr error

		waitLoop:
			for {
				select {
				case <-ticker.C:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					if done {
						break waitLoop
					}
				case <-timeout:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					break waitLoop
				case <-ctx.Done():
					// Incoming context was cancelled before we moved to background
					// Kill the shell and return error
					bgManager.Kill(bgShell.ID)
					return fantasy.ToolResponse{}, ctx.Err()
				}
			}

			if done {
				// Command completed within threshold - return synchronously
				// Remove from background manager since we're returning directly
				// Don't call Kill() as it cancels the context and corrupts the exit code
				bgManager.Remove(bgShell.ID)

				interrupted := shell.IsInterrupt(execErr)
				exitCode := shell.ExitCode(execErr)
				if exitCode == 0 && !interrupted && execErr != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
				}

				semantics := deriveCommandSemantics(params.Command, stdout, execErr)
				stdoutBytes := len(stdout)
				stderrBytes := len(stderr)
				spillPath, spillBytes := maybeSpillOutput(dataDir, sessionID, call.ID, stdout, stderr)
				stdout = formatOutput(stdout, stderr, execErr)
				if spillPath != "" {
					stdout += fmt.Sprintf("\n<output_spill bytes=\"%d\" path=\"%s\">Full transcript written to disk; use the view tool on this path for the complete output.</output_spill>", spillBytes, spillPath)
				}
				endTime := time.Now()

				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          endTime.UnixMilli(),
					DurationMs:       endTime.Sub(startTime).Milliseconds(),
					Output:           stdout,
					Description:      params.Description,
					Background:       params.RunInBackground,
					WorkingDirectory: bgShell.WorkingDir,
					ExitCode:         semantics.ExitCode,
					Outcome:          string(semantics.Outcome),
					StdoutBytes:      stdoutBytes,
					StderrBytes:      stderrBytes,
					SpillPath:        spillPath,
					SpillBytes:       spillBytes,
				}
				appendBashCommandTrace(ctx, call.ID, params, metadata, stdout)
				if stdout == "" {
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
				}
				stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
			}

			// Still running - keep as background job. Mark it so its completion
			// later wakes the agent automatically.
			bgShell.MarkBackgrounded()
			metadata := BashResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				DurationMs:       time.Since(startTime).Milliseconds(),
				Description:      params.Description,
				WorkingDirectory: bgShell.WorkingDir,
				Background:       true,
				ShellID:          bgShell.ID,
				Outcome:          "background_started",
			}
			response := fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground shell ID: %s\n\nIt will keep running; you'll be automatically notified to continue when it finishes. You can also use job_output to check on it or job_kill to terminate.", bgShell.ID)
			appendBashCommandTrace(ctx, call.ID, params, metadata, response)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		},
	)
}

// formatOutput formats the output of a completed command with error handling
func formatOutput(stdout, stderr string, execErr error) string {
	interrupted := shell.IsInterrupt(execErr)
	exitCode := shell.ExitCode(execErr)

	stdout = truncateOutput(stdout)
	stderr = truncateOutput(stderr)

	errorMessage := stderr
	if errorMessage == "" && execErr != nil {
		errorMessage = execErr.Error()
	}

	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	hasBothOutputs := stdout != "" && stderr != ""

	if hasBothOutputs {
		stdout += "\n"
	}

	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}

	return stdout
}

// TruncateOutput keeps the first MaxOutputLength bytes of content and drops
// the tail. The head is where the load-bearing information lives (command
// banner, argv echo, first error stack frame); the tail is usually
// repetitive output the model can ignore. When callers need the full
// transcript, they should spill to disk via spillBashOutput first and view
// the resulting file directly.
func TruncateOutput(content string) string {
	if len(content) <= MaxOutputLength {
		return content
	}
	dropped := len(content) - MaxOutputLength
	droppedLines := countLines(content[MaxOutputLength:])
	return fmt.Sprintf("%s\n\n... [%d bytes / %d lines truncated from tail — view spill file for full output] ...\n", content[:MaxOutputLength], dropped, droppedLines)
}

func truncateOutput(content string) string {
	return TruncateOutput(content)
}

// headPreview returns the first BashPreviewBytes of content, padded with a
// truncation footer when the content was longer.
func headPreview(content string) string {
	if len(content) <= BashPreviewBytes {
		return content
	}
	dropped := len(content) - BashPreviewBytes
	return fmt.Sprintf("%s\n\n... [%d bytes truncated from tail — see <output_spill> for full output] ...\n", content[:BashPreviewBytes], dropped)
}

// maybeSpillOutput writes the full transcript to disk when combined output
// exceeds BashSpillThreshold. Delegates to the package-shared [Spiller]
// abstraction so view/rg/fetch can reuse the same pattern. Returns
// ("", 0) on no-op or write failure — failure is treated as no-op so a
// disk hiccup never loses the inline preview.
func maybeSpillOutput(dataDir, sessionID, callID, stdout, stderr string) (string, int) {
	spiller := NewSpiller(dataDir)
	res, ok := spiller.MaybeSpill(sessionID, callID, "bash", BashSpillThreshold,
		SpillPart{Content: stdout},
		SpillPart{Name: "stderr", Content: stderr},
	)
	if !ok {
		return "", 0
	}
	return res.Path, res.Bytes
}

func sanitizeCallID(id string) string {
	if id == "" {
		return "anon"
	}
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "anon"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// gcSpillDir walks tool-results/* and unlinks files older than maxAge. Best
// effort; errors are intentionally swallowed because GC failure must not
// fail the current bash command.
func gcSpillDir(root string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(p)
		}
		return nil
	})
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}

func appendBashCommandTrace(ctx context.Context, toolCallID string, params BashParams, metadata BashResponseMetadata, output string) {
	startedAt := time.UnixMilli(metadata.StartTime)
	finishedAt := time.UnixMilli(metadata.EndTime)
	success := metadata.Outcome == string(commandOutcomeSucceeded) ||
		metadata.Outcome == string(commandOutcomeNoMatch) ||
		metadata.Outcome == "background_started"
	kind := agentruntime.TraceKindCommandDone
	if !success {
		kind = agentruntime.TraceKindCommandFail
	}
	var exitCode *int
	if metadata.Outcome != "background_started" {
		exitCode = &metadata.ExitCode
	}
	AppendTraceFromContext(ctx, agentruntime.TaskTrace{
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		DurationMs:  metadata.DurationMs,
		Kind:        kind,
		Status:      metadata.Outcome,
		Success:     success,
		ToolName:    BashToolName,
		ToolCallID:  toolCallID,
		ToolInput:   params.Command,
		ToolOutput:  output,
		Command:     params.Command,
		WorkingDir:  metadata.WorkingDirectory,
		ExitCode:    exitCode,
		Outcome:     metadata.Outcome,
		StdoutBytes: metadata.StdoutBytes,
		StderrBytes: metadata.StderrBytes,
		ShellID:     metadata.ShellID,
		Output:      output,
		OutputBytes: len(output),
	})
}
