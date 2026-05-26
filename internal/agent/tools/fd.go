package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"os/exec"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/permission"
)

const FdToolName = "fd"

//go:embed fd.md.tpl
var fdDescriptionTmpl []byte

var fdDescriptionTpl = template.Must(
	template.New("fdDescription").
		Parse(string(fdDescriptionTmpl)),
)

func fdDescription() string {
	return renderTemplate(fdDescriptionTpl, struct{ MaxResults int }{MaxResults: 100})
}

type FdParams struct {
	Pattern string `json:"pattern" description:"The pattern to match filenames against"`
	Path    string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
}

type FdResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

func NewFdTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		FdToolName,
		fdDescription(),
		func(ctx context.Context, params FdParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPath := cmp.Or(params.Path, workingDir)

			// Permission check
			sessionID := GetSessionFromContext(ctx)
			if sessionID != "" {
				granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
					SessionID:   sessionID,
					ToolCallID:  call.ID,
					ToolName:    FdToolName,
					Action:      "find",
					Path:        searchPath,
					Description: fmt.Sprintf("Find files matching %q in %s", params.Pattern, searchPath),
					Params:      params,
				})
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !granted {
					return NewPermissionDeniedResponse(), nil
				}
			}

			files, truncated, err := runFdSearch(ctx, params.Pattern, searchPath, 100)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error finding files: %v", err)), nil
			}

			var output string
			if len(files) == 0 {
				output = "No files found"
			} else {
				output = strings.Join(files, "\n")
				if truncated {
					output += "\n\n(Results are truncated. Use a more specific path or pattern.)"
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output),
				FdResponseMetadata{NumberOfFiles: len(files), Truncated: truncated},
			), nil
		},
	)
}

func runFdSearch(ctx context.Context, pattern, searchRoot string, limit int) ([]string, bool, error) {
	// Try fd first, then fallback to rg --files
	cmd := exec.CommandContext(ctx, "fd", "-0", pattern, searchRoot)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback to rg --files
		cmd = exec.CommandContext(ctx, "rg", "--files", "--null", "--glob", "*"+pattern+"*", searchRoot)
		out, err = cmd.CombinedOutput()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("search failed: %w", err)
		}
	}

	var matches []string
	for p := range bytes.SplitSeq(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		path := string(p)
		if !strings.HasPrefix(path, "/") {
			path = filepathext.SmartJoin(searchRoot, path)
		}
		matches = append(matches, path)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return len(matches[i]) < len(matches[j])
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	return matches, truncated, nil
}
