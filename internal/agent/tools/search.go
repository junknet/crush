package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/permission"
)

const SearchToolName = "search"

//go:embed search.md.tpl
var searchDescriptionTmpl []byte

var searchDescriptionTpl = template.Must(
	template.New("searchDescription").
		Parse(string(searchDescriptionTmpl)),
)

type searchDescriptionData struct {
	MaxResults int
}

func searchDescription() string {
	return renderTemplate(searchDescriptionTpl, searchDescriptionData{
		MaxResults: 100,
	})
}

type SearchParams struct {
	Pattern string `json:"pattern" description:"The search pattern to match files against"`
	Path    string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
}

type SearchResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

func NewSearchTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		SearchToolName,
		searchDescription(),
		func(ctx context.Context, params SearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPath := cmp.Or(params.Path, workingDir)

			files, truncated, err := searchFilesWithRipgrepFiles(ctx, params.Pattern, searchPath, 100)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error finding files: %v", err)), nil
			}

			var output string
			if len(files) == 0 {
				output = "No files found"
			} else {
				output = strings.Join(files, "\n")
				if truncated {
					output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output),
				SearchResponseMetadata{
					NumberOfFiles: len(files),
					Truncated:     truncated,
				},
			), nil
		},
	)
}

func searchFilesWithRipgrepFiles(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	rgPath := getRg()
	if rgPath == "" {
		return nil, false, fmt.Errorf("ripgrep (rg) not found in $PATH. File search is unavailable")
	}

	args := []string{"--files", "--null", "--hidden", "--glob", "!.git"}
	cmd := exec.CommandContext(ctx, rgPath, args...)
	cmd.Dir = searchPath

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			return nil, false, fmt.Errorf("rg --files error: %w", err)
		}
	}

	matches := filterSearchFilePaths(stdout.Bytes(), pattern)

	sort.SliceStable(matches, func(i, j int) bool {
		return len(matches[i]) < len(matches[j])
	})

	truncated := false
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
		truncated = true
	}

	for i, match := range matches {
		if !filepath.IsAbs(match) {
			matches[i] = filepathext.SmartJoin(searchPath, match)
		}
	}

	return matches, truncated, nil
}

func filterSearchFilePaths(output []byte, pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if len(output) == 0 || pattern == "" {
		return nil
	}

	var matcher func(string) bool
	if re, err := regexp.Compile(pattern); err == nil {
		matcher = re.MatchString
	} else {
		needle := strings.ToLower(pattern)
		matcher = func(path string) bool {
			return strings.Contains(strings.ToLower(path), needle)
		}
	}

	parts := bytes.Split(output, []byte{0})
	matches := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		path := filepath.ToSlash(string(part))
		if path == ".git" || strings.HasPrefix(path, ".git/") {
			continue
		}
		if matcher(path) || matcher(filepath.Base(path)) {
			matches = append(matches, path)
		}
	}

	return matches
}
