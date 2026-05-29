package tools

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/permission"
	"golang.org/x/sync/errgroup"
)

type DagRunParams struct {
	Nodes          []DagRunNode `json:"nodes" description:"DAG nodes to execute"`
	MaxParallel    int          `json:"max_parallel,omitempty" description:"Maximum concurrent ready nodes, default 4, max 16"`
	TimeoutSeconds int          `json:"timeout_seconds,omitempty" description:"Whole-DAG timeout in seconds, default 120, max 600"`
}

type DagRunNode struct {
	ID          string   `json:"id" description:"Unique node ID used by dependencies and output interpolation"`
	Kind        string   `json:"kind,omitempty" jsonschema:"description=Evidence node kind,enum=search_text,enum=search_files,enum=search_structure,enum=list_tree,enum=read_file,enum=check_file,enum=run_short_command,enum=web_search,enum=web_fetch"`
	Tool        string   `json:"tool,omitempty" description:"Deprecated compatibility field. Use kind instead."`
	DependsOn   []string `json:"depends_on,omitempty" description:"Node IDs that must complete before this node runs"`
	Query       string   `json:"query,omitempty" description:"Text, filename, or AST query depending on kind"`
	Pattern     string   `json:"pattern,omitempty" description:"Deprecated search pattern alias; use query"`
	Path        string   `json:"path,omitempty" description:"Workspace-relative file or directory path"`
	Include     string   `json:"include,omitempty" description:"Include glob for search_text or search_files"`
	LiteralText bool     `json:"literal_text,omitempty" description:"Treat search_text query as literal text"`
	Language    string   `json:"language,omitempty" description:"Language for search_structure or run_short_command: go, javascript, shell, node, etc."`
	Offset      int      `json:"offset,omitempty" description:"Line offset for read_file"`
	Limit       int      `json:"limit,omitempty" description:"Output line/item limit"`
	Fold        bool     `json:"fold,omitempty" description:"Fold read_file output semantically"`
	Depth       int      `json:"depth,omitempty" description:"Maximum list_tree depth"`
	Ignore      []string `json:"ignore,omitempty" description:"Ignore globs for list_tree"`
	Script      string   `json:"script,omitempty" description:"Short script for run_short_command"`
	Command     string   `json:"command,omitempty" description:"Short shell command alias for run_short_command"`
	OnFailure   string   `json:"on_failure,omitempty" jsonschema:"description=Failure policy,enum=continue,enum=skip_dependents,enum=stop_graph"`
	FilePath    string   `json:"file_path,omitempty" description:"Deprecated read_file path alias; use path"`
	FilesOnly   bool     `json:"files_only,omitempty" description:"Deprecated rg compatibility flag; use search_files kind"`
}

type DagRunResponse struct {
	DurationMs  int64               `json:"duration_ms"`
	MaxParallel int                 `json:"max_parallel"`
	Nodes       []DagRunNodeResult  `json:"nodes"`
	Summary     DagRunResultSummary `json:"summary"`
}

type DagRunNodeResult struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

type DagRunResultSummary struct {
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

const (
	DagRunToolName            = "dag_run"
	EvidenceBatchToolName     = "evidence_batch"
	EvidenceGraphToolName     = "evidence_graph"
	dagRunDefaultMaxParallel  = 4
	dagRunMaxParallel         = 16
	dagRunDefaultTimeout      = 120 * time.Second
	dagRunMaxTimeout          = 600 * time.Second
	dagRunShortCommandTimeout = 10 * time.Second
	dagRunNodeOutputMaxBytes  = 4000
	dagRunTotalOutputMaxBytes = 24000
)

//go:embed dag_run.md
var dagRunDescription string

var dagRunInterpolationPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_.-]+)\.output\}`)

func NewDagRunTool(lspManager *lsp.Manager, permissions permission.Service, workingDir string, httpClient *http.Client) fantasy.AgentTool {
	return newEvidenceGraphTool(DagRunToolName, dagRunDescription, lspManager, permissions, workingDir, httpClient, true)
}

func NewEvidenceGraphTool(lspManager *lsp.Manager, permissions permission.Service, workingDir string, httpClient *http.Client) fantasy.AgentTool {
	return newEvidenceGraphTool(EvidenceGraphToolName, evidenceGraphDescription(), lspManager, permissions, workingDir, httpClient, true)
}

func NewEvidenceBatchTool(lspManager *lsp.Manager, permissions permission.Service, workingDir string, httpClient *http.Client) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		EvidenceBatchToolName,
		evidenceBatchDescription(),
		func(ctx context.Context, params DagRunParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			for i := range params.Nodes {
				params.Nodes[i].DependsOn = nil
			}
			return runEvidenceNodes(ctx, lspManager, permissions, workingDir, httpClient, params, call, EvidenceBatchToolName)
		},
	)
}

func evidenceBatchDescription() string {
	return `Collect independent local evidence in one parallel tool call.

