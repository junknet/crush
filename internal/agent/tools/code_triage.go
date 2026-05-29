package tools

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"golang.org/x/sync/errgroup"
)

type CodeTriageParams struct {
	Intent         string                   `json:"intent,omitempty" description:"Semantic intent for the triage: inspect|understand|locate_bug|review|verify|refactor"`
	Queries        []CodeTriageQuery        `json:"queries" description:"Search tasks (rg query list) used during triage"`
	CheckCommands  []CodeTriageCheckCommand `json:"check_commands,omitempty" description:"Compile/test commands to run after triage. Omit in pure inspection mode."`
	TimeoutSeconds int                      `json:"timeout_seconds,omitempty" description:"Timeout for each query/check in seconds (default 20, max 120)"`
	MaxResults     int                      `json:"max_results,omitempty" description:"Maximum matches per query (default 50, max 200)"`
}

type CodeTriageQuery struct {
	ID          string `json:"id,omitempty" description:"Optional stable identifier for this query"`
	Query       string `json:"query" description:"Search pattern for rg (regex by default, glob when FilesOnly=true and pattern resembles glob)"`
	Path        string `json:"path,omitempty" description:"Search directory (defaults to working directory)"`
	Include     string `json:"include,omitempty" description:"Path glob passed to rg"`
	FilesOnly   bool   `json:"files_only,omitempty" description:"Search filenames only"`
	LiteralText bool   `json:"literal_text,omitempty" description:"Treat query as literal text when true"`
}

type CodeTriageCheckCommand struct {
	Name           string `json:"name,omitempty" description:"Human-readable command name"`
	Command        string `json:"command" description:"Command to execute for checks/tests"`
	Description    string `json:"description,omitempty" description:"Why this check exists"`
	Language       string `json:"language,omitempty" description:"shell|python|node"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"Optional command override timeout in seconds"`
}

type CodeTriageQueryFinding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Snippet string `json:"snippet"`
}

type CodeTriageQueryResponse struct {
	ID         string                   `json:"id"`
	Query      string                   `json:"query"`
	Path       string                   `json:"path"`
	FilesOnly  bool                     `json:"files_only"`
	Outcome    string                   `json:"outcome"`
	Error      string                   `json:"error,omitempty"`
	Matches    int                      `json:"matches"`
	Truncated  bool                     `json:"truncated"`
	TopMatches []CodeTriageQueryFinding `json:"top_matches"`
}

type CodeTriageFinding struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

type CodeTriageCheckResponse struct {
	Name        string              `json:"name"`
	Command     string              `json:"command"`
	Description string              `json:"description,omitempty"`
	Language    string              `json:"language"`
	ExitCode    int                 `json:"exit_code"`
	DurationMs  int64               `json:"duration_ms"`
	Outcome     string              `json:"outcome"`
	Output      string              `json:"output"`
	Findings    []CodeTriageFinding `json:"findings"`
}

type CodeTriageEvidenceSummary struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Outcome  string `json:"outcome"`
	Count    int    `json:"count,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message,omitempty"`
}

type CodeTriageGuidance struct {
	Phase          string   `json:"phase"`
	PrimaryFile    string   `json:"primary_file,omitempty"`
	PrimaryLine    int      `json:"primary_line,omitempty"`
	PrimaryMessage string   `json:"primary_message,omitempty"`
	NextAction     string   `json:"next_action"`
	CandidateFiles []string `json:"candidate_files,omitempty"`
}

type CodeTriageResponseMetadata struct {
	DurationMs   int64                       `json:"duration_ms"`
	Intent       string                      `json:"intent"`
	TotalMatches int                         `json:"total_matches"`
	Queries      []CodeTriageQueryResponse   `json:"queries"`
	Checks       []CodeTriageCheckResponse   `json:"checks"`
	Evidence     []CodeTriageEvidenceSummary `json:"evidence"`
	Guidance     CodeTriageGuidance          `json:"guidance"`
	Summary      string                      `json:"summary"`
	OverallRisk  string                      `json:"overall_risk"`
}

