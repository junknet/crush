package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/iodriver"
)

const (
	SearchToolName = "Search"
	// GrepToolName / FindToolName are habitual model vocabulary registered as
	// thin Search aliases (see newSearchVariant). The model emits these names
	// from a strong training prior no matter what the prompt says, so we accept
	// them instead of letting the agent loop reject with "tool not found".
	GrepToolName      = "Grep"
	FindToolName      = "Find"
	searchMatchLimit  = 100
	searchToolTimeout = 10 * time.Second
)

var defaultSearchExcludes = []string{
	"!**/.git/**",
	"!**/.repo/**",
	"!**/node_modules/**",
	"!**/vendor/**",
	"!**/dist/**",
	"!**/build/**",
	"!**/out/**",
	"!**/target/**",
	"!**/prebuilts/**",
}

type SearchParams struct {
	Mode       string `json:"mode" description:"Search mode: 'content' to search file contents, or 'files' to search for filenames."`
	Pattern    string `json:"pattern,omitempty" description:"The regex/literal pattern (for content search) or filename glob pattern (for files search)."`
	Path       string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
	Include    string `json:"include,omitempty" description:"Only search files matching this glob, e.g. \"*.go\" or \"*.{ts,tsx}\" (useful in content mode)."`
	IgnoreCase bool   `json:"ignore_case,omitempty" description:"Case-insensitive match (for content mode)."`
	Literal    bool   `json:"literal,omitempty" description:"Treat pattern as a fixed string, not regex (for content mode)."`
	FilesOnly  bool   `json:"files_only,omitempty" description:"List only the names of files that contain a match (for content mode)."`
	NoIgnore   bool   `json:"no_ignore,omitempty" description:"Include files excluded by .gitignore and hidden files."`
}

type SearchResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

func getRg() string {
	if path, err := exec.LookPath("rg"); err == nil {
		return path
	}
	EnsureEmbeddedToolsExist()
	if path, err := exec.LookPath("rg"); err == nil {
		return path
	}
	return ""
}

type runner func(ctx context.Context, argv []string, cwd string) ([]byte, int, error)

func resolveRunner(ctx context.Context) (run runner, cwd string, localRg string) {
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
		return run, cwd, ""
	}
	run = func(ctx context.Context, argv []string, cwd string) ([]byte, int, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		if cwd != "" {
			cmd.Dir = cwd
		}
		out, err := cmd.Output()
		return out, commandExit(err), localExecErr(err)
	}
	localRg = getRg()
	return run, "", localRg
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

func localExecErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return nil
	}
	return err
}

const grepDescription = `Search file contents (alias of Search mode="content"). Pattern is a regex (or literal with literal=true). Fast, .gitignore-aware, works locally and on attached remote hosts.`

const findDescription = `Find files by name (alias of Search mode="files"). Pattern is a filename glob, e.g. "*.go" or "**/*.{ts,tsx}". Fast, .gitignore-aware, works locally and on attached remote hosts.`

func NewSearchTool(workingDir string) fantasy.AgentTool {
	return newSearchVariant(SearchToolName, searchDescription, workingDir, "")
}

// NewGrepTool / NewFindTool register the model's habitual tool names as thin
// aliases of Search with the mode preset. The model emits "Grep"/"Find" from a
// strong training prior regardless of the prompt; registering them turns a
// "tool not found: Grep" dead turn into a working call. One Search
// implementation underneath — only the entry name and forced mode differ.
func NewGrepTool(workingDir string) fantasy.AgentTool {
	return newSearchVariant(GrepToolName, grepDescription, workingDir, "content")
}

func NewFindTool(workingDir string) fantasy.AgentTool {
	return newSearchVariant(FindToolName, findDescription, workingDir, "files")
}