Use this for parallel repository inspection: text search, filename search,
structural search, directory listing, file reads, file diagnostics, and short
bounded verification commands.

Each node uses kind, not tool. Supported kind values:
- search_text: query + optional path/include/literal_text
- search_files: query + optional path/include
- search_structure: query + optional path/language
- list_tree: path + optional depth/ignore
- read_file: path + optional offset/limit/fold
- check_file: path
- run_short_command: script or command, optional language shell/node/python

Prefer evidence_batch when nodes are independent. Use evidence_graph only when
one node must depend on another node's output.`
}

func evidenceGraphDescription() string {
	return evidenceBatchDescription() + `

Dependency output interpolation:
- Use ${node_id.output} in query, path, script, command, or include.
- Dependents run only after dependencies complete.`
}

func newEvidenceGraphTool(toolName, description string, lspManager *lsp.Manager, permissions permission.Service, workingDir string, httpClient *http.Client, allowDependencies bool) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		toolName,
		description,
		func(ctx context.Context, params DagRunParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if !allowDependencies {
				for i := range params.Nodes {
					params.Nodes[i].DependsOn = nil
				}
			}
			return runEvidenceNodes(ctx, lspManager, permissions, workingDir, httpClient, params, call, toolName)
		},
	)
}

func runEvidenceNodes(ctx context.Context, lspManager *lsp.Manager, permissions permission.Service, workingDir string, httpClient *http.Client, params DagRunParams, call fantasy.ToolCall, toolName string) (fantasy.ToolResponse, error) {
	if len(params.Nodes) == 0 {
		return fantasy.NewTextErrorResponse("nodes is required"), nil
	}
	normalized := normalizeDagRunNodes(params.Nodes)
	if err := validateDagRunNodes(normalized); err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing %s", toolName)
	}
	granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		Path:        workingDir,
		ToolCallID:  call.ID,
		ToolName:    toolName,
		Action:      "execute",
		Description: fmt.Sprintf("Execute evidence graph with %d nodes", len(normalized)),
		Params:      params,
	})
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if !granted {
		return NewPermissionDeniedResponse(), nil
	}

	timeout := dagRunTimeout(params.TimeoutSeconds)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startedAt := time.Now()
	results := executeDagRun(runCtx, lspManager, workingDir, httpClient, normalized, dagRunMaxParallelValue(params.MaxParallel))
	response := buildDagRunResponse(startedAt, dagRunMaxParallelValue(params.MaxParallel), results)
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(formatEvidenceResponse(response)), response), nil
}

