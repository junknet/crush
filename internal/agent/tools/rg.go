package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
)

type RgParams struct {
	Pattern     string `json:"pattern" description:"The regex pattern to search for in file contents or filenames"`
	Path        string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
	Include     string `json:"include,omitempty" description:"File pattern to include in the search (e.g. \"*.js\", \"*.{ts,tsx}\")"`
	LiteralText bool   `json:"literal_text,omitempty" description:"If true, the pattern will be treated as literal text with special regex characters escaped. Default is false."`
	FilesOnly   bool   `json:"files_only,omitempty" description:"If true, searches for filenames matching the pattern instead of file contents."`
}

type RgMatch struct {
	Path     string
	ModTime  time.Time
	LineNum  int
	CharNum  int
	LineText string
}

type RgResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

const (
	RgToolName        = "rg"
	maxRgContentWidth = 500
)

//go:embed rg.md.tpl
var rgDescriptionTmpl []byte

var rgDescriptionTpl = template.Must(
	template.New("rgDescription").
		Parse(string(rgDescriptionTmpl)),
)

func rgDescription() string {
	return renderTemplate(rgDescriptionTpl, struct{ MaxResults int }{MaxResults: 100})
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

func escapeRegexPattern(pattern string) string {
	specialChars := []string{"\\", ".", "+", "*", "?", "(", ")", "[", "]", "{", "}", "^", "$", "|"}
	escaped := pattern
	for _, char := range specialChars {
		escaped = strings.ReplaceAll(escaped, char, "\\"+char)
	}
	return escaped
}

func NewRgTool(permissions permission.Service, workingDir string, config config.ToolRg) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		RgToolName,
		rgDescription(),
		func(ctx context.Context, params RgParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPattern := params.Pattern
			if params.LiteralText {
				searchPattern = escapeRegexPattern(params.Pattern)
			}

			searchPath := cmp.Or(params.Path, workingDir)

			// Permission check
			sessionID := GetSessionFromContext(ctx)
			if sessionID != "" {
				action := "search"
				if params.FilesOnly {
					action = "list-files"
				}
				granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
					SessionID:   sessionID,
					ToolCallID:  call.ID,
					ToolName:    RgToolName,
					Action:      action,
					Path:        searchPath,
					Description: fmt.Sprintf("Search for %q in %s", searchPattern, searchPath),
					Params:      params,
				})
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !granted {
					return NewPermissionDeniedResponse(), nil
				}
			}

			searchCtx, cancel := context.WithTimeout(ctx, config.GetTimeout())
			defer cancel()

			var matches []RgMatch
			var truncated bool
			var err error

			if params.FilesOnly {
				matches, truncated, err = RgSearchFiles(searchCtx, searchPattern, searchPath, params.Include, 100)
			} else {
				matches, truncated, err = RgSearch(searchCtx, searchPattern, searchPath, params.Include, 100)
			}

			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
			}

			var output strings.Builder
			if len(matches) == 0 {
				output.WriteString("No files found")
			} else {
				if params.FilesOnly {
					fmt.Fprintf(&output, "Found %d matching files\n", len(matches))
					for _, match := range matches {
						fmt.Fprintf(&output, "%s\n", filepath.ToSlash(match.Path))
					}
				} else {
					fmt.Fprintf(&output, "Found %d matches\n", len(matches))
					currentFile := ""
					for _, match := range matches {
						if currentFile != match.Path {
							if currentFile != "" {
								output.WriteString("\n")
							}
							currentFile = match.Path
							fmt.Fprintf(&output, "%s:\n", filepath.ToSlash(match.Path))
						}
						lineText := match.LineText
						if len(lineText) > maxRgContentWidth {
							lineText = lineText[:maxRgContentWidth] + "..."
						}
						fmt.Fprintf(&output, "  Line %d, Char %d: %s\n", match.LineNum, match.CharNum, lineText)
					}
				}
				if truncated {
					output.WriteString("\n(Results are truncated. Use a more specific path or pattern.)")
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output.String()),
				RgResponseMetadata{NumberOfMatches: len(matches), Truncated: truncated},
			), nil
		},
	)
}

func RgSearch(ctx context.Context, pattern, path, include string, limit int) ([]RgMatch, bool, error) {
	rgPath := getRg()
	if rgPath == "" {
		return nil, false, fmt.Errorf("ripgrep (rg) not found in $PATH. Content search is unavailable")
	}

	args := []string{"--json", "-H", "-n", "-0", pattern}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, path)

	cmd := exec.CommandContext(ctx, rgPath, args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []RgMatch{}, false, nil
		}
		return nil, false, err
	}

	var matches []RgMatch
	modTimeCache := make(map[string]time.Time)
	for line := range bytes.SplitSeq(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(line) == 0 {
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
		if err := json.Unmarshal(line, &m); err != nil || m.Type != "match" {
			continue
		}

		filePath := m.Data.Path.Text
		mTime, ok := modTimeCache[filePath]
		if !ok {
			if fi, err := os.Stat(filePath); err == nil {
				mTime = fi.ModTime()
				modTimeCache[filePath] = mTime
			}
		}

		for _, sm := range m.Data.Submatches {
			matches = append(matches, RgMatch{
				Path:     filePath,
				ModTime:  mTime,
				LineNum:  m.Data.LineNumber,
				CharNum:  sm.Start + 1,
				LineText: strings.TrimSpace(m.Data.Lines.Text),
			})
			break // First match per line
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ModTime.After(matches[j].ModTime)
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	return matches, truncated, nil
}

func RgSearchFiles(ctx context.Context, pattern, path, include string, limit int) ([]RgMatch, bool, error) {
	rgPath := getRg()
	if rgPath == "" {
		return nil, false, fmt.Errorf("ripgrep (rg) not found in $PATH. Filename search is unavailable")
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, fmt.Errorf("invalid filename search pattern %q: %w", pattern, err)
	}

	args := []string{"--files", "--null"}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, path)

	cmd := exec.CommandContext(ctx, rgPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []RgMatch{}, false, nil
		}
		return nil, false, err
	}

	var matches []RgMatch
	for p := range bytes.SplitSeq(output, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		filePath := string(p)
		if !matcher.MatchString(filepath.ToSlash(filePath)) {
			continue
		}
		matches = append(matches, RgMatch{
			Path: filePath,
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