func newSearchVariant(name, description, workingDir, forcedMode string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		name,
		description,
		func(ctx context.Context, params SearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			mode := strings.ToLower(strings.TrimSpace(params.Mode))
			if forcedMode != "" {
				mode = forcedMode // Grep/Find force their mode; any model-supplied mode is ignored
			}
			if mode != "content" && mode != "files" {
				return fantasy.NewTextErrorResponse("search: mode must be either 'content' or 'files'"), nil
			}

			run, root, localRg := resolveRunner(ctx)
			searchPath := resolveSearchPath(root, workingDir, params.Path)
			defaultPath := strings.TrimSpace(params.Path) == ""

			bin := localRg
			if bin == "" {
				bin = "rg" // fallback for remote
			}

			searchCtx, cancel := context.WithTimeout(ctx, searchToolTimeout)
			defer cancel()

			if mode == "content" {
				if params.Pattern == "" {
					return fantasy.NewTextErrorResponse("search: pattern is required for content mode"), nil
				}
				argv := []string{bin}
				if params.FilesOnly {
					argv = append(argv, "-l")
				} else {
					argv = append(argv, "--json", "-H", "-n")
				}
				if params.IgnoreCase {
					argv = append(argv, "-i")
				}
				if params.Literal {
					argv = append(argv, "-F")
				}
				if params.Include != "" {
					argv = append(argv, "--glob", sanitizeGlobInclude(params.Include))
				}
				if !params.NoIgnore {
					argv = appendDefaultSearchExcludes(argv)
				} else {
					argv = append(argv, "--no-ignore", "--hidden")
				}
				argv = append(argv, "--", params.Pattern, searchPath)

				stdout, code, err := run(searchCtx, argv, "")
				timedOut := searchCtx.Err() == context.DeadlineExceeded
				if err != nil && !timedOut {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("search content: %v", err)), nil
				}
				if code > 1 && !timedOut {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("search content: rg exited %d", code)), nil
				}

				var out string
				var n int
				if params.FilesOnly {
					files := splitNonEmptyLines(stdout)
					n = len(files)
					out = renderList(files)
				} else {
					lines, total := parseRgJSON(stdout)
					n = total
					out = renderLines(lines)
				}

				if defaultPath {
					out = strings.TrimSpace(out + "\n(searched the working directory because path was omitted; default excludes applied.)")
				}
				if timedOut {
					out = strings.TrimSpace(out + "\n(search timed out; partial results may be shown.)")
				}

				meta := SearchResponseMetadata{NumberOfMatches: n, Truncated: n > searchMatchLimit}
				if n == 0 {
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse("No matches found."), meta), nil
				}
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(out), meta), nil

			} else { // mode == "files"
				argv := []string{bin, "--files", "--null"}
				if params.Include != "" {
					argv = append(argv, "--glob", sanitizeGlobInclude(params.Include))
				}
				if !params.NoIgnore {
					argv = appendDefaultSearchExcludes(argv)
				} else {
					argv = append(argv, "--no-ignore", "--hidden")
				}
				argv = append(argv, searchPath)

				stdout, code, err := run(searchCtx, argv, "")
				timedOut := searchCtx.Err() == context.DeadlineExceeded
				if err != nil && !timedOut {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("search files: %v", err)), nil
				}
				if code > 1 && !timedOut {
					if code != 2 {
						return fantasy.NewTextErrorResponse(fmt.Sprintf("search files: rg exited %d", code)), nil
					}
				}

				var matcher *regexp.Regexp
				var resolvedPattern string
				if params.Pattern != "" {
					var err error
					resolvedPattern, err = resolveFilePattern(params.Pattern)
					if err != nil {
						return fantasy.NewTextErrorResponse(fmt.Sprintf("search files: %v", err)), nil
					}
					matcher, err = regexp.Compile(resolvedPattern)
					if err != nil {
						return fantasy.NewTextErrorResponse(fmt.Sprintf("search files: %v", err)), nil
					}
				}

				var matchedFiles []string
				for p := range bytes.SplitSeq(stdout, []byte{0}) {
					if len(p) == 0 {
						continue
					}
					filePath := string(p)
					slashPath := filepath.ToSlash(filePath)

					if matcher != nil {
						var matchTarget string
						if strings.Contains(resolvedPattern, "/") {
							rel, err := filepath.Rel(searchPath, filePath)
							if err != nil {
								rel = slashPath
							}
							matchTarget = filepath.ToSlash(rel)
						} else {
							matchTarget = filepath.Base(slashPath)
						}

						if !matcher.MatchString(matchTarget) {
							continue
						}
					}
					matchedFiles = append(matchedFiles, slashPath)
				}

				sort.SliceStable(matchedFiles, func(i, j int) bool {
					return len(matchedFiles[i]) < len(matchedFiles[j])
				})

				n := len(matchedFiles)
				truncated := n > searchMatchLimit
				if truncated {
					matchedFiles = matchedFiles[:searchMatchLimit]
				}

				out := strings.Join(matchedFiles, "\n")
				if defaultPath {
					out = strings.TrimSpace(out + "\n(searched the working directory because path was omitted; default excludes applied.)")
				}
				if timedOut {
					out = strings.TrimSpace(out + "\n(search timed out; partial results may be shown.)")
				}

				meta := SearchResponseMetadata{NumberOfMatches: n, Truncated: truncated}
				if n == 0 {
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse("No files found."), meta), nil
				}
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(out), meta), nil
			}
		},
	)
}

