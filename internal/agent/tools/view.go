package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/skills"
)

//go:embed view.md.tpl
var viewDescriptionTmpl []byte

var viewDescriptionTpl = template.Must(
	template.New("viewDescription").
		Parse(string(viewDescriptionTmpl)),
)

type viewDescriptionData struct {
	DefaultReadLimit int
	MaxViewSizeKB    int
}

func viewDescription() string {
	return renderTemplate(viewDescriptionTpl, viewDescriptionData{
		DefaultReadLimit: DefaultReadLimit,
		MaxViewSizeKB:    MaxViewSize / 1024,
	})
}

type ViewParams struct {
	FilePath string `json:"file_path" description:"The path to the file to read"`
	Offset   int    `json:"offset,omitempty" description:"The line number to start reading from (0-based)"`
	Limit    int    `json:"limit,omitempty" description:"The number of lines to read (defaults to 2000)"`
	Fold     bool   `json:"fold,omitempty" description:"If true, use regex-based folding to collapse function/type bodies and return a compressed semantic view (supports Nim and Go)"`
}

type ViewPermissionsParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
	Fold     bool   `json:"fold"`
}

type viewLine struct {
	Number int
	Text   string
}

func foldLines(lines []string, filePath string) []viewLine {
	ext := filepath.Ext(filePath)
	var result []viewLine
	for i, line := range lines {
		result = append(result, viewLine{Number: i + 1, Text: line})
	}

	if ext != ".go" && ext != ".nim" {
		return result
	}

	var folded []viewLine
	if ext == ".go" {
		inBlock := false
		braceCount := 0
		for _, line := range result {
			trimmed := strings.TrimSpace(line.Text)
			if !inBlock {
				folded = append(folded, line)
				// Start folding if it's a func or type declaration ending with {
				if (strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "type ")) && strings.HasSuffix(trimmed, "{") {
					inBlock = true
					braceCount = 1
					folded = append(folded, viewLine{Number: 0, Text: "    ..."})
				}
			} else {
				braceCount += strings.Count(line.Text, "{")
				braceCount -= strings.Count(line.Text, "}")
				if braceCount <= 0 {
					inBlock = false
					// Only keep the line if it contains the final closing brace
					if strings.Contains(line.Text, "}") {
						folded = append(folded, line)
					}
				}
			}
		}
	} else if ext == ".nim" {
		inBlock := false
		blockIndent := -1
		declRegex := regexp.MustCompile(`^(proc|template|macro|method|iterator|converter|type)\b`)

		for i := 0; i < len(result); i++ {
			line := result[i]
			trimmed := strings.TrimSpace(line.Text)
			if trimmed == "" {
				if !inBlock {
					folded = append(folded, line)
				}
				continue
			}

			indent := len(line.Text) - len(strings.TrimLeft(line.Text, " "))
			if !inBlock {
				folded = append(folded, line)
				if declRegex.MatchString(trimmed) {
					// Look ahead for indentation
					nextIdx := i + 1
					for nextIdx < len(result) && strings.TrimSpace(result[nextIdx].Text) == "" {
						nextIdx++
					}
					if nextIdx < len(result) {
						nextIndent := len(result[nextIdx].Text) - len(strings.TrimLeft(result[nextIdx].Text, " "))
						if nextIndent > indent {
							inBlock = true
							blockIndent = indent
							folded = append(folded, viewLine{Number: 0, Text: strings.Repeat(" ", nextIndent) + "..."})
						}
					}
				}
			} else {
				if indent <= blockIndent {
					inBlock = false
					folded = append(folded, line)
				}
			}
		}
	}
	if len(folded) > 0 {
		return folded
	}
	return result
}

type ViewResourceType string

const (
	ViewResourceUnset ViewResourceType = ""
	ViewResourceSkill ViewResourceType = "skill"
)

type ViewResponseMetadata struct {
	FilePath            string           `json:"file_path"`
	Content             string           `json:"content"`
	ResourceType        ViewResourceType `json:"resource_type,omitempty"`
	ResourceName        string           `json:"resource_name,omitempty"`
	ResourceDescription string           `json:"resource_description,omitempty"`
}

const (
	ViewToolName     = "view"
	MaxViewSize      = 200 * 1024 // 200KB
	DefaultReadLimit = 2000
	MaxLineLength    = 2000
)

type contentTooLargeError struct {
	Size int
	Max  int
}

