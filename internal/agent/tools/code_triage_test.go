package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func runCodeTriageTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params CodeTriageParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  CodeTriageToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}

func TestCodeTriageRunsQueries(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "service.go"), []byte("package main\nfunc main() {\n// TODO: bug\n}\n"), 0o644))

	tool := NewCodeTriageTool(&mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runCodeTriageTool(t, tool, ctx, CodeTriageParams{
		Intent: "understand",
		Queries: []CodeTriageQuery{
			{
				ID:          "bugs",
				Query:       "TODO",
				Path:        workingDir,
				LiteralText: true,
			},
		},
	})

	require.False(t, resp.IsError)
	require.NotEmpty(t, resp.Content)

	var meta CodeTriageResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Len(t, meta.Queries, 1)
	require.Equal(t, "understand", meta.Intent)
	require.Equal(t, "bugs", meta.Queries[0].ID)
	require.Equal(t, "passed", meta.Queries[0].Outcome)
	require.GreaterOrEqual(t, meta.Queries[0].Matches, 1)
	require.Empty(t, meta.Checks)
	require.Equal(t, "low", meta.OverallRisk)
	require.NotEmpty(t, meta.Evidence)
	require.Equal(t, "open the strongest query match and narrow with neighboring symbols", meta.Guidance.NextAction)
	require.NotEmpty(t, meta.Guidance.PrimaryFile)
}

func TestCodeTriageCheckCommandFindings(t *testing.T) {
	workingDir := t.TempDir()

	tool := NewCodeTriageTool(&mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runCodeTriageTool(t, tool, ctx, CodeTriageParams{
		Intent: "debug",
		CheckCommands: []CodeTriageCheckCommand{
			{
				Name:    "smoke",
				Command: "printf 'src/main.go:10:2: panic: nil pointer'\\n",
			},
		},
	})

	require.False(t, resp.IsError)
	var meta CodeTriageResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Len(t, meta.Checks, 1)
	require.Equal(t, "locate_bug", meta.Intent)
	require.Equal(t, "high", meta.OverallRisk)
	require.GreaterOrEqual(t, len(meta.Checks[0].Findings), 1)
	require.Equal(t, "high", meta.Checks[0].Findings[0].Severity)
	require.Equal(t, "src/main.go", meta.Checks[0].Findings[0].File)
	require.Equal(t, 10, meta.Checks[0].Findings[0].Line)
	require.Equal(t, 2, meta.Checks[0].Findings[0].Column)
	require.Equal(t, "src/main.go", meta.Guidance.PrimaryFile)
	require.Equal(t, 10, meta.Guidance.PrimaryLine)
	require.Contains(t, meta.Guidance.NextAction, "primary finding")
}

func TestCodeTriageKeepsPartialQueryFailuresStructured(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "service.go"), []byte("package main\nfunc main() {}\n"), 0o644))

	tool := NewCodeTriageTool(&mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runCodeTriageTool(t, tool, ctx, CodeTriageParams{
		Queries: []CodeTriageQuery{
			{
				ID:    "bad-regex",
				Query: "(",
				Path:  workingDir,
			},
			{
				ID:          "good",
				Query:       "package",
				Path:        workingDir,
				LiteralText: true,
			},
		},
	})

	require.False(t, resp.IsError)
	var meta CodeTriageResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Len(t, meta.Queries, 2)
	require.Equal(t, "failed", meta.Queries[0].Outcome)
	require.NotEmpty(t, meta.Queries[0].Error)
	require.Equal(t, "passed", meta.Queries[1].Outcome)
	require.GreaterOrEqual(t, meta.Queries[1].Matches, 1)
	require.NotEmpty(t, meta.Evidence)
}

func TestBuildCodeTriageSummary(t *testing.T) {
	require.Equal(t, "No evidence or checks were executed.", buildCodeTriageSummary(0, 0, 0, codeTriageRiskLow))
	require.Equal(t, "Triage summary: 2 query sets, 7 matches, risk=low.", buildCodeTriageSummary(2, 0, 7, codeTriageRiskLow))
	require.Equal(t, "Triage summary: 3 checks, risk=medium.", buildCodeTriageSummary(0, 3, 0, codeTriageRiskMedium))
	require.Equal(t, "Triage summary: 2 query sets, 11 matches, 1 checks, risk=high.", buildCodeTriageSummary(2, 1, 11, codeTriageRiskHigh))
}

func TestParseCodeTriageLocation(t *testing.T) {
	tests := []struct {
		line    string
		file    string
		lineNum int
		colNum  int
		message string
	}{
		{
			line:    "/tmp/main.go:10:2: panic: nil pointer",
			file:    "/tmp/main.go",
			lineNum: 10,
			colNum:  2,
			message: "panic: nil pointer",
		},
		{
			line:    "src/app.go:88:42 expected true",
			file:    "src/app.go",
			lineNum: 88,
			colNum:  42,
			message: "expected true",
		},
		{
			line:    `File "/tmp/app.py", line 17, in main - NameError: x is not defined`,
			file:    "/tmp/app.py",
			lineNum: 17,
			colNum:  0,
			message: "NameError: x is not defined",
		},
		{
			line:    "at process (src/lib.rs:55:11)",
			file:    "src/lib.rs",
			lineNum: 55,
			colNum:  11,
			message: "",
		},
		{
			line:    "  8: MyClass.method (src/main.ts:77)",
			file:    "src/main.ts",
			lineNum: 77,
			colNum:  0,
			message: "",
		},
		{
			line: "src/index.js:33]",
			file: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			file, lineNum, colNum, message := parseCodeTriageLocation(tt.line)
			require.Equal(t, tt.file, file)
			require.Equal(t, tt.lineNum, lineNum)
			require.Equal(t, tt.colNum, colNum)
			if tt.message != "" {
				require.Equal(t, tt.message, message)
			} else if message != "" {
				require.Empty(t, strings.TrimSpace(message))
			}
		})
	}
}

func TestParseCodeTriageOutputForFindings(t *testing.T) {
	output := strings.Join([]string{
		"Traceback (most recent call last):",
		`  File "/app/main.py", line 12, in <module>`,
		"    func()",
		`NameError: name 'x' is not defined`,
		"",
		"/app/main.go:55:16: undefined: foo",
		"/app/main.go:55:16: undefined: foo",
		"    at main.main (/app/main.go:55)",
		"/app/other.go:12: warning: var is unused",
		`at module (/app/other.go:12)`,
		`  21: foo (/app/main.go:21)`,
		"/bin/compile: line 1: syntax error near unexpected token `}'",
	}, "\n")

	findings := parseCodeTriageOutputForFindings(output)
	require.GreaterOrEqual(t, len(findings), 2)

	byMessage := make(map[string]CodeTriageFinding, len(findings))
	for _, finding := range findings {
		byMessage[finding.Message] = finding
	}

	nameErr, ok := byMessage["Traceback (most recent call last):: NameError: name 'x' is not defined"]
	require.True(t, ok)
	require.Equal(t, "/app/main.py", nameErr.File)
	require.Equal(t, 12, nameErr.Line)
	require.Equal(t, codeTriageRiskMedium, nameErr.Severity)

	undefined := byMessage["undefined: foo"]
	require.Equal(t, "/app/main.go", undefined.File)
	require.Equal(t, 55, undefined.Line)
	require.Equal(t, 16, undefined.Column)

	warning := byMessage["warning: var is unused"]
	require.Equal(t, "/app/other.go", warning.File)
	require.Equal(t, 12, warning.Line)
	require.Equal(t, codeTriageRiskLow, warning.Severity)
}