func sanitizeGlobInclude(include string) string {
	include = strings.TrimPrefix(include, "./")
	include = strings.TrimPrefix(include, "/")
	return include
}

func escapeRegexPattern(pattern string) string {
	specialChars := []string{"\\", ".", "+", "*", "?", "(", ")", "[", "]", "{", "}", "^", "$", "|"}
	escaped := pattern
	for _, char := range specialChars {
		escaped = strings.ReplaceAll(escaped, char, "\\"+char)
	}
	return escaped
}

func isGlobPattern(s string) bool {
	if !strings.ContainsAny(s, "*?") {
		return false
	}
	if strings.Contains(s, ".*") {
		return false
	}
	if strings.Contains(s, "+(){}|^$") {
		return false
	}
	if strings.Contains(s, `\`) {
		return false
	}
	return true
}

func globToRegex(glob string) string {
	var b strings.Builder
	b.WriteByte('^')
	i := 0
	for i < len(glob) {
		switch {
		case glob[i] == '*' && i+1 < len(glob) && glob[i+1] == '*':
			b.WriteString(".*")
			i += 2
			if i < len(glob) && glob[i] == '/' {
				i++
			}
		case glob[i] == '*':
			b.WriteString("[^/]*")
			i++
		case glob[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
			i++
		}
	}
	b.WriteByte('$')
	return b.String()
}

func resolveFilePattern(pattern string) (string, error) {
	if isGlobPattern(pattern) {
		return globToRegex(pattern), nil
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return "", fmt.Errorf("invalid file search pattern %q (not a valid glob or regex): %w", pattern, err)
	}
	return pattern, nil
}

func appendDefaultSearchExcludes(argv []string) []string {
	for _, glob := range defaultSearchExcludes {
		argv = append(argv, "--glob", glob)
	}
	return argv
}

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

func renderLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func renderList(items []string) string {
	sort.Strings(items)
	if len(items) > searchMatchLimit {
		items = items[:searchMatchLimit]
	}
	return strings.Join(items, "\n")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "."
}

func resolveSearchPath(root, workingDir, requested string) string {
	base := firstNonEmpty(root, workingDir, ".")
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return base
	}
	return filepathext.SmartJoin(base, requested)
}

const searchDescription = `Search file contents (mode="content") or find filenames (mode="files"). Fast, .gitignore-aware, works both locally and on attached remote hosts.

In content mode:
- pattern: regex pattern to search for.
- ignore_case: set true for case-insensitive search.
- literal: set true to treat pattern as literal text.
- files_only: set true to only list filenames with matches.
- include: glob pattern to restrict searched files (e.g. "*.go").

In files mode:
- pattern: glob pattern (e.g. "*.go") or regex to match filenames.

Global parameters:
- path: search directory path (defaults to current directory).
- no_ignore: set true to include hidden and gitignored files.`

type RgMatch struct {
	Path     string
	ModTime  time.Time
	LineNum  int
	CharNum  int
	LineText string
}

func RgSearch(ctx context.Context, pattern, path, include string, limit int) ([]RgMatch, bool, error) {
	run, root, localRg := resolveRunner(ctx)
	searchPath := resolveSearchPath(root, "", path)
	bin := localRg
	if bin == "" {
		bin = "rg"
	}
	argv := []string{bin, "--json", "-H", "-n", "-0", pattern}
	if include != "" {
		argv = append(argv, "--glob", sanitizeGlobInclude(include))
	}
	argv = appendDefaultSearchExcludes(argv)
	argv = append(argv, searchPath)

	stdout, code, err := run(ctx, argv, "")
	if err != nil {
		if code == 1 {
			return []RgMatch{}, false, nil
		}
		return nil, false, err
	}
	if code > 1 {
		return nil, false, fmt.Errorf("rg exited %d: %s", code, string(stdout))
	}

	var matches []RgMatch
	for _, raw := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		if raw == "" {
			continue
		}
		var m struct {
			Type string `json:"type"`
			Data struct {
				Path       struct{ Text string } `json:"path"`
				Lines      struct{ Text string } `json:"lines"`
				LineNumber int                   `json:"line_number"`
				Submatches []struct{ Start int } `json:"submatches"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &m); err != nil || m.Type != "match" {
			continue
		}

		charNum := 1
		if len(m.Data.Submatches) > 0 {
			charNum = m.Data.Submatches[0].Start + 1
		}

		matches = append(matches, RgMatch{
			Path:     m.Data.Path.Text,
			LineNum:  m.Data.LineNumber,
			CharNum:  charNum,
			LineText: strings.TrimRight(m.Data.Lines.Text, "\n"),
		})
	}

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	return matches, truncated, nil
}

