package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/iodriver"
)

// This file implements the grep and find tools.
//
// Naming is deliberate: `grep` and `find` are the highest-frequency search
// verbs in LLM training data ("命名即 prompt"), so the model reaches for them
// correctly by reflex — unlike `rg`/`fd`, which are sparse in the corpus and
// invite mis-applied flags. The tools expose grep/find-shaped STRUCTURED
// params (no raw CLI), then transparently transpile to ripgrep / fd when those
// faster, .gitignore-aware tools exist on the target, falling back to real
// grep/find otherwise. The model gets familiar semantics; we get rg/fd speed.
//
// Everything runs through the active IO backend (iodriver.Execer) when a remote
// host is attached, so grep/find operate on the remote exactly as locally — and
// capability detection (does the remote have rg/fd?) is per-target.

const (
	// GrepToolName searches file CONTENTS.
	GrepToolName = "grep"
	// FindToolName searches for FILES by name.
	FindToolName = "find"

	searchMatchLimit = 100
)

// GrepParams is the grep-shaped content-search request.
type GrepParams struct {
	Pattern    string `json:"pattern" description:"Regex (or literal, with literal=true) to search for in file contents."`
	Path       string `json:"path,omitempty" description:"Directory to search. Defaults to the working directory."`
	Glob       string `json:"glob,omitempty" description:"Only search files matching this glob, e.g. \"*.go\" or \"*.{ts,tsx}\"."`
	IgnoreCase bool   `json:"ignore_case,omitempty" description:"Case-insensitive match (grep -i)."`
	Literal    bool   `json:"literal,omitempty" description:"Treat pattern as a fixed string, not regex (grep -F)."`
	FilesOnly  bool   `json:"files_only,omitempty" description:"List only the names of files that contain a match (grep -l)."`
	NoIgnore   bool   `json:"no_ignore,omitempty" description:"Also search files normally excluded by .gitignore and hidden files. Off by default to skip build/vendor noise."`
}

// FindParams is the find-shaped file-search request.
type FindParams struct {
	Name     string `json:"name,omitempty" description:"Glob to match file/dir names, e.g. \"*.go\" or \"Dockerfile\". Empty lists everything."`
	Path     string `json:"path,omitempty" description:"Directory to search. Defaults to the working directory."`
	Type     string `json:"type,omitempty" description:"Restrict to \"f\" (files) or \"d\" (directories). Empty matches both."`
	MaxDepth int    `json:"max_depth,omitempty" description:"Maximum directory depth to descend. 0 means unlimited."`
	NoIgnore bool   `json:"no_ignore,omitempty" description:"Also include files excluded by .gitignore and hidden files."`
}

// searchCaps records, per target, whether the fast tools are present.
type searchCaps struct {
	hasRg bool
	hasFd bool
	fdBin string // "fd" or "fdfind"
}

var (
	capsMu    sync.Mutex
	capsCache = map[string]searchCaps{} // keyed by backend kind ("local"/"remote:host")
)

// runner abstracts running a program argv against the active target (local
// process or the attached remote daemon) and returns stdout, exit code, err.
type runner func(ctx context.Context, argv []string, cwd string) ([]byte, int, error)

// resolveRunner returns the target's runner, working dir, and fast-tool caps.
// Local uses the bundled rg (getRg) and the local PATH; remote probes the
// daemon once and caches.
func resolveRunner(ctx context.Context) (run runner, cwd string, caps searchCaps, localRg string) {
	backend := GetBackendFromContext(ctx)
	if ex, ok := backend.(iodriver.Execer); ok {
		cwd = backend.Root()
		run = func(ctx context.Context, argv []string, cwd string) ([]byte, int, error) {
			res, err := ex.Exec(ctx, iodriver.ExecRequest{Argv: argv, Cwd: cwd})
			if err != nil {
				return nil, -1, err
			}
			return res.Stdout, res.ExitCode, nil
		}
		caps = detectCaps(ctx, backend.Kind(), run)
		return run, cwd, caps, ""
	}
	// Local: bundled rg is always available; detect fd on PATH.
	run = func(ctx context.Context, argv []string, cwd string) ([]byte, int, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		if cwd != "" {
			cmd.Dir = cwd
		}
		out, err := cmd.Output()
		return out, commandExit(err), localExecErr(err)
	}
	localRg = getRg()
	caps = detectCaps(ctx, "local", run)
	caps.hasRg = localRg != "" // bundled rg
	return run, "", caps, localRg
}