func (e contentTooLargeError) Error() string {
	return fmt.Sprintf("content section is too large (%d bytes). Maximum size is %d bytes", e.Size, e.Max)
}

func NewViewTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	filetracker filetracker.Service,
	skillTracker *skills.Tracker,
	workingDir string,
	skillsPaths ...string,
) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		ViewToolName,
		viewDescription(),
		func(ctx context.Context, params ViewParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			// Handle skill files (crush://skills/ prefix).
			if strings.HasPrefix(params.FilePath, skills.BuiltinPrefix) {
				// Try to resolve virtual skill paths (e.g. crush://skills/e2e-test/SKILL.md)
				// to their actual filesystem paths if they are user skills.
				pathWithoutPrefix := strings.TrimPrefix(params.FilePath, skills.BuiltinPrefix)
				parts := strings.Split(pathWithoutPrefix, "/")
				if len(parts) >= 1 {
					name := parts[0]
					if resolved := skillTracker.GetPath(name); resolved != "" {
						// If it resolves to an absolute filesystem path, use it.
						// Builtin skills resolve to a crush:// path themselves.
						if !strings.HasPrefix(resolved, skills.BuiltinPrefix) {
							params.FilePath = resolved
						}
					}
				}

				// If it's still a crush:// path, it's a builtin skill.
				if strings.HasPrefix(params.FilePath, skills.BuiltinPrefix) {
					resp, err := readBuiltinFile(params, skillTracker)
					return resp, err
				}
			}

			// Handle relative paths
			filePath := filepathext.SmartJoin(workingDir, params.FilePath)
			slog.Debug("View tool reading file", "path", filePath, "original", params.FilePath)

			// Check if file is outside working directory and request permission if needed
			absWorkingDir, err := filepath.Abs(workingDir)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error resolving working directory: %w", err)
			}

			absFilePath, err := filepath.Abs(filePath)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error resolving file path: %w", err)
			}

			relPath, err := filepath.Rel(absWorkingDir, absFilePath)
			isOutsideWorkDir := err != nil || strings.HasPrefix(relPath, "..")
			isSkillFile := isInSkillsPath(absFilePath, skillsPaths)

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for accessing files outside working directory")
			}

			// Request permission for files outside working directory, unless it's a skill file.
			if isOutsideWorkDir && !isSkillFile {
				granted, permReqErr := permissions.Request(
					ctx,
					permission.CreatePermissionRequest{
						SessionID:   sessionID,
						Path:        absFilePath,
						ToolCallID:  call.ID,
						ToolName:    ViewToolName,
						Action:      "read",
						Description: fmt.Sprintf("Read file outside working directory: %s", absFilePath),
						Params:      ViewPermissionsParams(params),
					},
				)
				if permReqErr != nil {
					return fantasy.ToolResponse{}, permReqErr
				}
				if !granted {
					return NewPermissionDeniedResponse(), nil
				}
			}

			// Check if file exists
			fileInfo, err := CtxStat(ctx, filePath)
			if err != nil {
				if os.IsNotExist(err) {
					// Try to offer suggestions for similarly named files
					dir := filepath.Dir(filePath)
					base := filepath.Base(filePath)

					dirEntries, dirErr := os.ReadDir(dir)
					if dirErr == nil {
						var suggestions []string
						for _, entry := range dirEntries {
							if strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(base)) ||
								strings.Contains(strings.ToLower(base), strings.ToLower(entry.Name())) {
								suggestions = append(suggestions, filepath.Join(dir, entry.Name()))
								if len(suggestions) >= 3 {
									break
								}
							}
						}

						if len(suggestions) > 0 {
							return fantasy.NewTextErrorResponse(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
								filePath, strings.Join(suggestions, "\n"))), nil
						}
					}

					return fantasy.NewTextErrorResponse(fmt.Sprintf("File not found: %s", filePath)), nil
				}
				return fantasy.ToolResponse{}, fmt.Errorf("error accessing file: %w", err)
			}

			// Check if it's a directory
			if fileInfo.IsDir() {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
			}

			// Set default limit if not provided (no limit for SKILL.md files)
			if params.Limit <= 0 {
				if isSkillFile {
					params.Limit = 1000000 // Effectively no limit for skill files
				} else {
					params.Limit = DefaultReadLimit
				}
			}

			isSupportedImage, mimeType := getImageMimeType(filePath)
			if isSupportedImage {
				if fileInfo.Size() > MaxViewSize {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Image file is too large (%d bytes). Maximum size is %d bytes",
						fileInfo.Size(), MaxViewSize)), nil
				}
				if !GetSupportsImagesFromContext(ctx) {
					modelName := GetModelNameFromContext(ctx)
					return fantasy.NewTextErrorResponse(fmt.Sprintf("This model (%s) does not support image data.", modelName)), nil
				}

				imageData, readErr := CtxReadFile(ctx, filePath)
				if readErr != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error reading image file: %w", readErr)
				}

				// Some tools save files with a mismatched extension
				// (e.g. pinchtab writes JPEG bytes to a .png file).
				// Providers like Anthropic strictly validate the
				// media type against the base64 magic bytes and 400
				// on mismatch, so prefer the sniffed type whenever
				// it identifies a supported image format.
				mimeType = sniffImageMimeType(imageData, mimeType)

				return fantasy.NewImageResponse(imageData, mimeType), nil
			}

			// Read the file content
			maxContentSize := MaxViewSize
			if isSkillFile {
				maxContentSize = 0
			}
			content, rawContent, hasMore, err := readTextFile(ctx, filePath, params.Offset, params.Limit, maxContentSize, params.Fold)
			if err != nil {
				var tooLarge contentTooLargeError
				if errors.As(err, &tooLarge) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Content section is too large (%d bytes). Maximum size is %d bytes",
						tooLarge.Size, tooLarge.Max)), nil
				}
				return fantasy.ToolResponse{}, fmt.Errorf("error reading file: %w", err)
			}
			if !utf8.ValidString(rawContent) {
				return fantasy.NewTextErrorResponse("File content is not valid UTF-8"), nil
			}

			openInLSPs(ctx, lspManager, filePath)
			waitForLSPDiagnostics(ctx, lspManager, filePath, 300*time.Millisecond)
			output := "<file>\n"
			output += content

			if hasMore {
				output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
					params.Offset+len(strings.Split(content, "\n")))
			}
			output += "\n</file>\n"
			output += getDiagnostics(filePath, lspManager)
			filetracker.RecordRead(ctx, sessionID, filePath)

			meta := ViewResponseMetadata{
				FilePath: filePath,
				Content:  rawContent,
			}
			if isSkillFile {
				if skill, err := skills.Parse(filePath); err == nil {
					meta.ResourceType = ViewResourceSkill
					meta.ResourceName = skill.Name
					meta.ResourceDescription = skill.Description
					skillTracker.MarkLoaded(skill.Name)
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output),
				meta,
			), nil
		},
	)
}

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		lineNum := i + startLine
		numStr := fmt.Sprintf("%d", lineNum)

		if len(numStr) >= 6 {
			result = append(result, fmt.Sprintf("%s|%s", numStr, line))
		} else {
			paddedNum := fmt.Sprintf("%6s", numStr)
			result = append(result, fmt.Sprintf("%s|%s", paddedNum, line))
		}
	}

	return strings.Join(result, "\n")
}

