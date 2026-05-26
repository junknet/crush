package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/x/vcr"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	_ "github.com/joho/godotenv/autoload"
)

// fakeEnv is an environment for testing.
type fakeEnv struct {
	workingDir  string
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	history     history.Service
	filetracker *filetracker.Service
	lspClients  *csync.Map[string, *lsp.Client]
}

type builderFunc func(t *testing.T, r *vcr.Recorder) (fantasy.LanguageModel, error)

type modelPair struct {
	name         string
	primaryModel builderFunc
	titleModel   builderFunc
}

func hyperBuilder(model string) builderFunc {
	return func(t *testing.T, r *vcr.Recorder) (fantasy.LanguageModel, error) {
		provider, err := openaicompat.New(
			openaicompat.WithBaseURL("https://hyper.charm.land/v1"),
			openaicompat.WithAPIKey(os.Getenv("CRUSH_HYPER_API_KEY")),
			openaicompat.WithHTTPClient(&http.Client{Transport: r}),
		)
		if err != nil {
			return nil, err
		}
		return provider.LanguageModel(t.Context(), model)
	}
}

func testEnv(t *testing.T) fakeEnv {
	workingDir := filepath.Join("/tmp/crush-test/", t.Name())
	os.RemoveAll(workingDir)

	err := os.MkdirAll(workingDir, 0o755)
	require.NoError(t, err)

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	q := db.New(conn)
	sessions := session.NewService(q, conn, workingDir)
	messages := message.NewService(q)

	permissions := permission.NewPermissionService(workingDir, []string{})
	history := history.NewService(q, conn)
	filetrackerService := filetracker.NewService(q)
	lspClients := csync.NewMap[string, *lsp.Client]()

	t.Cleanup(func() {
		conn.Close()
		os.RemoveAll(workingDir)
	})

	return fakeEnv{
		workingDir,
		sessions,
		messages,
		permissions,
		history,
		&filetrackerService,
		lspClients,
	}
}

func newTestRecorder(t *testing.T) *vcr.Recorder {
	cassetteName := filepath.Join("testdata", t.Name())
	r, err := recorder.New(
		cassetteName,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(systemPromptAwareMatcher(t)),
		recorder.WithSkipRequestLatency(true),
	)
	if err != nil {
		t.Fatalf("vcr: failed to create recorder: %v", err)
	}

	t.Cleanup(func() {
		if err := r.Stop(); err != nil {
			t.Errorf("vcr: failed to stop recorder: %v", err)
		}
	})

	return r
}

func systemPromptAwareMatcher(t *testing.T) recorder.MatcherFunc {
	return func(r *http.Request, i cassette.Request) bool {
		if r.Body == nil || r.Body == http.NoBody {
			return cassette.DefaultMatcher(r, i)
		}
		if r.Method != i.Method || r.URL.String() != i.URL {
			return false
		}

		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("vcr: failed to read request body")
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewBuffer(reqBody))

		requestContent := normalizeLineEndings(reqBody)
		cassetteContent := normalizeLineEndings(i.Body)
		if requestContent == cassetteContent {
			return true
		}

		var requestJSON any
		var cassetteJSON any
		if err := json.Unmarshal([]byte(requestContent), &requestJSON); err != nil {
			return false
		}
		if err := json.Unmarshal([]byte(cassetteContent), &cassetteJSON); err != nil {
			return false
		}

		normalizePromptPayload(requestJSON)
		normalizePromptPayload(cassetteJSON)
		if reflect.DeepEqual(requestJSON, cassetteJSON) {
			return true
		}
		t.Logf("Request interaction not found for %q.\nDiff:\n%s", t.Name(), cmp.Diff(cassetteJSON, requestJSON))
		return false
	}
}

func normalizePromptPayload(value any) {
	switch v := value.(type) {
	case map[string]any:
		if messages, ok := v["messages"].([]any); ok {
			for _, message := range messages {
				msgMap, ok := message.(map[string]any)
				if !ok {
					continue
				}
				normalizeMessagePayload(msgMap)
			}
		}
		if tools, ok := v["tools"].([]any); ok {
			for _, tool := range tools {
				if toolMap, ok := tool.(map[string]any); ok {
					normalizeToolPayload(toolMap)
				}
			}
		}
		for _, nested := range v {
			normalizePromptPayload(nested)
		}
	case []any:
		for _, nested := range v {
			normalizePromptPayload(nested)
		}
	}
}

func normalizeToolPayload(tool map[string]any) {
	recursiveClearDescription(tool)
}

func recursiveClearDescription(m map[string]any) {
	for k, v := range m {
		if k == "description" {
			m[k] = ""
		} else if nextMap, ok := v.(map[string]any); ok {
			recursiveClearDescription(nextMap)
		} else if nextSlice, ok := v.([]any); ok {
			for _, item := range nextSlice {
				if itemMap, ok := item.(map[string]any); ok {
					recursiveClearDescription(itemMap)
				}
			}
		}
	}
}