const (
	CodeTriageToolName = "code_triage"
	BugTriageToolName  = "bug_triage"
)

const (
	codeTriageDefaultTimeout = 20
	codeTriageMaxTimeout     = 120
	codeTriageDefaultLimit   = 50
	codeTriageMaxLimit       = 200
	codeTriageQueryTopMatch  = 6
	codeTriageMaxFindings    = 20
	codeTriageOutputMaxBytes = 12000
)

const (
	codeTriageRiskLow    = "low"
	codeTriageRiskMedium = "medium"
	codeTriageRiskHigh   = "high"
)

const codeTriageDefaultTaskIDPrefix = "query"

//go:embed code_triage.md
var codeTriageDescription string

func NewCodeTriageTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		CodeTriageToolName,
		codeTriageDescription,
		func(ctx context.Context, params CodeTriageParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if len(params.Queries) == 0 && len(params.CheckCommands) == 0 {
				return fantasy.NewTextErrorResponse("at least one of queries or check_commands is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing code_triage")
			}

			queryTimeout := clampDuration(params.TimeoutSeconds, codeTriageDefaultTimeout, codeTriageMaxTimeout)
			maxResults := clampInt(params.MaxResults, codeTriageDefaultLimit, 1, codeTriageMaxLimit)

			start := time.Now()
			meta := CodeTriageResponseMetadata{}
			meta.Intent = normalizeCodeTriageIntent(params.Intent)
			meta.Queries = make([]CodeTriageQueryResponse, len(params.Queries))
			meta.Checks = make([]CodeTriageCheckResponse, 0, len(params.CheckCommands))

			if len(params.Queries) > 0 {
				if err := codeTriageRunQueries(ctx, permissions, sessionID, call.ID, workingDir, params, queryTimeout, maxResults, meta.Queries); err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
			}

			for _, check := range params.CheckCommands {
				checkCommand := strings.TrimSpace(check.Command)
				if checkCommand == "" {
					continue
				}

				checkName := cmpOr(check.Name, "check")
				language := normalizeCodeTriageLanguage(check.Language)
				granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
					SessionID:   sessionID,
					ToolCallID:  call.ID,
					ToolName:    CodeTriageToolName,
					Action:      "execute",
					Path:        workingDir,
					Description: fmt.Sprintf("Run check command for code triage: %s", checkName),
					Params:      check,
				})
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !granted {
					return NewPermissionDeniedResponse(), nil
				}

				checkTimeout := clampDuration(check.TimeoutSeconds, int(queryTimeout.Seconds()), codeTriageMaxTimeout)
				checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
				checkStart := time.Now()
				stdout, stderr, cmdErr := executeRunScript(checkCtx, workingDir, language, checkCommand)
				cancel()
				duration := time.Since(checkStart).Milliseconds()
				if checkCtx.Err() == context.DeadlineExceeded {
					stdout = fmt.Sprintf("%s\ncheck timed out after %s", stdout, checkTimeout)
				}

				output := strings.TrimSpace(formatOutput(stdout, stderr, cmdErr))
				outcome := "passed"
				exitCode := 0
				if cmdErr != nil {
					exitCode = 1
					outcome = "failed"
				}

				meta.Checks = append(meta.Checks, CodeTriageCheckResponse{
					Name:        checkName,
					Command:     checkCommand,
					Description: strings.TrimSpace(check.Description),
					Language:    language,
					ExitCode:    exitCode,
					DurationMs:  duration,
					Outcome:     outcome,
					Output:      truncateString(output, codeTriageOutputMaxBytes),
					Findings:    parseCodeTriageOutputForFindings(output),
				})
			}

			for _, q := range meta.Queries {
				meta.TotalMatches += q.Matches
			}
			meta.DurationMs = time.Since(start).Milliseconds()
			meta.OverallRisk = codeTriageOverallRisk(meta)
			meta.Evidence = buildCodeTriageEvidence(meta)
			meta.Guidance = buildCodeTriageGuidance(meta)
			meta.Summary = buildCodeTriageSummary(len(meta.Queries), len(meta.Checks), meta.TotalMatches, meta.OverallRisk)

			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(formatCodeTriageOutput(meta)), meta), nil
		},
	)
}