func validateDagRunNodes(nodes []DagRunNode) error {
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			return fmt.Errorf("evidence node id is required")
		}
		if _, ok := seen[node.ID]; ok {
			return fmt.Errorf("duplicate evidence node id: %s", node.ID)
		}
		seen[node.ID] = struct{}{}
		switch node.Kind {
		case "search_text", "search_files", "search_structure":
			if node.Query == "" {
				return fmt.Errorf("node %s: query is required for %s", node.ID, node.Kind)
			}
		case "read_file":
			if node.Path == "" {
				return fmt.Errorf("node %s: path is required for read_file", node.ID)
			}
		case "check_file":
			if node.Path == "" {
				return fmt.Errorf("node %s: path is required for check_file", node.ID)
			}
		case "web_search":
			if node.Query == "" {
				return fmt.Errorf("node %s: query is required for web_search", node.ID)
			}
		case "web_fetch":
			if node.Path == "" {
				return fmt.Errorf("node %s: path (url) is required for web_fetch", node.ID)
			}
		case "list_tree":
		case "run_short_command":
			if strings.TrimSpace(dagRunCommand(node)) == "" {
				return fmt.Errorf("node %s: script or command is required for run_short_command", node.ID)
			}
			if message, blocked := blockForegroundSleep(dagRunCommand(node)); blocked {
				return fmt.Errorf("%s", message)
			}
		default:
			return fmt.Errorf("node %s: unsupported kind %q; use search_text, search_files, search_structure, list_tree, read_file, check_file, run_short_command, web_search, or web_fetch", node.ID, node.Kind)
		}
		if node.OnFailure != "" && node.OnFailure != "continue" && node.OnFailure != "skip_dependents" && node.OnFailure != "stop_graph" {
			return fmt.Errorf("node %s: unsupported on_failure %q", node.ID, node.OnFailure)
		}
	}
	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			if _, ok := seen[dep]; !ok {
				return fmt.Errorf("node %s depends on unknown node %s", node.ID, dep)
			}
			if dep == node.ID {
				return fmt.Errorf("node %s cannot depend on itself", node.ID)
			}
		}
	}
	if hasDagCycle(nodes) {
		return fmt.Errorf("dag_run contains a dependency cycle")
	}
	return nil
}

func normalizeDagRunNodes(nodes []DagRunNode) []DagRunNode {
	normalized := make([]DagRunNode, len(nodes))
	for i, node := range nodes {
		if node.Kind == "" {
			node.Kind = legacyDagRunKind(node)
		}
		if node.Query == "" {
			node.Query = node.Pattern
		}
		if node.Path == "" {
			node.Path = node.FilePath
		}
		if node.OnFailure == "" {
			node.OnFailure = "continue"
		}
		normalized[i] = node
	}
	return normalized
}

func legacyDagRunKind(node DagRunNode) string {
	switch node.Tool {
	case "rg":
		if node.FilesOnly {
			return "search_files"
		}
		return "search_text"
	case "view":
		return "read_file"
	case "run", "shell":
		return "run_short_command"
	default:
		return node.Tool
	}
}

func hasDagCycle(nodes []DagRunNode) bool {
	byID := make(map[string]DagRunNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	visiting := make(map[string]bool, len(nodes))
	visited := make(map[string]bool, len(nodes))
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dep := range byID[id].DependsOn {
			if visit(dep) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	for _, node := range nodes {
		if visit(node.ID) {
			return true
		}
	}
	return false
}

func dagRunMaxParallelValue(value int) int {
	if value <= 0 {
		return dagRunDefaultMaxParallel
	}
	if value > dagRunMaxParallel {
		return dagRunMaxParallel
	}
	return value
}

func dagRunTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return dagRunDefaultTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > dagRunMaxTimeout {
		return dagRunMaxTimeout
	}
	return timeout
}

func executeDagRun(ctx context.Context, lspManager *lsp.Manager, workingDir string, httpClient *http.Client, nodes []DagRunNode, maxParallel int) map[string]DagRunNodeResult {
	remaining := make(map[string]DagRunNode, len(nodes))
	byID := make(map[string]DagRunNode, len(nodes))
	for _, node := range nodes {
		remaining[node.ID] = node
		byID[node.ID] = node
	}
	results := make(map[string]DagRunNodeResult, len(nodes))
	var resultsMu sync.Mutex
	sem := make(chan struct{}, maxParallel)

	for len(remaining) > 0 {
		if shouldStopDagRun(byID, results) {
			for id, node := range remaining {
				results[id] = DagRunNodeResult{ID: id, Kind: node.Kind, Status: "skipped", Error: "graph stopped by failed dependency policy"}
				delete(remaining, id)
			}
			break
		}
		ready := readyDagRunNodes(remaining, results, byID)
		if len(ready) == 0 {
			for id, node := range remaining {
				results[id] = DagRunNodeResult{ID: id, Kind: node.Kind, Status: "skipped", Error: "dependency failed or no node can run"}
				delete(remaining, id)
			}
			break
		}

		group, groupCtx := errgroup.WithContext(ctx)
		for _, node := range ready {
			node := node
			delete(remaining, node.ID)
			group.Go(func() error {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-groupCtx.Done():
					resultsMu.Lock()
					results[node.ID] = DagRunNodeResult{ID: node.ID, Kind: node.Kind, Status: "skipped", Error: groupCtx.Err().Error()}
					resultsMu.Unlock()
					return nil
				}
				resultsMu.Lock()
				dependencyOutputs := dagRunDependencyOutputs(node, results)
				resultsMu.Unlock()
				result := executeDagRunNode(groupCtx, lspManager, workingDir, httpClient, node, dependencyOutputs)
				resultsMu.Lock()
				results[node.ID] = result
				resultsMu.Unlock()
				return nil
			})
		}
		_ = group.Wait()
	}
	return results
}