func normalizeMessagePayload(message map[string]any) {
	for key, value := range message {
		switch typed := value.(type) {
		case string:
			if key == "content" || key == "arguments" {
				message[key] = ""
			}
		case map[string]any:
			normalizeMessagePayload(typed)
		case []any:
			for _, nested := range typed {
				if nestedMap, ok := nested.(map[string]any); ok {
					normalizeMessagePayload(nestedMap)
				}
			}
		}
	}
}

func normalizeLineEndings[T string | []byte](s T) string {
	str := string(s)
	str = strings.ReplaceAll(str, "\r\n", "\n")
	str = strings.ReplaceAll(str, `\r\n`, `\n`)
	return str
}

func testSessionAgent(env fakeEnv, primary, title fantasy.LanguageModel, systemPrompt string, tools ...fantasy.AgentTool) SessionAgent {
	primaryModel := Model{
		Model: primary,
		CatwalkCfg: catwalk.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	titleModel := Model{
		Model: title,
		CatwalkCfg: catwalk.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	agent := NewSessionAgent(SessionAgentOptions{
		PrimaryModel: primaryModel,
		TitleModel:   titleModel,
		SystemPrompt: systemPrompt,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Tools:        tools,
		WorkingDir:   env.workingDir,
	})
	return agent
}

type nonInteractiveSessionAgent struct {
	SessionAgent
}

func (a nonInteractiveSessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	call.NonInteractive = true
	return a.SessionAgent.Run(ctx, call)
}

func workerAgent(r *vcr.Recorder, env fakeEnv, primary, title fantasy.LanguageModel) (SessionAgent, error) {
	fixedTime := func() time.Time {
		t, _ := time.Parse("1/2/2006", "1/1/2025")
		return t
	}
	prompt, err := workerPrompt(
		prompt.WithTimeFunc(fixedTime),
		prompt.WithPlatform("linux"),
		prompt.WithWorkingDir(filepath.ToSlash(env.workingDir)),
	)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Init(env.workingDir, "", false)
	if err != nil {
		return nil, err
	}

	// NOTE(@andreynering): Set a fixed config to ensure cassettes match
	// independently of user config on `$HOME/.config/crush/crush.json`.
	cfg.Config().Options.Attribution = &config.Attribution{
		TrailerStyle:  "co-authored-by",
		GeneratedWith: true,
	}

	// Clear some fields to avoid issues with VCR cassette matching.
	cfg.Config().Options.SkillsPaths = nil
	cfg.Config().Options.DisabledSkills = []string{"crush-config"}
	cfg.Config().Options.ContextPaths = nil
	cfg.Config().LSP = nil

	systemPrompt, err := prompt.Build(context.TODO(), primary.Provider(), primary.Model(), cfg)
	if err != nil {
		return nil, err
	}

	// Get the model name for the bash tool
	modelName := primary.Model() // fallback to ID if Name not available
	if model := cfg.Config().GetModel(primary.Provider(), primary.Model()); model != nil {
		modelName = model.Name
	}

	allTools := []fantasy.AgentTool{
		tools.NewBashTool(env.permissions, shell.NewBackgroundShellManager(), env.workingDir, cfg.Config().Options.DataDirectory, cfg.Config().Options.Attribution, modelName),
		tools.NewDownloadTool(env.permissions, env.workingDir, r.GetDefaultClient()),
		tools.NewEditTool(nil, env.permissions, env.history, *env.filetracker, env.workingDir),
		tools.NewMultiEditTool(nil, env.permissions, env.history, *env.filetracker, env.workingDir),
		tools.NewFetchTool(env.permissions, env.workingDir, r.GetDefaultClient()),
		tools.NewSearchTool(env.permissions, env.workingDir),
		tools.NewRgTool(env.permissions, env.workingDir, cfg.Config().Tools.Rg),
		tools.NewLsTool(env.permissions, env.workingDir, cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(r.GetDefaultClient()),
		tools.NewViewTool(nil, env.permissions, *env.filetracker, nil, env.workingDir),
		tools.NewWriteTool(nil, env.permissions, env.history, *env.filetracker, env.workingDir),
	}

	return nonInteractiveSessionAgent{SessionAgent: testSessionAgent(env, primary, title, systemPrompt, allTools...)}, nil
}

// createSimpleGoProject creates a simple Go project structure in the given directory.
// It creates a go.mod file and a main.go file with a basic hello world program.
func createSimpleGoProject(t *testing.T, dir string) {
	goMod := `module example.com/testproject

go 1.23
`
	err := os.WriteFile(dir+"/go.mod", []byte(goMod), 0o644)
	require.NoError(t, err)

	mainGo := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err = os.WriteFile(dir+"/main.go", []byte(mainGo), 0o644)
	require.NoError(t, err)
}