type codeTriageQueryResult struct {
	idx      int
	response CodeTriageQueryResponse
}

func codeTriageRunQueries(
	ctx context.Context,
	permissions permission.Service,
	sessionID, toolCallID, workingDir string,
	params CodeTriageParams,
	timeout time.Duration,
	maxResults int,
	out []CodeTriageQueryResponse,
) error {
	var (
		g   errgroup.Group
		res = make([]codeTriageQueryResult, len(params.Queries))
	)

	for i, query := range params.Queries {
		i := i
		query := query
		g.Go(func() error {
			queryID := cmpOr(query.ID, fmt.Sprintf("%s-%d", codeTriageDefaultTaskIDPrefix, i+1))
			queryPath := filepath.Clean(cmpOr(strings.TrimSpace(query.Path), workingDir))
			if !filepath.IsAbs(queryPath) {
				queryPath = filepath.Join(workingDir, queryPath)
			}
			queryText := strings.TrimSpace(query.Query)
			if queryText == "" {
				res[i] = codeTriageQueryResult{idx: i, response: CodeTriageQueryResponse{
					ID:      queryID,
					Path:    queryPath,
					Outcome: "failed",
					Error:   "query text is required",
				}}
				return nil
			}
			granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				ToolCallID:  toolCallID,
				ToolName:    CodeTriageToolName,
				Action:      "search",
				Path:        queryPath,
				Description: fmt.Sprintf("Run code triage query %q", queryText),
				Params:      query,
			})
			if err != nil {
				return err
			}
			if !granted {
				res[i] = codeTriageQueryResult{idx: i, response: CodeTriageQueryResponse{
					ID:        queryID,
					Query:     queryText,
					Path:      queryPath,
					FilesOnly: query.FilesOnly,
					Outcome:   "failed",
					Error:     "permission denied",
				}}
				return nil
			}

			searchCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			matches, truncated, err := executeCodeTriageQuery(searchCtx, queryText, queryPath, query.Include, query.FilesOnly, query.LiteralText, maxResults)
			if err != nil {
				res[i] = codeTriageQueryResult{idx: i, response: CodeTriageQueryResponse{
					ID:        queryID,
					Query:     queryText,
					Path:      queryPath,
					FilesOnly: query.FilesOnly,
					Outcome:   "failed",
					Error:     err.Error(),
				}}
				return nil
			}

			topMatches := make([]CodeTriageQueryFinding, 0, minInt(len(matches), codeTriageQueryTopMatch))
			for j := range matches {
				if j >= codeTriageQueryTopMatch {
					break
				}
				topMatches = append(topMatches, CodeTriageQueryFinding{
					File:    matches[j].Path,
					Line:    matches[j].LineNum,
					Column:  matches[j].CharNum,
					Snippet: matches[j].LineText,
				})
			}

			res[i] = codeTriageQueryResult{idx: i, response: CodeTriageQueryResponse{
				ID:         queryID,
				Query:      queryText,
				Path:       queryPath,
				FilesOnly:  query.FilesOnly,
				Outcome:    "passed",
				Matches:    len(matches),
				Truncated:  truncated,
				TopMatches: topMatches,
			}}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	for _, queryResult := range res {
		if queryResult.response.ID == "" {
			continue
		}
		out[queryResult.idx] = queryResult.response
	}
	return nil
}

func executeCodeTriageQuery(ctx context.Context, query, path, include string, filesOnly bool, literal bool, maxResults int) ([]RgMatch, bool, error) {
	if filesOnly {
		return RgSearchFiles(ctx, query, path, include, maxResults)
	}
	if literal {
		query = regexp.QuoteMeta(query)
	}
	return RgSearch(ctx, query, path, include, maxResults)
}

var codeTriageLocationRegexes = []*regexp.Regexp{
	regexp.MustCompile(`^\s*at\s+.+\((?P<file>[^:]+):(?P<line>\d+):(?P<col>\d+)\)`),
	regexp.MustCompile(`^\s*at\s+.+\((?P<file>[^:]+):(?P<line>\d+)\)`),
	regexp.MustCompile(`^\s*\d+:\s+[^\s]+\s+\((?P<file>[^:]+):(?P<line>\d+):(?P<col>\d+)\)`),
	regexp.MustCompile(`^\s*\d+:\s+[^\s]+\s+\((?P<file>[^:]+):(?P<line>\d+)\)`),
	regexp.MustCompile(`(?i)^\s*File\s+"(?P<file>[^"]+)",\s*line\s*(?P<line>\d+)(?:,\s*in\s+[^\n\r-]+)?(?:\s*-\s*(?P<message>.+))?$`),
	regexp.MustCompile(`(?i)^\s*File\s+'(?P<file>[^']+)',\s*line\s*(?P<line>\d+)(?:,\s*in\s+[^\n\r-]+)?(?:\s*-\s*(?P<message>.+))?$`),
	regexp.MustCompile(`(?i)^(?P<file>.+):(?P<line>\d+):(?P<col>\d+):\s*(?P<message>.+)$`),
	regexp.MustCompile(`(?i)^(?P<file>.+):(?P<line>\d+):(?P<col>\d+)\s+(?P<message>.+)$`),
	regexp.MustCompile(`(?i)^(?P<file>.+):(?P<line>\d+):\s*(?P<message>.+)$`),
	regexp.MustCompile(`(?i)^(?P<file>.+)\[(?P<line>\d+),(?P<col>\d+)\]\s*(?P<message>.+)$`),
}

var codeTriageAnsiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func parseCodeTriageOutputForFindings(output string) []CodeTriageFinding {
	if output == "" {
		return nil
	}

	lines := strings.Split(output, "\n")
	findings := make([]CodeTriageFinding, 0, minInt(len(lines), codeTriageMaxFindings))
	seen := make(map[string]struct{}, minInt(len(lines), codeTriageMaxFindings))
	contextMessage := ""
	contextFile := ""
	contextLine := 0
	contextColumn := 0

	for _, line := range lines {
		line = sanitizeCodeTriageLine(line)
		if line == "" {
			continue
		}
		if len(findings) >= codeTriageMaxFindings {
			break
		}

		file, ln, col, message := parseCodeTriageLocation(line)
		if file == "" && isStackContextLine(line) {
			contextMessage = line
			contextFile = ""
			contextLine = 0
			contextColumn = 0
			continue
		}

		isNoLocationSignal := isCodeTriageNoLocationSignal(line)
		if file != "" {
			if message == "" {
				contextFile = file
				contextLine = ln
				contextColumn = col
				continue
			}
			contextFile = file
			contextLine = ln
			contextColumn = col
			if isNoLocationSignal && contextMessage != "" {
				message = contextMessage + ": " + message
				contextMessage = ""
			}
		}

		if file == "" {
			if !isNoLocationSignal {
				continue
			}
			if contextFile != "" {
				file = contextFile
				ln = contextLine
				col = contextColumn
			}
			if contextMessage != "" {
				message = contextMessage + ": " + line
				contextMessage = ""
			} else {
				message = line
			}
		}

		if file == "" && message == "" {
			continue
		}

		key := fmt.Sprintf("%s|%d|%d|%s", file, ln, col, strings.TrimSpace(message))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		contextMessage = ""

		severity := classifyCodeTriageFindingSeverity(message)
		findings = append(findings, CodeTriageFinding{
			Severity: severity,
			Message:  message,
			File:     file,
			Line:     ln,
			Column:   col,
		})
	}
	return findings
}

func isStackContextLine(line string) bool {
	l := strings.ToLower(line)
	if strings.HasPrefix(l, "traceback") || strings.HasPrefix(l, "panic:") || strings.HasPrefix(l, "exception") {
		return true
	}
	return strings.Contains(l, " error:") || strings.Contains(l, "failed:") || strings.Contains(l, "panicked")
}

func isCodeTriageNoLocationSignal(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "error") ||
		strings.Contains(l, "exception") ||
		strings.Contains(l, "panic") ||
		strings.Contains(l, "failed") ||
		strings.Contains(l, "fatal") ||
		strings.HasPrefix(l, "traceback")
}