func commandExit(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// localExecErr discards non-zero-exit errors (handled via the exit code) but
// surfaces real failures (binary missing, etc.).
func localExecErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return nil
	}
	return err
}

// detectCaps probes rg/fd availability on the target once, then caches.
func detectCaps(ctx context.Context, key string, run runner) searchCaps {
	capsMu.Lock()
	defer capsMu.Unlock()
	if c, ok := capsCache[key]; ok {
		return c
	}
	c := searchCaps{}
	if _, code, err := run(ctx, []string{"rg", "--version"}, ""); err == nil && code == 0 {
		c.hasRg = true
	}
	for _, name := range []string{"fd", "fdfind"} {
		if _, code, err := run(ctx, []string{name, "--version"}, ""); err == nil && code == 0 {
			c.hasFd = true
			c.fdBin = name
			break
		}
	}
	capsCache[key] = c
	return c
}

// NewGrepTool returns the content-search tool, backed by rg when available.
func NewGrepTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		GrepToolName,
		grepDescription,
		func(ctx context.Context, params GrepParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("grep: missing pattern"), nil
			}
			run, root, caps, localRg := resolveRunner(ctx)
			searchPath := firstNonEmpty(params.Path, root, workingDir, ".")

			argv, parseRg := grepArgv(params, caps.hasRg, localRg, searchPath)
			stdout, code, err := run(ctx, argv, "")
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("grep: %v", err)), nil
			}
			// rg/grep exit 1 == no matches (not an error).
			if code > 1 {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("grep: search exited %d", code)), nil
			}

			var out string
			var n int
			if params.FilesOnly {
				files := splitNonEmptyLines(stdout)
				n = len(files)
				out = renderList(files, "file")
			} else if parseRg {
				lines, total := parseRgJSON(stdout)
				n = total
				out = renderLines(lines)
			} else {
				lines := splitNonEmptyLines(stdout)
				n = len(lines)
				out = renderLines(truncate(lines))
			}
			meta := RgResponseMetadata{NumberOfMatches: n, Truncated: n > searchMatchLimit}
			if n == 0 {
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse("No matches found."), meta), nil
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(out), meta), nil
		},
	)
}

// NewFindTool returns the file-search tool, backed by fd when available.
func NewFindTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		FindToolName,
		findDescription,
		func(ctx context.Context, params FindParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			run, root, caps, _ := resolveRunner(ctx)
			searchPath := firstNonEmpty(params.Path, root, workingDir, ".")

			argv := findArgv(params, caps.hasFd, caps.fdBin, searchPath)
			stdout, code, err := run(ctx, argv, "")
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("find: %v", err)), nil
			}
			if code > 1 {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("find: exited %d", code)), nil
			}
			files := truncate(splitNonEmptyLines(stdout))
			meta := RgResponseMetadata{NumberOfMatches: len(files), Truncated: len(files) >= searchMatchLimit}
			if len(files) == 0 {
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse("No files found."), meta), nil
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(renderList(files, "file")), meta), nil
		},
	)
}

