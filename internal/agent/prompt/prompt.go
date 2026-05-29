package prompt

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/memdir"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/skills"
)

// Prompt represents a template-based prompt generator.
type Prompt struct {
	name       string
	template   string
	now        func() time.Time
	platform   string
	workingDir string
}

type PromptDat struct {
	Provider      string
	Model         string
	Config        config.Config
	WorkingDir    string
	IsGitRepo     bool
	Platform      string
	ContextFiles  []ContextFile
	AvailSkillXML string
	// MemoryIndex is the rendered <auto_memory>...</auto_memory> block
	// from the per-workspace MEMORY.md index. Empty when no index exists
	// or the index was emptied to its header comment.
	MemoryIndex      string
	UserConstitution string
}

type ContextFile struct {
	Path    string
	Content string
}

type Option func(*Prompt)

func WithTimeFunc(fn func() time.Time) Option {
	return func(p *Prompt) {
		p.now = fn
	}
}

func WithPlatform(platform string) Option {
	return func(p *Prompt) {
		p.platform = platform
	}
}

func WithWorkingDir(workingDir string) Option {
	return func(p *Prompt) {
		p.workingDir = workingDir
	}
}

func NewPrompt(name, promptTemplate string, opts ...Option) (*Prompt, error) {
	p := &Prompt{
		name:     name,
		template: promptTemplate,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

func (p *Prompt) Build(ctx context.Context, provider, model string, store *config.ConfigStore) (string, error) {
	t, err := template.New(p.name).Parse(p.template)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var sb strings.Builder
	d, err := p.promptData(ctx, provider, model, store)
	if err != nil {
		return "", err
	}
	if err := t.Execute(&sb, d); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}

	return sb.String(), nil
}

func processFile(filePath string) *ContextFile {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return &ContextFile{
		Path:    filePath,
		Content: string(content),
	}
}

func processContextPath(p string, store *config.ConfigStore) []ContextFile {
	var contexts []ContextFile
	fullPath := filepathext.SmartJoin(store.WorkingDir(), p)
	info, err := os.Stat(fullPath)
	if err != nil {
		return contexts
	}
	if info.IsDir() {
		filepath.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				if result := processFile(path); result != nil {
					contexts = append(contexts, *result)
				}
			}
			return nil
		})
	} else {
		result := processFile(fullPath)
		if result != nil {
			contexts = append(contexts, *result)
		}
	}
	return contexts
}

// expandPath expands ~ and environment variables in file paths
func expandPath(path string, store *config.ConfigStore) string {
	path = home.Long(path)
	// Handle environment variable expansion using the same pattern as config
	if strings.HasPrefix(path, "$") {
		if expanded, err := store.Resolver().ResolveValue(path); err == nil {
			path = expanded
		}
	}

	return path
}

func (p *Prompt) promptData(ctx context.Context, provider, model string, store *config.ConfigStore) (PromptDat, error) {
	workingDir := cmp.Or(p.workingDir, store.WorkingDir())
	platform := cmp.Or(p.platform, runtime.GOOS)

	// Thin agents skip workspace-wide scaffolding that is expensive and
	// discoverable on demand. Context files still load for every role because
	// they carry project constitution and verification rules.
	isThinAgent := p.name == "explore" || p.name == "plan" || p.name == "agentic_fetch" || p.name == "speculation" || p.name == "worker"

	files := map[string][]ContextFile{}

	cfg := store.Config()
	for _, pth := range cfg.Options.ContextPaths {
		expanded := expandPath(pth, store)
		pathKey := strings.ToLower(expanded)
		if _, ok := files[pathKey]; ok {
			continue
		}
		content := processContextPath(expanded, store)
		files[pathKey] = content
	}

	// Discover and load skills metadata.
	var availSkillXML string

	if !isThinAgent {
		// Start with builtin skills.
		allSkills := skills.DiscoverBuiltin()
		builtinNames := make(map[string]bool, len(allSkills))
		for _, s := range allSkills {
			builtinNames[s.Name] = true
		}

		// Discover user skills from configured paths.
		if len(cfg.Options.SkillsPaths) > 0 {
			expandedPaths := make([]string, 0, len(cfg.Options.SkillsPaths))
			for _, pth := range cfg.Options.SkillsPaths {
				expandedPaths = append(expandedPaths, expandPath(pth, store))
			}
			for _, userSkill := range skills.Discover(expandedPaths) {
				if builtinNames[userSkill.Name] {
					slog.Warn("User skill overrides builtin skill", "name", userSkill.Name)
				}
				allSkills = append(allSkills, userSkill)
			}
		}

		// Deduplicate: user skills override builtins with the same name.
		allSkills = skills.Deduplicate(allSkills)

		// Filter out disabled skills.
		allSkills = skills.Filter(allSkills, cfg.Options.DisabledSkills)

		if len(allSkills) > 0 {
			availSkillXML = skills.ToPromptXML(allSkills)
		}
	}

	// Best-effort: ensure the per-workspace memdir exists so the model
	// has somewhere to write to. Failure is non-fatal — prompt assembly
	// must never block on disk operations.
	_ = memdir.EnsureWorkspace(cfg.Options.DataDirectory, store.WorkingDir())

	// Date and GitStatus are deliberately omitted here so that the static
	// system prompt hash stays stable across turns. They are produced per
	// turn by DynamicPrefix and injected as a user message prefix instead,
	// which keeps prompt caches warm across providers.
	var memoryIndex string
	// We deliberately skip MEMORY.md injection into the system prompt to maximize Prompt Cache hits.
	// Memories are dynamically recalled per-turn and attached to User message via SystemAttachments.
	_ = isThinAgent // satisfy unused lint check

	data := PromptDat{
		Provider:      provider,
		Model:         model,
		Config:        *cfg,
		WorkingDir:    filepath.ToSlash(workingDir),
		IsGitRepo:     isGitRepo(store.WorkingDir()),
		Platform:      platform,
		AvailSkillXML: availSkillXML,
		MemoryIndex:   memoryIndex,
	}

	// explore is a read-only fact-retrieval agent: it never writes code or
	// designs structure, and its parent (brain) already enforces the
	// constitution. Injecting the full ~17KB constitution would dwarf its
	// own ~2KB prompt for no behavioral gain. plan/worker/auditor still get
	// it because they produce or judge code.
	if p.name != "explore" {
		data.UserConstitution = loadUserConstitution()
	}

	for _, contextFiles := range files {
		data.ContextFiles = append(data.ContextFiles, contextFiles...)
	}
	return data, nil
}