func sanitizeCodeTriageLine(line string) string {
	line = strings.TrimSpace(line)
	line = codeTriageAnsiPattern.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

func parseCodeTriageLocation(line string) (string, int, int, string) {
	for _, re := range codeTriageLocationRegexes {
		matches := re.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		names := re.SubexpNames()
		m := map[string]string{}
		for i, name := range names {
			if name != "" && i < len(matches) {
				m[name] = matches[i]
			}
		}
		ln, _ := strconv.Atoi(m["line"])
		col, _ := strconv.Atoi(m["col"])
		return strings.TrimSpace(m["file"]), ln, col, strings.TrimSpace(m["message"])
	}
	return "", 0, 0, ""
}

func classifyCodeTriageFindingSeverity(message string) string {
	l := strings.ToLower(message)
	switch {
	case strings.Contains(l, "panic") || strings.Contains(l, "data race") || strings.Contains(l, "segmentation") || strings.Contains(l, "out of memory") || strings.Contains(l, "fatal"):
		return codeTriageRiskHigh
	case strings.Contains(l, "error") || strings.Contains(l, "failed") || strings.Contains(l, "exception"):
		return codeTriageRiskMedium
	case strings.Contains(l, "warning") || strings.Contains(l, "warn"):
		return codeTriageRiskLow
	default:
		return codeTriageRiskLow
	}
}

func codeTriageOverallRisk(meta CodeTriageResponseMetadata) string {
	for _, cmd := range meta.Checks {
		if cmd.Outcome == "failed" {
			return codeTriageRiskHigh
		}
		for _, finding := range cmd.Findings {
			if finding.Severity == codeTriageRiskHigh {
				return codeTriageRiskHigh
			}
		}
	}
	for _, cmd := range meta.Checks {
		for _, finding := range cmd.Findings {
			if finding.Severity == codeTriageRiskMedium {
				return codeTriageRiskMedium
			}
		}
	}
	if len(meta.Checks) > 0 {
		return codeTriageRiskLow
	}
	return codeTriageRiskLow
}

func buildCodeTriageEvidence(meta CodeTriageResponseMetadata) []CodeTriageEvidenceSummary {
	evidence := make([]CodeTriageEvidenceSummary, 0, len(meta.Queries)+len(meta.Checks))
	for _, q := range meta.Queries {
		item := CodeTriageEvidenceSummary{
			Kind:    "query",
			ID:      q.ID,
			Outcome: cmpOr(q.Outcome, "passed"),
			Count:   q.Matches,
		}
		if q.Error != "" {
			item.Message = q.Error
		} else if len(q.TopMatches) > 0 {
			item.File = q.TopMatches[0].File
			item.Line = q.TopMatches[0].Line
			item.Message = q.TopMatches[0].Snippet
		}
		evidence = append(evidence, item)
	}
	for _, check := range meta.Checks {
		item := CodeTriageEvidenceSummary{
			Kind:    "check",
			ID:      check.Name,
			Outcome: check.Outcome,
			Count:   len(check.Findings),
		}
		if len(check.Findings) > 0 {
			primary := highestSeverityFinding(check.Findings)
			item.File = primary.File
			item.Line = primary.Line
			item.Severity = primary.Severity
			item.Message = primary.Message
		} else if check.Output != "" {
			item.Message = truncateString(check.Output, 220)
		}
		evidence = append(evidence, item)
	}
	return evidence
}

func buildCodeTriageGuidance(meta CodeTriageResponseMetadata) CodeTriageGuidance {
	guidance := CodeTriageGuidance{
		Phase:      "inspect",
		NextAction: "inspect focused evidence before editing",
	}
	if meta.Intent != "" {
		guidance.Phase = meta.Intent
	}

	var primary *CodeTriageFinding
	for _, check := range meta.Checks {
		if len(check.Findings) == 0 {
			continue
		}
		candidate := highestSeverityFinding(check.Findings)
		if primary == nil || codeTriageSeverityRank(candidate.Severity) > codeTriageSeverityRank(primary.Severity) {
			candidateCopy := candidate
			primary = &candidateCopy
		}
	}
	if primary != nil {
		guidance.PrimaryFile = primary.File
		guidance.PrimaryLine = primary.Line
		guidance.PrimaryMessage = primary.Message
		guidance.NextAction = "open the primary finding, inspect nearby code, then rerun the failing check"
	} else if first := firstQueryMatch(meta.Queries); first != nil {
		guidance.PrimaryFile = first.File
		guidance.PrimaryLine = first.Line
		guidance.PrimaryMessage = first.Snippet
		guidance.NextAction = "open the strongest query match and narrow with neighboring symbols"
	} else if hasFailedCodeTriageQuery(meta.Queries) {
		guidance.NextAction = "repair or narrow failed queries before drawing conclusions"
	} else if len(meta.Checks) > 0 {
		guidance.NextAction = "checks passed or produced no parsed findings; broaden evidence only if the task remains unresolved"
	}
	guidance.CandidateFiles = codeTriageCandidateFiles(meta, 8)
	return guidance
}

func highestSeverityFinding(findings []CodeTriageFinding) CodeTriageFinding {
	best := findings[0]
	for _, finding := range findings[1:] {
		if codeTriageSeverityRank(finding.Severity) > codeTriageSeverityRank(best.Severity) {
			best = finding
		}
	}
	return best
}

func firstQueryMatch(queries []CodeTriageQueryResponse) *CodeTriageQueryFinding {
	for _, query := range queries {
		if len(query.TopMatches) == 0 {
			continue
		}
		return &query.TopMatches[0]
	}
	return nil
}

func hasFailedCodeTriageQuery(queries []CodeTriageQueryResponse) bool {
	for _, query := range queries {
		if query.Outcome == "failed" {
			return true
		}
	}
	return false
}

func codeTriageCandidateFiles(meta CodeTriageResponseMetadata, limit int) []string {
	if limit <= 0 {
		return nil
	}
	files := make([]string, 0, limit)
	seen := map[string]struct{}{}
	add := func(file string) {
		if file == "" || len(files) >= limit {
			return
		}
		if _, ok := seen[file]; ok {
			return
		}
		seen[file] = struct{}{}
		files = append(files, file)
	}
	for _, check := range meta.Checks {
		for _, finding := range check.Findings {
			add(finding.File)
		}
	}
	for _, query := range meta.Queries {
		for _, match := range query.TopMatches {
			add(match.File)
		}
	}
	return files
}

func codeTriageSeverityRank(severity string) int {
	switch severity {
	case codeTriageRiskHigh:
		return 3
	case codeTriageRiskMedium:
		return 2
	case codeTriageRiskLow:
		return 1
	default:
		return 0
	}
}

func normalizeCodeTriageIntent(intent string) string {
	switch strings.ToLower(strings.TrimSpace(intent)) {
	case "inspect", "understand", "locate_bug", "review", "verify", "refactor":
		return strings.ToLower(strings.TrimSpace(intent))
	case "bug", "debug", "triage":
		return "locate_bug"
	case "":
		return "inspect"
	default:
		return "inspect"
	}
}

func buildCodeTriageSummary(queryCount, checkCount, totalMatches int, risk string) string {
	if checkCount == 0 && totalMatches == 0 {
		return "No evidence or checks were executed."
	}
	if queryCount == 0 {
		return fmt.Sprintf("Triage summary: %d checks, risk=%s.", checkCount, risk)
	}
	if checkCount == 0 {
		return fmt.Sprintf("Triage summary: %d query sets, %d matches, risk=%s.", queryCount, totalMatches, risk)
	}
	return fmt.Sprintf("Triage summary: %d query sets, %d matches, %d checks, risk=%s.", queryCount, totalMatches, checkCount, risk)
}

func formatCodeTriageOutput(meta CodeTriageResponseMetadata) string {
	var sb strings.Builder
	sb.WriteString("## Code Triage\n\n")
	sb.WriteString(fmt.Sprintf("Intent: %s\n", meta.Intent))
	sb.WriteString(fmt.Sprintf("Risk: %s\n", meta.OverallRisk))
	sb.WriteString(fmt.Sprintf("Matches: %d\n", meta.TotalMatches))
	if meta.Guidance.NextAction != "" {
		sb.WriteString(fmt.Sprintf("Next: %s\n", meta.Guidance.NextAction))
	}
	if meta.Guidance.PrimaryFile != "" {
		fmt.Fprintf(&sb, "Focus: %s:%d %s\n", meta.Guidance.PrimaryFile, meta.Guidance.PrimaryLine, meta.Guidance.PrimaryMessage)
	}
	if len(meta.Queries) > 0 {
		sb.WriteString("\n### Queries\n")
		for _, q := range meta.Queries {
			fmt.Fprintf(&sb, "- %s [%s]: `%s` in %s (matches=%d", q.ID, cmpOr(q.Outcome, "passed"), q.Query, q.Path, q.Matches)
			if q.Truncated {
				sb.WriteString(", truncated")
			}
			sb.WriteString(")\n")
			if q.Error != "" {
				fmt.Fprintf(&sb, "  - ERROR: %s\n", q.Error)
				continue
			}
			for _, match := range q.TopMatches {
				fmt.Fprintf(&sb, "  - %s:%d:%d %s\n", match.File, match.Line, match.Column, match.Snippet)
			}
		}
	}
	if len(meta.Checks) > 0 {
		sb.WriteString("\n### Checks\n")
		for _, check := range meta.Checks {
			fmt.Fprintf(&sb, "- %s [%s] exit=%d duration=%dms\n", check.Name, check.Outcome, check.ExitCode, check.DurationMs)
			if len(check.Findings) > 0 {
				for _, finding := range check.Findings {
					prefix := ""
					if finding.File != "" {
						prefix = fmt.Sprintf(" (%s:%d)", finding.File, finding.Line)
					}
					sb.WriteString("  - ")
					sb.WriteString(strings.ToUpper(finding.Severity))
					sb.WriteString(prefix)
					sb.WriteString(": ")
					sb.WriteString(finding.Message)
					sb.WriteString("\n")
				}
			} else if check.Output != "" {
				sb.WriteString("  - ")
				sb.WriteString(truncateString(check.Output, 220))
				sb.WriteString("\n")
			}
		}
	}
	sb.WriteString("\n")
	sb.WriteString(meta.Summary)
	return sb.String()
}

func normalizeCodeTriageLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "", "shell", "sh", "bash":
		return "shell"
	case "python", "python3":
		return "python"
	case "node":
		return "node"
	default:
		return "shell"
	}
}

func clampDuration(seconds, fallback, max int) time.Duration {
	if seconds <= 0 {
		seconds = fallback
	}
	if seconds > max {
		seconds = max
	}
	return time.Duration(seconds) * time.Second
}

func clampInt(v, fallback, min, max int) int {
	if v <= 0 {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func cmpOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