func RgSearchFiles(ctx context.Context, pattern, path, include string, limit int) ([]RgMatch, bool, error) {
	run, root, localRg := resolveRunner(ctx)
	searchPath := resolveSearchPath(root, "", path)
	bin := localRg
	if bin == "" {
		bin = "rg"
	}
	argv := []string{bin, "--files", "--null"}
	if include != "" {
		argv = append(argv, "--glob", sanitizeGlobInclude(include))
	}
	argv = appendDefaultSearchExcludes(argv)
	argv = append(argv, searchPath)

	stdout, code, err := run(ctx, argv, "")
	if err != nil && code != 2 {
		return nil, false, err
	}

	var matcher *regexp.Regexp
	var resolvedPattern string
	if pattern != "" {
		var err error
		resolvedPattern, err = resolveFilePattern(pattern)
		if err != nil {
			return nil, false, err
		}
		matcher, err = regexp.Compile(resolvedPattern)
		if err != nil {
			return nil, false, err
		}
	}

	var matches []RgMatch
	for p := range bytes.SplitSeq(stdout, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		filePath := string(p)
		slashPath := filepath.ToSlash(filePath)

		if matcher != nil {
			var matchTarget string
			if strings.Contains(resolvedPattern, "/") {
				rel, err := filepath.Rel(searchPath, filePath)
				if err != nil {
					rel = slashPath
				}
				matchTarget = filepath.ToSlash(rel)
			} else {
				matchTarget = filepath.Base(slashPath)
			}

			if !matcher.MatchString(matchTarget) {
				continue
			}
		}
		matches = append(matches, RgMatch{
			Path: slashPath,
		})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return len(matches[i].Path) < len(matches[j].Path)
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	return matches, truncated, nil
}
