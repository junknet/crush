package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
)

type AstGrepParams struct {
	Pattern string `json:"pattern" description:"The AST pattern to search for (e.g., \"fmt.Println($MSG)\")"`
	Path    string `json:"path,omitempty" description:"The directory or file to search in. Defaults to the current working directory."`
	Rewrite string `json:"rewrite,omitempty" description:"An AST pattern to rewrite the matches to. If provided, the tool will modify the files."`
	Lang    string `json:"lang,omitempty" description:"The language of the pattern (e.g., \"go\", \"javascript\", \"python\", \"rust\")"`
}

const AstGrepToolName = "ast_grep"

//go:embed ast_grep.md.tpl
var astGrepDescriptionTmpl []byte

var astGrepDescriptionTpl = template.Must(
	template.New("astGrepDescription").
		Parse(string(astGrepDescriptionTmpl)),
)

func astGrepDescription() string {
	return renderTemplate(astGrepDescriptionTpl, nil)
}

func NewAstGrepTool(permissions permission.Service, workingDir string, config config.ToolAstGrep) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		AstGrepToolName,
		astGrepDescription(),
		func(ctx context.Context, params AstGrepParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPath := params.Path
			if searchPath == "" {
				searchPath = workingDir
			}
			if !filepath.IsAbs(searchPath) {
				searchPath = filepath.Join(workingDir, searchPath)
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required")
			}

			if params.Rewrite != "" {
				granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
					SessionID:   sessionID,
					ToolCallID:  call.ID,
					ToolName:    AstGrepToolName,
					Description: fmt.Sprintf("Apply AST-based rewrites to %s", searchPath),
					Action:      "write",
					Params:      params,
					Path:        searchPath,
				})
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !granted {
					return NewPermissionDeniedResponse(), nil
				}
				return runAstGrepRewrite(ctx, params, searchPath, config)
			}

			granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				ToolCallID:  call.ID,
				ToolName:    AstGrepToolName,
				Description: fmt.Sprintf("Search files using AST pattern in %s", searchPath),
				Action:      "read",
				Params:      params,
				Path:        searchPath,
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !granted {
				return NewPermissionDeniedResponse(), nil
			}

			return runAstGrepScan(ctx, params, searchPath, config)
		},
	)
}

type astGrepMatch struct {
	Text  string `json:"text"`
	File  string `json:"file"`
	Range struct {
		Start struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"start"`
	} `json:"range"`
}

func runAstGrepScan(ctx context.Context, params AstGrepParams, path string, cfg config.ToolAstGrep) (fantasy.ToolResponse, error) {
	args := []string{"run", "--pattern", params.Pattern, "--json"}
	if params.Lang != "" {
		args = append(args, "--lang", params.Lang)
	}
	args = append(args, path)

	searchCtx, cancel := context.WithTimeout(ctx, cfg.GetTimeout())
	defer cancel()

	cmd := exec.CommandContext(searchCtx, "ast-grep", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to execute ast-grep: %v", err)), nil
		}
		// ast-grep exits with 1 if no matches are found.
		if len(output) == 0 {
			return fantasy.NewTextResponse("No matches found"), nil
		}
	}

	var matches []astGrepMatch
	if err := json.Unmarshal(output, &matches); err != nil {
		// If it's not valid JSON, it might be an error message from ast-grep.
		return fantasy.NewTextResponse(string(output)), nil
	}

	if len(matches) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matches\n", len(matches))
	currentFile := ""
	for _, match := range matches {
		if match.File != currentFile {
			if currentFile != "" {
				sb.WriteString("\n")
			}
			currentFile = match.File
			fmt.Fprintf(&sb, "%s:\n", match.File)
		}
		line := match.Range.Start.Line + 1
		col := match.Range.Start.Column + 1
		text := strings.TrimSpace(match.Text)
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		fmt.Fprintf(&sb, "  Line %d, Char %d: %s\n", line, col, text)
	}

	return fantasy.NewTextResponse(sb.String()), nil
}

func runAstGrepRewrite(ctx context.Context, params AstGrepParams, path string, cfg config.ToolAstGrep) (fantasy.ToolResponse, error) {
	args := []string{"run", "--pattern", params.Pattern, "--rewrite", params.Rewrite, "--update-all"}
	if params.Lang != "" {
		args = append(args, "--lang", params.Lang)
	}
	args = append(args, path)

	searchCtx, cancel := context.WithTimeout(ctx, cfg.GetTimeout())
	defer cancel()

	cmd := exec.CommandContext(searchCtx, "ast-grep", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("error running ast-grep rewrite: %v\nOutput: %s", err, string(output))), nil
	}

	return fantasy.NewTextResponse(fmt.Sprintf("Successfully applied rewrites.\n%s", string(output))), nil
}