func shouldStopDagRun(nodes map[string]DagRunNode, results map[string]DagRunNodeResult) bool {
	for id, result := range results {
		if result.Status == "failed" && nodes[id].OnFailure == "stop_graph" {
			return true
		}
	}
	return false
}

func readyDagRunNodes(remaining map[string]DagRunNode, results map[string]DagRunNodeResult, nodes map[string]DagRunNode) []DagRunNode {
	ready := make([]DagRunNode, 0)
	for _, node := range remaining {
		blocked := false
		for _, dep := range node.DependsOn {
			result, ok := results[dep]
			if !ok {
				blocked = true
				break
			}
			if result.Status == "completed" {
				continue
			}
			if result.Status == "failed" && nodes[dep].OnFailure == "continue" {
				continue
			}
			if result.Status != "completed" {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, node)
		}
	}
	slices.SortFunc(ready, func(left, right DagRunNode) int {
		return strings.Compare(left.ID, right.ID)
	})
	return ready
}

func dagRunDependencyOutputs(node DagRunNode, results map[string]DagRunNodeResult) map[string]string {
	outputs := make(map[string]string, len(node.DependsOn))
	for _, dep := range node.DependsOn {
		if result, ok := results[dep]; ok {
			outputs[dep] = result.Output
		}
	}
	return outputs
}

func executeDagRunNode(ctx context.Context, lspManager *lsp.Manager, workingDir string, httpClient *http.Client, node DagRunNode, dependencyOutputs map[string]string) DagRunNodeResult {
	startedAt := time.Now()
	result := DagRunNodeResult{ID: node.ID, Kind: node.Kind, Status: "completed"}
	output, err := executeDagRunNodeOutput(ctx, lspManager, workingDir, httpClient, interpolateDagRunNode(node, dependencyOutputs))
	result.DurationMs = time.Since(startedAt).Milliseconds()
	result.Output, result.Truncated = truncateDagRunOutput(output, dagRunNodeOutputMaxBytes)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	return result
}