// grepArgv transpiles GrepParams to ripgrep argv (parseRg=true) or grep argv.
func grepArgv(p GrepParams, hasRg bool, localRg, searchPath string) (argv []string, parseRg bool) {
	if hasRg {
		bin := localRg
		if bin == "" {
			bin = "rg"
		}
		argv = []string{bin}
		if p.FilesOnly {
			argv = append(argv, "-l")
		} else {
			argv = append(argv, "--json", "-H", "-n")
		}
		if p.IgnoreCase {
			argv = append(argv, "-i")
		}
		if p.Literal {
			argv = append(argv, "-F")
		}
		if p.Glob != "" {
			argv = append(argv, "--glob", p.Glob)
		}
		if p.NoIgnore {
			argv = append(argv, "--no-ignore", "--hidden")
		}
		argv = append(argv, "--", p.Pattern, searchPath)
		return argv, !p.FilesOnly
	}
	// Fallback: real grep (recursive, line-numbered).
	argv = []string{"grep", "-rn", "--color=never"}
	if p.FilesOnly {
		argv = []string{"grep", "-rl", "--color=never"}
	}
	if p.IgnoreCase {
		argv = append(argv, "-i")
	}
	if p.Literal {
		argv = append(argv, "-F")
	}
	if p.Glob != "" {
		argv = append(argv, "--include="+p.Glob)
	}
	argv = append(argv, "-e", p.Pattern, searchPath)
	return argv, false
}

// findArgv transpiles FindParams to fd argv or find argv.
func findArgv(p FindParams, hasFd bool, fdBin, searchPath string) []string {
	if hasFd {
		bin := fdBin
		if bin == "" {
			bin = "fd"
		}
		argv := []string{bin, "--color", "never"}
		if p.Type == "f" {
			argv = append(argv, "-t", "f")
		} else if p.Type == "d" {
			argv = append(argv, "-t", "d")
		}
		if p.MaxDepth > 0 {
			argv = append(argv, "-d", strconv.Itoa(p.MaxDepth))
		}
		if p.NoIgnore {
			argv = append(argv, "--no-ignore", "--hidden")
		}
		if p.Name != "" {
			argv = append(argv, "-g", p.Name)
		}
		argv = append(argv, ".", searchPath)
		return argv
	}
	// Fallback: real find.
	argv := []string{"find", searchPath}
	if p.MaxDepth > 0 {
		argv = append(argv, "-maxdepth", strconv.Itoa(p.MaxDepth))
	}
	if p.Type == "f" {
		argv = append(argv, "-type", "f")
	} else if p.Type == "d" {
		argv = append(argv, "-type", "d")
	}
	if p.Name != "" {
		argv = append(argv, "-name", p.Name)
	}
	return argv
}

// parseRgJSON parses ripgrep --json match events into "path:line: text" lines,
// returning the rendered slice (capped) and the total match count.
func parseRgJSON(stdout []byte) (lines []string, total int) {
	for _, raw := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		if raw == "" {
			continue
		}
		var m struct {
			Type string `json:"type"`
			Data struct {
				Path  struct{ Text string } `json:"path"`
				Lines struct{ Text string } `json:"lines"`
				Num   int                   `json:"line_number"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &m); err != nil || m.Type != "match" {
			continue
		}
		total++
		if len(lines) < searchMatchLimit {
			lines = append(lines, fmt.Sprintf("%s:%d: %s", m.Data.Path.Text, m.Data.Num, strings.TrimRight(m.Data.Lines.Text, "\n")))
		}
	}
	return lines, total
}

func splitNonEmptyLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func truncate(lines []string) []string {
	if len(lines) > searchMatchLimit {
		return lines[:searchMatchLimit]
	}
	return lines
}

func renderLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func renderList(items []string, _ string) string {
	sort.Strings(items)
	return strings.Join(truncate(items), "\n")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "."
}

const grepDescription = `Search file CONTENTS for a regex (or literal) pattern. Backed by ripgrep when available (fast, .gitignore-aware) and plain grep otherwise — you don't choose; just give the pattern.

By default it skips files ignored by .gitignore and hidden files (build/vendor/node_modules noise); set no_ignore=true to search everything. Use glob to scope by file type ("*.go"), files_only to get just matching filenames, literal=true to match a fixed string. When attached to a remote host (remote_attach), it searches the remote filesystem.`

const findDescription = `Find FILES and directories by name. Backed by fd when available (fast, .gitignore-aware) and plain find otherwise.

Give a name glob ("*.go", "Dockerfile"); empty lists everything under path. type="f"/"d" restricts to files/dirs; max_depth limits descent; no_ignore includes .gitignore'd and hidden entries. When attached to a remote host, it searches the remote filesystem.`