func loadUserConstitution() string {
	path := filepath.Join(home.Dir(), ".claude", "CLAUDE.md")
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		return ""
	}
	return fmt.Sprintf("<file path=%q>\n%s\n</file>", filepath.ToSlash(path), text)
}

// DynamicNow is the time source used by DynamicPrefix; tests may replace it.
var DynamicNow = time.Now

// DynamicPrefix builds the per-turn environment block that is injected as a
// prefix to the current user prompt. Keeping date and git status out of the
// system prompt body is what makes the static prefix hash stable enough for
// Anthropic ephemeral cache, Gemini implicit prefix cache, and OpenAI
// PromptCacheKey routing to actually hit.
//
// Returned string ends with a trailing newline when non-empty so callers can
// concatenate the original user prompt directly.
func DynamicPrefix(ctx context.Context, store *config.ConfigStore) string {
	if os.Getenv("CRUSH_DISABLE_DYNAMIC_PREFIX") == "true" {
		return ""
	}
	var sb strings.Builder
	// Minute resolution: the wall-clock displayed here is the authoritative
	// answer to "what time is it" / "几点了" — brain must read this instead
	// of refusing on the AI-has-no-clock reflex. Cutting to the minute keeps
	// the prefix stable for ~60 cache-friendly seconds at a time, well within
	// Anthropic ephemeral cache's 5 min TTL.
	now := DynamicNow()
	sb.WriteString("<env_dynamic>\n")
	fmt.Fprintf(&sb, "今日日期: %s\n", now.Format("2006/1/2"))
	fmt.Fprintf(&sb, "当前本地时间: %s\n", now.Format("15:04 MST"))
	sb.WriteString("</env_dynamic>\n")

	if isGitRepo(store.WorkingDir()) {
		status, err := getGitStatus(ctx, store.WorkingDir())
		if err == nil && strings.TrimSpace(status) != "" {
			sb.WriteString("<git_status>\n")
			sb.WriteString(strings.TrimRight(status, "\n"))
			sb.WriteString("\n</git_status>\n")
		}
	}
	return sb.String()
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func getGitStatus(ctx context.Context, dir string) (string, error) {
	sh := shell.NewShell(&shell.Options{
		WorkingDir: dir,
	})
	branch, err := getGitBranch(ctx, sh)
	if err != nil {
		return "", err
	}
	status, err := getGitStatusSummary(ctx, sh)
	if err != nil {
		return "", err
	}
	commits, err := getGitRecentCommits(ctx, sh)
	if err != nil {
		return "", err
	}
	return branch + status + commits, nil
}

func getGitBranch(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git branch --show-current 2>/dev/null")
	if err != nil {
		return "", nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", nil
	}
	return fmt.Sprintf("当前分支: %s\n", out), nil
}

func getGitStatusSummary(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git status --short 2>/dev/null | head -20")
	if err != nil {
		return "", nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "状态: clean (无改动)\n", nil
	}
	return fmt.Sprintf("状态:\n%s\n", out), nil
}

func getGitRecentCommits(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git log --oneline -n 3 2>/dev/null")
	if err != nil || out == "" {
		return "", nil
	}
	out = strings.TrimSpace(out)
	return fmt.Sprintf("最近提交:\n%s\n", out), nil
}

func (p *Prompt) Name() string {
	return p.name
}