func executeDagRunNodeOutput(ctx context.Context, lspManager *lsp.Manager, workingDir string, httpClient *http.Client, node DagRunNode) (string, error) {
	switch node.Kind {
	case "check_file":
		if node.Path == "" {
			return "", fmt.Errorf("path is required for check_file")
		}
		absPath, err := filepath.Abs(filepathext.SmartJoin(workingDir, node.Path))
		if err != nil {
			return "", fmt.Errorf("failed to get absolute path: %w", err)
		}
		// Ensure LSP is started for this file.
		lspManager.Start(ctx, absPath)

		if lspManager.Clients().Len() == 0 {
			return "", fmt.Errorf("no LSP clients available")
		}
		notifyLSPs(ctx, lspManager, absPath)
		fileDiags := collectFileDiagnostics(absPath, lspManager)
		if len(fileDiags) == 0 {
			return fmt.Sprintf("%s: clean (0 diagnostics).", absPath), nil
		}
		sortDiagnostics(fileDiags)
		errors := countSeverity(fileDiags, "Error")
		warnings := countSeverity(fileDiags, "Warn")
		hints := countSeverity(fileDiags, "Hint")
		var out strings.Builder
		fmt.Fprintf(&out, "%s — %d error(s), %d warning(s), %d hint(s):\n", absPath, errors, warnings, hints)
		for _, d := range fileDiags {
			out.WriteString(d)
			out.WriteString("\n")
		}
		return out.String(), nil
	case "search_text":
		searchPattern := node.Query
		if node.LiteralText {
			searchPattern = escapeRegexPattern(searchPattern)
		}
		searchPath := filepathext.SmartJoin(workingDir, node.Path)
		matches, truncated, err := RgSearch(ctx, searchPattern, searchPath, node.Include, 100)
		if err != nil {
			return "", err
		}
		return formatDagRunRgMatches(matches, truncated), nil
	case "search_files":
		searchPath := filepathext.SmartJoin(workingDir, node.Path)
		matches, _, err := RgSearchFiles(ctx, node.Query, searchPath, node.Include, dagRunLimit(node.Limit, 100))
		if err != nil {
			return "", err
		}
		if len(matches) == 0 {
			return "No files found", nil
		}
		var out strings.Builder
		for _, match := range matches {
			fmt.Fprintf(&out, "%s\n", filepath.ToSlash(match.Path))
		}
		return strings.TrimRight(out.String(), "\n"), nil
	case "search_structure":
		searchPath := filepathext.SmartJoin(workingDir, node.Path)
		resp, err := runAstGrepScan(ctx, AstGrepParams{
			Pattern: node.Query,
			Path:    searchPath,
			Lang:    node.Language,
		}, searchPath, config.ToolAstGrep{Timeout: durationPtr(dagRunShortCommandTimeout)})
		if err != nil {
			return "", err
		}
		if resp.IsError {
			return resp.Content, fmt.Errorf("%s", resp.Content)
		}
		return resp.Content, nil
	case "list_tree":
		searchPath, err := resolveDagRunWorkspacePath(workingDir, node.Path)
		if err != nil {
			return "", err
		}
		output, _, err := ListDirectoryTree(ctx, searchPath, LSParams{
			Path:   searchPath,
			Ignore: node.Ignore,
			Depth:  node.Depth,
		}, config.ToolLs{})
		return output, err
	case "read_file":
		filePath, err := resolveDagRunWorkspacePath(workingDir, node.Path)
		if err != nil {
			return "", err
		}
		limit := node.Limit
		if limit <= 0 {
			limit = 200
		}
		content, _, hasMore, err := readTextFile(ctx, filePath, node.Offset, limit, MaxViewSize, node.Fold)
		if err != nil {
			return "", err
		}
		if hasMore {
			content += "\n(File has more lines.)"
		}
		return content, nil
	case "run_short_command":
		language := strings.ToLower(strings.TrimSpace(node.Language))
		if language == "" {
			language = "shell"
		}
		runCtx, cancel := context.WithTimeout(ctx, dagRunShortCommandTimeout)
		defer cancel()
		stdout, stderr, err := executeRunScript(runCtx, workingDir, language, dagRunCommand(node))
		output := formatOutput(stdout, stderr, err)
		if runCtx.Err() == context.DeadlineExceeded {
			output = strings.TrimSpace(output + "\nrun_short_command timed out after " + dagRunShortCommandTimeout.String())
		}
		return output, err
	case "web_search":
		if node.Query == "" {
			return "", fmt.Errorf("query is required for web_search")
		}
		maxResults := node.Limit
		if maxResults <= 0 {
			maxResults = 10
		}
		if maxResults > 20 {
			maxResults = 20
		}
		results, err := searchDuckDuckGo(ctx, httpClient, node.Query, maxResults)
		if err != nil {
			return "", err
		}
		return formatSearchResults(results), nil
	case "web_fetch":
		if node.Path == "" {
			return "", fmt.Errorf("url (path) is required for web_fetch")
		}
		content, err := FetchURLAndConvert(ctx, httpClient, node.Path)
		if err != nil {
			return "", err
		}
		return content, nil
	default:
		return "", fmt.Errorf("unsupported kind %q", node.Kind)
	}
}

func dagRunCommand(node DagRunNode) string {
	if strings.TrimSpace(node.Script) != "" {
		return node.Script
	}
	return node.Command
}

