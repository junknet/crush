package tools

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/iodriver"
	"github.com/charmbracelet/crush/internal/permission"
	agentruntime "github.com/charmbracelet/crush/internal/runtime"
	"github.com/charmbracelet/crush/internal/shell"
)

type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to execute"`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to current directory)"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in the background. Use job_output to read the output later."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait before automatically moving the command to a background job (default: 5)"`
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
	BashToolName = "Bash"

	DefaultAutoBackgroundAfter = 5 // Commands taking longer automatically become background jobs
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

var foregroundSleepPattern = regexp.MustCompile(`(?is)^\s*sleep\s+([0-9]+(\.[0-9]+)?)([smh]?)\s*(&&|;|$)`)

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
	"sftp",
	"ssh",
	"sshfs",
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
	"watch",
}

var agentToolOnlyShellCommands = []string{
	"find",
	"scp",
	"sftp",
	"ssh",
	"sshfs",
}

func bashRewriteFuncs() []shell.RewriteFunc {
	rgPath := getRg()
	if rgPath == "" {
		return nil
	}
	return []shell.RewriteFunc{grepToRgRewriteFunc(rgPath)}
}

func grepToRgRewriteFunc(rgPath string) shell.RewriteFunc {
	return func(args []string) []string {
		rewritten, ok := rewriteGrepArgsToRg(args, rgPath)
		if !ok {
			return nil
		}
		return rewritten
	}
}

func rewriteGrepArgsToRg(args []string, rgPath string) ([]string, bool) {
	if len(args) == 0 || rgPath == "" {
		return nil, false
	}

	name := filepath.Base(strings.TrimSuffix(args[0], string(os.PathSeparator)))
	fixedPattern := name == "fgrep"
	if name != "grep" && name != "egrep" && !fixedPattern {
		return nil, false
	}

	rgArgs := []string{rgPath, "--hidden", "--no-ignore", "--color=never", "-H", "-n"}
	if fixedPattern {
		rgArgs = append(rgArgs, "-F")
	}
	var patterns []string
	var operands []string
	afterDashDash := false

	for i := 1; i < len(args); i++ {
		arg := args[i]
		if afterDashDash {
			operands = append(operands, arg)
			continue
		}
		if arg == "--" {
			afterDashDash = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			operands = append(operands, arg)
			continue
		}
		if strings.HasPrefix(arg, "--include=") {
			rgArgs = append(rgArgs, "--glob", strings.TrimPrefix(arg, "--include="))
			continue
		}
		if strings.HasPrefix(arg, "--exclude=") {
			rgArgs = append(rgArgs, "--glob", "!"+strings.TrimPrefix(arg, "--exclude="))
			continue
		}
		if strings.HasPrefix(arg, "--color=") {
			rgArgs = append(rgArgs, arg)
			continue
		}

		short := strings.TrimPrefix(arg, "-")
		for j := 0; j < len(short); j++ {
			ch := short[j]
			rest := short[j+1:]
			switch ch {
			case 'E', 'I', 'r', 'R':
				// rg already uses regexes, skips binary blobs by default, and
				// recurses when a directory operand is present.
			case 'F':
				rgArgs = append(rgArgs, "-F")
			case 'i', 'l', 'L', 'q', 'v', 'o', 'w', 'x':
				rgArgs = append(rgArgs, "-"+string(ch))
			case 'H', 'n':
				// Already forced above for stable grep-like output.
			case 'h':
				rgArgs = append(rgArgs, "--no-heading")
			case 'c':
				rgArgs = append(rgArgs, "--count")
			case 'P':
				rgArgs = append(rgArgs, "-P")
			case 'A', 'B', 'C', 'm':
				value := rest
				if value == "" {
					if i+1 >= len(args) {
						return nil, false
					}
					i++
					value = args[i]
				}
				rgArgs = append(rgArgs, "-"+string(ch), value)
				j = len(short)
			case 'e':
				value := rest
				if value == "" {
					if i+1 >= len(args) {
						return nil, false
					}
					i++
					value = args[i]
				}
				patterns = append(patterns, value)
				j = len(short)
			default:
				return nil, false
			}
		}
	}

	if len(patterns) == 0 {
		if len(operands) == 0 {
			return nil, false
		}
		patterns = append(patterns, operands[0])
		operands = operands[1:]
	}

	if len(patterns) == 1 {
		rgArgs = append(rgArgs, "--", patterns[0])
	} else {
		for _, pattern := range patterns {
			rgArgs = append(rgArgs, "-e", pattern)
		}
		rgArgs = append(rgArgs, "--")
	}
	rgArgs = append(rgArgs, operands...)
	return rgArgs, true
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
		shell.CommandBasenameBlocker(agentToolOnlyShellCommands),
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

		// Sleep polling is rejected before execution so short shell timing
		// tests can still use sleep without broad command blocking. Other
		// long-running commands are handled by foreground-budget auto-background.
	}
}

func blockForegroundSleep(command string) (string, bool) {
	matches := foregroundSleepPattern.FindStringSubmatch(command)
	if len(matches) == 0 {
		return "", false
	}

	amount, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return "", false
	}

	switch strings.ToLower(matches[3]) {
	case "m":
		amount *= 60
	case "h":
		amount *= 60 * 60
	}

	if time.Duration(amount*float64(time.Second)) < 2*time.Second {
		return "", false
	}

	return "foreground sleep polling is blocked: do not run `sleep N && ...` or long `sleep` in bash. Start the waiting command with run_in_background=true and use the monitor tool to wake on completion/error, or use schedule_wakeup for a pure time delay.", true
}

func blockedShellCommandMessage(command string) string {
	for _, segment := range splitShellCommandSegments(command) {
		args := strings.Fields(segment)
		if len(args) == 0 {
			continue
		}
		for _, blockFunc := range blockFuncs() {
			if blockFunc(args) {
				return fmt.Sprintf("command is not allowed for security reasons: %q", args[0])
			}
		}
	}
	return ""
}

func splitShellCommandSegments(command string) []string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	segments := make([]string, 0, 1)
	current := make([]string, 0, len(fields))
	for _, field := range fields {
		switch field {
		case "|", "||", "&&", ";":
			if len(current) > 0 {
				segments = append(segments, strings.Join(current, " "))
				current = current[:0]
			}
		default:
			current = append(current, field)
		}
	}
	if len(current) > 0 {
		segments = append(segments, strings.Join(current, " "))
	}
	return segments
}

type BashTool struct {
	permissions  permission.Service
	bgManager    *shell.BackgroundShellManager
	workingDir   string
	dataDir      string
	attribution  *config.Attribution
	modelID      string
	registry     map[string]fantasy.AgentTool
	providerOpts fantasy.ProviderOptions
	mu           sync.Mutex
}

func (b *BashTool) SetRegistry(registry map[string]fantasy.AgentTool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.registry = registry
}

func (b *BashTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        BashToolName,
		Description: string(bashDescription(b.attribution, b.modelID)),
	}
}

func (b *BashTool) ProviderOptions() fantasy.ProviderOptions {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.providerOpts
}

func (b *BashTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.providerOpts = opts
}

func (b *BashTool) Execute(ctx context.Context, toolName string, inputJSON string) (string, bool, error) {
	b.mu.Lock()
	registry := b.registry
	b.mu.Unlock()

	var actualTool fantasy.AgentTool
	if registry != nil {
		actualTool = registry[toolName]
		if actualTool == nil {
			for name, t := range registry {
				if strings.EqualFold(name, toolName) {
					actualTool = t
					break
				}
			}
		}
	}

	if actualTool == nil {
		return "", false, fmt.Errorf("tool %s not registered", toolName)
	}

	resp, err := actualTool.Run(ctx, fantasy.ToolCall{
		ID:    "shell-transparent-route",
		Name:  toolName,
		Input: inputJSON,
	})
	if err != nil {
		return "", resp.IsError, err
	}
	return resp.Content, resp.IsError, nil
}

func NewBashTool(permissions permission.Service, bgManager *shell.BackgroundShellManager, workingDir, dataDir string, attribution *config.Attribution, modelID string) fantasy.AgentTool {
	return &BashTool{
		permissions: permissions,
		bgManager:   bgManager,
		workingDir:  workingDir,
		dataDir:     dataDir,
		attribution: attribution,
		modelID:     modelID,
	}
}

func (b *BashTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params BashParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.ToolResponse{}, err
	}

	if params.Command == "" {
		return fantasy.NewTextErrorResponse("missing command"), nil
	}
	if message, blocked := blockForegroundSleep(params.Command); blocked {
		return fantasy.NewTextErrorResponse(message), nil
	}

	var args []string
	if parsedArgs, ok := shell.ParseCommandLine(params.Command); ok {
		args = parsedArgs
	} else {
		args = strings.Fields(params.Command)
	}

	// 裸命令透明拦截（控制端前置）
	if toolName, inputJSON, ok := shell.MatchAndRouteCommand(args); ok {
		slog.Info("Bash transparent route match", "cmd", params.Command, "tool", toolName)
		output, isErr, err := b.Execute(ctx, toolName, inputJSON)

		startTime := time.Now()
		execWorkingDir := cmp.Or(params.WorkingDir, b.workingDir)
		if backend := GetBackendFromContext(ctx); backend != nil {
			execWorkingDir = cmp.Or(params.WorkingDir, backend.Root())
		}

		metadata := BashResponseMetadata{
			StartTime:        startTime.UnixMilli(),
			EndTime:          time.Now().UnixMilli(),
			DurationMs:       time.Since(startTime).Milliseconds(),
			Output:           output,
			Description:      params.Description,
			WorkingDirectory: execWorkingDir,
			ExitCode:         0,
			Outcome:          "passed",
			StdoutBytes:      len(output),
		}
		if isErr || err != nil {
			metadata.ExitCode = 1
			metadata.Outcome = "failed"
			errMsg := output
			if err != nil {
				errMsg = err.Error()
			}
			metadata.StderrBytes = len(errMsg)
			appendBashCommandTrace(ctx, call.ID, params, metadata, errMsg)
			return fantasy.WithResponseMetadata(fantasy.NewTextErrorResponse(errMsg), metadata), nil
		}

		// 特判 grep 找不到匹配的情况，模拟 grep 的返回状态
		isGrepCmd := false
		if len(args) > 0 {
			cmdBase := filepath.Base(args[0])
			if cmdBase == "grep" || cmdBase == "rg" || cmdBase == "ag" || cmdBase == "ack" {
				isGrepCmd = true
			}
		}

		if isGrepCmd && output == "No matches found." {
			metadata.ExitCode = 1
			metadata.Outcome = "no_match"
			appendBashCommandTrace(ctx, call.ID, params, metadata, output)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(output), metadata), nil
		}

		appendBashCommandTrace(ctx, call.ID, params, metadata, output)
		if output == "" {
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
		}
		output += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(execWorkingDir))
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(output), metadata), nil
	}

	// Determine working directory
	execWorkingDir := cmp.Or(params.WorkingDir, b.workingDir)

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
		p, err := b.permissions.Request(
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

	// When a remote backend is attached to this session, run the command
	// on the remote host (run-to-completion) instead of the local shell.
	// Only RemoteBackend implements Execer; the local path is unchanged.
	if backend := GetBackendFromContext(ctx); backend != nil {
		if jobber, ok := backend.(iodriver.Jobber); ok {
			return runRemoteBash(ctx, jobber, backend, params, call, b.dataDir, sessionID)
		}
	}

	// If explicitly requested as background, start immediately with detached context
	if params.RunInBackground {
		startTime := time.Now()
		b.bgManager.Cleanup()
		// Use background context so it continues after tool returns
		bgShell, err := b.bgManager.StartWithRewriters(shell.WithToolExecutor(context.Background(), b), execWorkingDir, blockFuncs(), bashRewriteFuncs(), params.Command, params.Description, sessionID)
		if err != nil {
			return fantasy.ToolResponse{}, fmt.Errorf("error starting background shell: %w", err)
		}

		// Wait a short time to detect fast failures (blocked commands, syntax errors, etc.)
		time.Sleep(1 * time.Second)
		stdout, stderr, done, execErr := bgShell.GetOutput()

		if done {
			// Command failed or completed very quickly
			b.bgManager.Remove(bgShell.ID)

			interrupted := shell.IsInterrupt(execErr)
			exitCode := shell.ExitCode(execErr)
			if exitCode == 0 && !interrupted && execErr != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
			}

			semantics := deriveCommandSemantics(params.Command, stdout, execErr)
			stdoutBytes := len(stdout)
			stderrBytes := len(stderr)
			spillPath, spillBytes := maybeSpillOutput(b.dataDir, sessionID, call.ID, stdout, stderr)
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
	b.bgManager.Cleanup()
	bgShell, err := b.bgManager.StartWithRewriters(shell.WithToolExecutor(context.Background(), b), execWorkingDir, blockFuncs(), bashRewriteFuncs(), params.Command, params.Description, sessionID)
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
			b.bgManager.Kill(bgShell.ID)
			return fantasy.ToolResponse{}, ctx.Err()
		}
	}

	if done {
		// Command completed within threshold - return synchronously
		// Remove from background manager since we're returning directly
		// Don't call Kill() as it cancels the context and corrupts the exit code
		b.bgManager.Remove(bgShell.ID)

		interrupted := shell.IsInterrupt(execErr)
		exitCode := shell.ExitCode(execErr)
		if exitCode == 0 && !interrupted && execErr != nil {
			return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
		}

		semantics := deriveCommandSemantics(params.Command, stdout, execErr)
		stdoutBytes := len(stdout)
		stderrBytes := len(stderr)
		spillPath, spillBytes := maybeSpillOutput(b.dataDir, sessionID, call.ID, stdout, stderr)
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
	response := fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground shell ID: <shell_id>%s</shell_id>\n\nIt will keep running; you'll be automatically notified to continue when it finishes. You can also use job_output to check on it or job_kill to terminate.", bgShell.ID)
	appendBashCommandTrace(ctx, call.ID, params, metadata, response)
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
}

// runRemoteBash executes the command on the attached remote host (run to
// completion) and formats the result identically to the local synchronous
// path, so the model sees the same shape whether local or remote. Remote
// execution starts as a job, then either completes inside the short foreground
// budget or is handed back as a background shell id for job_output/monitor.
func runRemoteBash(ctx context.Context, jobber iodriver.Jobber, backend iodriver.Backend, params BashParams, call fantasy.ToolCall, dataDir, sessionID string) (fantasy.ToolResponse, error) {
	startTime := time.Now()
	remoteCwd := cmp.Or(params.WorkingDir, backend.Root())

	if message := blockedShellCommandMessage(params.Command); message != "" {
		endTime := time.Now()
		metadata := BashResponseMetadata{
			StartTime:        startTime.UnixMilli(),
			EndTime:          endTime.UnixMilli(),
			DurationMs:       endTime.Sub(startTime).Milliseconds(),
			Output:           message,
			Description:      params.Description,
			WorkingDirectory: remoteCwd,
			ExitCode:         126,
			Outcome:          "blocked",
			StdoutBytes:      0,
			StderrBytes:      len(message),
		}
		appendBashCommandTrace(ctx, call.ID, params, metadata, message)
		return fantasy.WithResponseMetadata(fantasy.NewTextErrorResponse(message), metadata), nil
	}

	snapshot, err := jobber.StartJob(ctx, iodriver.JobRequest{
		ExecRequest: iodriver.ExecRequest{Command: params.Command, Cwd: remoteCwd},
		Description: params.Description,
		SessionID:   sessionID,
	})
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("remote job start on %s: %w", backend.Kind(), err)
	}

	waitBudget := time.Duration(cmp.Or(params.AutoBackgroundAfter, DefaultAutoBackgroundAfter)) * time.Second
	if params.RunInBackground {
		waitBudget = time.Second
	}

	deadline := time.NewTimer(waitBudget)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
waitLoop:
	for !snapshot.Done {
		select {
		case <-ticker.C:
			if next, err := jobber.JobOutput(ctx, snapshot.ID); err == nil {
				snapshot = next
			}
		case <-deadline.C:
			break waitLoop
		case <-ctx.Done():
			return fantasy.ToolResponse{}, ctx.Err()
		}
	}

	if !snapshot.Done {
		go watchRemoteBackgroundJob(context.Background(), jobber, snapshot.ID, sessionID, params.Command, params.Description)

		endTime := time.Now()
		metadata := BashResponseMetadata{
			StartTime:        startTime.UnixMilli(),
			EndTime:          endTime.UnixMilli(),
			DurationMs:       endTime.Sub(startTime).Milliseconds(),
			Description:      params.Description,
			WorkingDirectory: remoteCwd,
			Background:       true,
			ShellID:          snapshot.ID,
			Outcome:          "background_started",
			StdoutBytes:      len(snapshot.Stdout),
			StderrBytes:      len(snapshot.Stderr),
		}
		response := fmt.Sprintf("Command is still running after %s and has been moved to background.\n\nBackground shell ID: <shell_id>%s</shell_id>\n\nIt will keep running on %s. Use job_output to check output, job_kill to terminate it, or monitor to wake on a matching output line.", waitBudget, snapshot.ID, backend.Kind())
		appendBashCommandTrace(ctx, call.ID, params, metadata, response)
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
	}

	stdout := string(snapshot.Stdout)
	stderr := string(snapshot.Stderr)
	execErr := shell.SyntheticExitError(snapshot.ExitCode)
	semantics := deriveCommandSemantics(params.Command, stdout, execErr)
	stdoutBytes := len(stdout)
	stderrBytes := len(stderr)
	spillPath, spillBytes := maybeSpillOutput(dataDir, sessionID, call.ID, stdout, stderr)
	formatted := formatOutput(stdout, stderr, execErr)
	if spillPath != "" {
		formatted += fmt.Sprintf("\n<output_spill bytes=\"%d\" path=\"%s\">Full transcript written to disk; use the view tool on this path for the complete output.</output_spill>", spillBytes, spillPath)
	}
	endTime := time.Now()

	metadata := BashResponseMetadata{
		StartTime:        startTime.UnixMilli(),
		EndTime:          endTime.UnixMilli(),
		DurationMs:       endTime.Sub(startTime).Milliseconds(),
		Output:           formatted,
		Description:      params.Description,
		WorkingDirectory: remoteCwd,
		ExitCode:         semantics.ExitCode,
		Outcome:          string(semantics.Outcome),
		StdoutBytes:      stdoutBytes,
		StderrBytes:      stderrBytes,
		SpillPath:        spillPath,
		SpillBytes:       spillBytes,
	}
	appendBashCommandTrace(ctx, call.ID, params, metadata, formatted)
	if formatted == "" {
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
	}
	formatted += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(remoteCwd))
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(formatted), metadata), nil
}

func watchRemoteBackgroundJob(ctx context.Context, jobber iodriver.Jobber, shellID, sessionID, command, description string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			snapshot, err := jobber.JobOutput(ctx, shellID)
			if err != nil {
				return
			}
			if !snapshot.Done {
				continue
			}
			time.Sleep(750 * time.Millisecond)
			if hasRemoteMonitor(shellID) {
				return
			}
			output := backgroundTail(string(snapshot.Stdout), string(snapshot.Stderr))
			shell.PublishBackgroundDone(shell.BackgroundJobEvent{
				Kind:        shell.BackgroundKindDone,
				ID:          shellID,
				SessionID:   sessionID,
				Command:     command,
				Description: description,
				ExitCode:    snapshot.ExitCode,
				OutputTail:  output,
			})
			return
		case <-ctx.Done():
			return
		}
	}
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

func backgroundTail(stdout, stderr string) string {
	combined := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(stdout),
		strings.TrimSpace(stderr),
	}, "\n"))
	if len(combined) <= 4096 {
		return combined
	}
	return combined[len(combined)-4096:]
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