func readTextFile(ctx context.Context, filePath string, offset, limit, maxContentSize int, fold bool) (string, string, bool, error) {
	data, err := CtxReadFile(ctx, filePath)
	if err != nil {
		return "", "", false, err
	}
	rawLines := strings.Split(string(data), "\n")
	// Strip a trailing empty element from a final newline so line counts
	// match the editor's notion of "lines in the file".
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}

	var lines []viewLine
	if fold {
		lines = foldLines(rawLines, filePath)
	} else {
		lines = make([]viewLine, len(rawLines))
		for i, line := range rawLines {
			lines[i] = viewLine{Number: i + 1, Text: line}
		}
	}

	if offset >= len(lines) {
		return "", "", false, nil
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	slice := lines[offset:end]

	var resultLines []string
	var rawResultLines []string
	contentSize := 0
	for i, line := range slice {
		lineText := strings.TrimSuffix(line.Text, "\r")
		if len(lineText) > MaxLineLength {
			lineText = lineText[:MaxLineLength] + "..."
		}

		var formatted string
		if line.Number == 0 {
			formatted = "      | " + lineText
		} else {
			numStr := fmt.Sprintf("%d", line.Number)
			if len(numStr) >= 6 {
				formatted = fmt.Sprintf("%s|%s", numStr, lineText)
			} else {
				formatted = fmt.Sprintf("%6s|%s", numStr, lineText)
			}
		}

		projectedSize := contentSize + len(lineText)
		if i > 0 {
			projectedSize++
		}
		if maxContentSize > 0 && projectedSize > maxContentSize {
			return "", "", false, contentTooLargeError{Size: projectedSize, Max: maxContentSize}
		}
		contentSize = projectedSize
		resultLines = append(resultLines, formatted)
		rawResultLines = append(rawResultLines, lineText)
	}

	hasMore := end < len(lines)
	return strings.Join(resultLines, "\n"), strings.Join(rawResultLines, "\n"), hasMore, nil
}