func dagRunLimit(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func durationPtr(value time.Duration) *time.Duration {
	return &value
}

func interpolateDagRunNode(node DagRunNode, outputs map[string]string) DagRunNode {
	interpolate := func(value string) string {
		if value == "" {
			return value
		}
		return dagRunInterpolationPattern.ReplaceAllStringFunc(value, func(match string) string {
			parts := dagRunInterpolationPattern.FindStringSubmatch(match)
			if len(parts) != 2 {
				return match
			}
			if output, ok := outputs[parts[1]]; ok {
				return output
			}
			return ""
		})
	}
	node.Pattern = interpolate(node.Pattern)
	node.Query = interpolate(node.Query)
	node.Path = interpolate(node.Path)
	node.Include = interpolate(node.Include)
	node.Language = interpolate(node.Language)
	node.Script = interpolate(node.Script)
	node.Command = interpolate(node.Command)
	node.FilePath = interpolate(node.FilePath)
	return node
}

func resolveDagRunWorkspacePath(workingDir, target string) (string, error) {
	filePath := filepathext.SmartJoin(workingDir, target)
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return "", err
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absWorkingDir, absFilePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("dag_run view cannot read outside working directory: %s", target)
	}
	return absFilePath, nil
}

func formatDagRunRgMatches(matches []RgMatch, truncated bool) string {
	if len(matches) == 0 {
		return "No files found"
	}
	var out strings.Builder
	for _, match := range matches {
		fmt.Fprintf(&out, "%s:%d:%d:%s\n", filepath.ToSlash(match.Path), match.LineNum, match.CharNum, strings.TrimSpace(match.LineText))
	}
	if truncated {
		out.WriteString("(Results are truncated.)\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func buildDagRunResponse(startedAt time.Time, maxParallel int, results map[string]DagRunNodeResult) DagRunResponse {
	ids := make([]string, 0, len(results))
	for id := range results {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	response := DagRunResponse{
		DurationMs:  time.Since(startedAt).Milliseconds(),
		MaxParallel: maxParallel,
		Nodes:       make([]DagRunNodeResult, 0, len(ids)),
	}
	totalBytes := 0
	for _, id := range ids {
		result := results[id]
		switch result.Status {
		case "completed":
			response.Summary.Completed++
		case "failed":
			response.Summary.Failed++
		case "skipped":
			response.Summary.Skipped++
		}
		if result.Output != "" {
			remaining := dagRunTotalOutputMaxBytes - totalBytes
			if remaining <= 0 {
				result.Output = ""
				result.Truncated = true
			} else {
				output, truncated := truncateDagRunOutput(result.Output, remaining)
				result.Output = output
				result.Truncated = result.Truncated || truncated
				totalBytes += len(output)
			}
		}
		response.Nodes = append(response.Nodes, result)
	}
	return response
}

func formatEvidenceResponse(response DagRunResponse) string {
	var out strings.Builder
	out.WriteString("[evidence]\n")
	for _, node := range response.Nodes {
		fmt.Fprintf(&out, "id=%s kind=%s status=%s duration_ms=%d", node.ID, node.Kind, node.Status, node.DurationMs)
		if node.Truncated {
			out.WriteString(" truncated=true")
		}
		out.WriteByte('\n')
		if node.Error != "" {
			fmt.Fprintf(&out, "error=%s\n", strings.TrimSpace(node.Error))
		}
		if strings.TrimSpace(node.Output) != "" {
			out.WriteString(strings.TrimRight(node.Output, "\n"))
			out.WriteByte('\n')
		}
		out.WriteByte('\n')
	}
	fmt.Fprintf(
		&out,
		"[summary]\ncompleted=%d failed=%d skipped=%d duration_ms=%d max_parallel=%d",
		response.Summary.Completed,
		response.Summary.Failed,
		response.Summary.Skipped,
		response.DurationMs,
		response.MaxParallel,
	)
	return out.String()
}

func truncateDagRunOutput(output string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", output != ""
	}
	if len(output) <= maxBytes {
		return output, false
	}
	return output[:maxBytes] + "\n<truncated>", true
}