func getImageMimeType(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "image/jpeg"
	case ".png":
		return true, "image/png"
	case ".gif":
		return true, "image/gif"
	case ".webp":
		return true, "image/webp"
	default:
		return false, ""
	}
}

// sniffImageMimeType returns the content-sniffed MIME type when it identifies
// a supported image format. Otherwise it returns the provided fallback, which
// is usually the extension-derived type. Providers that validate the image
// media type against the base64 magic bytes (e.g. Anthropic) reject mismatched
// requests with a 400, so trusting the filename alone is unsafe.
func sniffImageMimeType(data []byte, fallback string) string {
	sniffed := http.DetectContentType(data)
	// http.DetectContentType may return the MIME with a ";" parameter
	// (e.g. "image/svg+xml; charset=utf-8") although current image sniffers
	// return bare types; strip defensively.
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = strings.TrimSpace(sniffed[:i])
	}
	switch sniffed {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return sniffed
	}
	return fallback
}

func isInSkillsPath(filePath string, skillsPaths []string) bool {
	if len(skillsPaths) == 0 {
		return false
	}

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}

	evalFilePath, err := filepath.EvalSymlinks(absFilePath)
	hasEvalFile := err == nil

	for _, skillsPath := range skillsPaths {
		absSkillsPath, err := filepath.Abs(skillsPath)
		if err != nil {
			continue
		}

		// Check 1: Try matching using unresolved (absolute) paths.
		// This handles the case where the file path is under a symlinked skills directory
		// (e.g. filePath = /home/user/.claude/skills/my-skill/SKILL.md, skillsPath = /home/user/.claude/skills).
		// Since both share the unresolved symlink path prefix, Rel will match correctly.
		relPath, err := filepath.Rel(absSkillsPath, absFilePath)
		if err == nil && !strings.HasPrefix(relPath, "..") {
			return true
		}

		// Check 2: Try matching using fully evaluated paths.
		// This handles the case where either the filePath or skillsPath (or both) are fully resolved.
		evalSkillsPath, err := filepath.EvalSymlinks(absSkillsPath)
		if err != nil {
			continue
		}

		if hasEvalFile {
			relPath, err = filepath.Rel(evalSkillsPath, evalFilePath)
			if err == nil && !strings.HasPrefix(relPath, "..") {
				return true
			}
		}
	}

	return false
}

// readBuiltinFile reads a file from the embedded builtin skills filesystem.
func readBuiltinFile(params ViewParams, skillTracker *skills.Tracker) (fantasy.ToolResponse, error) {
	embeddedPath := "builtin/" + strings.TrimPrefix(params.FilePath, skills.BuiltinPrefix)
	builtinFS := skills.BuiltinFS()

	data, err := fs.ReadFile(builtinFS, embeddedPath)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Internal builtin skill file not found: %s. Please check <available_skills> for the correct location of this skill.", params.FilePath)), nil
	}

	content := string(data)
	if !utf8.ValidString(content) {
		return fantasy.NewTextErrorResponse("File content is not valid UTF-8"), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 1000000 // Effectively no limit for skill files.
	}

	lines := strings.Split(content, "\n")
	offset := min(params.Offset, len(lines))
	lines = lines[offset:]

	hasMore := len(lines) > limit
	if hasMore {
		lines = lines[:limit]
	}

	output := "<file>\n"
	output += addLineNumbers(strings.Join(lines, "\n"), offset+1)
	if hasMore {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			offset+len(lines))
	}
	output += "\n</file>\n"

	meta := ViewResponseMetadata{
		FilePath: params.FilePath,
		Content:  strings.Join(lines, "\n"),
	}
	if skill, err := skills.ParseContent(data); err == nil {
		meta.ResourceType = ViewResourceSkill
		meta.ResourceName = skill.Name
		meta.ResourceDescription = skill.Description
		skillTracker.MarkLoaded(skill.Name)
	}

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(output),
		meta,
	), nil
}
