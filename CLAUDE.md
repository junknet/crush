# Crush Facade (L0 Entrypoint)

Go CLI/TUI agent runner. Bubble Tea v2 front end, Fantasy agent loop (charm.land),
sqlc/SQLite persistence, mvdan `sh/v3` interpreter for the embedded shell, and
MCP/LSP integration through generic evidence and resource tools.

---

## Build / Test / Format

`task` (taskfile.dev) drives everything; the file pins `CGO_ENABLED=0` and
`GOEXPERIMENT=greenteagc` at the env level (`Taskfile.yaml:19-21`). Lint tasks
override `GOEXPERIMENT: null` so golangci-lint doesn't choke on the experiment.

| Task | What it actually does |
|------|-----------------------|
| `task build` | Builds one binary (`-trimpath`, `-s -w`) into `$XDG_CACHE_HOME/crush-prod/crush` and installs **three launchers** to `$CRUSH_LOCAL_BIN_DIR` (default `~/.local/bin`): `crush` (normal), `crush-dev` (forces `--debug` + `--trace-file` + `CRUSH_ANTHROPIC_DUMP`, logs to `$XDG_STATE_HOME/crush-dev/`), and `crush-test` (rebuilds `main.go` from the current checkout on every launch). If `race.log` exists at repo root, the build gets `-race`. (`Taskfile.yaml:51-76`) |
| `task run` | `build` then exec the launcher with `{{.CLI_ARGS}}`; pipes stderr to `race.log` if race mode active. |
| `task test:latest` | `build` then exec `crush-test`, which rebuilds `main.go` from the current checkout on every launch and runs that fresh binary. Use this for local UI checks when you want the exact tree in your workspace, not the cached prod binary. |
| `task run:catwalk` | Same as `run` with `CATWALK_URL=http://localhost:8080` for local Catwalk dev. |
| `task run:onboarding` | Wipes `tmp/onboarding/` and runs against a scratch `CRUSH_GLOBAL_DATA`/`_CONFIG` so you can test first-run UX. |
| `task test` | `go test -race -failfast ./... {{.CLI_ARGS}}` — note the `-failfast`; use `-- -run Foo` to scope. |
| `task test:record` (alias `task record`) | **Deletes `internal/agent/testdata` then re-records VCR cassettes** via `go test -v -count=1 -timeout=1h ./internal/agent`. Don't run unless you intend to refresh fixtures. (`Taskfile.yaml:110-115`) |
| `task lint` | Runs `./scripts/check_log_capitalization.sh` first (custom rule: log strings must start capitalised) then `golangci-lint`. |
| `task lint:fix` | Same with `--fix`. |
| `task fmt` | `gofumpt -w .`. There is no separate `goimports` task. |
| `task fmt:html` | Prettier across `internal/cmd/stats/index.{html,css,js}` only. |
| `task modernize` | Runs `golang.org/x/tools/.../modernize -fix -test ./...`. |
| `task dev` | `go run .` with `CRUSH_PROFILE=true` (enables pprof on :6060). |
| `task profile:{cpu,heap,allocs}` | Opens pprof UI on `:6061` against a running dev process. |
| `task schema` | `go run main.go schema > schema.json` — regenerates the config JSON schema. |
| `task hyper` | `go generate ./internal/agent/hyper/...` — refreshes the embedded `provider.json` catalog. |
| `task sqlc` | `sqlc generate` against `internal/db/`. |
| `task swag` | Regenerates Swagger docs in `internal/swagger/` from `internal/server/*` and `internal/proto/*` annotations. |
| `task deps` | `GOPROXY=direct` update of `charm.land/fantasy` and `charm.land/catwalk` (bypass proxy lag). |
| `task release` | Tag-push from `main`, gated on clean tree and green `build.yml`/`snapshot.yml` workflows. Don't run casually. |

**Build inputs.** The `build` task's `sources:` list explicitly tracks
`internal/agent/**/*.md`, `*.md.tpl`, and `internal/agent/hyper/provider.json`;
these are `//go:embed`'d so editing them requires a rebuild even though they're
not `.go`.

**Go toolchain.** `go.mod` declares Go 1.26.3. `lint:install` pins
`GOTOOLCHAIN: go1.25.0` for the golangci-lint binary.

---

## Architecture (in-repo)

```
main.go → internal/cmd (Cobra)
        → internal/app (wiring)
            → internal/agent (coordinator + agents + tools)
            → internal/ui (Bubble Tea v2)
            → internal/lsp (powernap-backed manager)
            → internal/hooks (PreToolUse pipeline)
            → internal/skills (crush:// + filesystem skills)
            → internal/db (sqlc, goose migrations)
            → internal/shell (mvdan sh/v3 interpreter + custom dispatcher)
```

### Agent coordinator (`internal/agent/coordinator.go`)

Three named agents share one coordinator: `AgentBrain` (mandatory),
`AgentWorker`, `AgentExplore`. `NewCoordinator` returns
`errWorkerAgentNotConfigured` if brain is missing. Per-agent prompts are
rendered from `internal/agent/templates/{brain,worker,explore}.md.tpl` via
`promptForAgentRole()`.

Sub-agents are spawned through `internal/agent/agent_tool.go` using
`fantasy.NewParallelAgentTool` with `role` discriminating worker vs explore.
The explore sub-agent gets a read-only tool set; mutating tools live only on
the brain/worker surface.

A subset of OpenAI models (gpt-5.2 family) is routed to the **Responses API**
instead of Chat Completions — model whitelist near
`internal/agent/coordinator.go:73-80`. When wiring a new reasoning model,
check whether it belongs there.

### Tools (`internal/agent/tools/`)

No central registry; each tool is a constructor invoked during `buildAgent()`.
Context propagation uses keyed values: session ID, message ID, model name,
provider ID, task node ID (`tools.go:29-51`). New tools must accept this
context and emit task-node IDs into events so the sidebar/sub-agent sidecar
can attribute output.

Language-specific helper tools are intentionally not part of the built-in
surface. Prefer generic evidence/code-intelligence tools over one-off
`nim_*`-style tools; language-specific integrations should live behind MCP or
inside a generic reducer with a language adapter.

### MCP

Coordinator registers `ListMCPResourcesTool` and `ReadMCPResourceTool` when
any MCP server is configured (`coordinator.go:658-685`). Per-agent
`AllowedMCP` config filters the tool set advertised to each role.

### Hooks (`internal/hooks/`)

Currently only `PreToolUse` is wired (`hooks.go:14-16`). `runner.go` executes
all matching hooks in parallel with dedup; decisions are
`DecisionNone`/`Allow`/`Deny`. User-facing docs live in `docs/hooks/`. The
`.agents/skills/crush-hooks` skill describes the JSON contract — load it
before authoring hook fixtures.

### LSP manager (`internal/lsp/manager.go`)

Lazy client init with a 30-second back-off after a server fails
(`unavailableTimeout`). Defaults come from `powernap.Manager` and are merged
with user `lsp` config. **Gotcha**: `resolveServerName()` exists because users
often put a command name (e.g. `nimlangserver`) in the config where Crush
expects the canonical server name (`nim`); the HACK comment at
`manager.go:49` documents this. When debugging "LSP not starting", check that
mapping.

### Skills (`internal/skills/`)

Skills live in two places:

1. **Embedded builtins** under `internal/skills/builtin/{name}/SKILL.md`,
   surfaced through `embed.FS` with the `crush://skills/` prefix
   (`embed.go:12`). `DiscoverBuiltin()` walks the FS and prefixes paths so the
   View tool can resolve them.
2. **Project skills** under `.agents/skills/{name}/SKILL.md` (filesystem).

The `crush://` scheme is **not** a URL or MCP resource — it's an internal
identifier that the View tool understands natively. Pass it verbatim. Adding
a builtin: see `.agents/skills/builtin-skills/SKILL.md`.

### Shell (`internal/shell/`)

Built on `mvdan.cc/sh/v3`. The `scriptDispatchHandler` middleware
(`dispatch.go:29-60`) probes the first 128 bytes of `argv[0]` when it has a
path prefix:

- Shebang → resolve interpreter via PATH, `os/exec`.
- Binary magic (MZ / ELF / Mach-O / NUL) → pass through to default handler.
- Otherwise → parse as shell source and execute in-process via a nested
  `interp.Runner`.

Builtin command interception (e.g. coreutils replacements, `jq`) is wired via
`interp.ExecHandlerFunc` chains in `run.go`. New builtins: see
`.agents/skills/shell-builtins/SKILL.md`.

### UI (`internal/ui/`)

Bubble Tea v2 (`charm.land/bubbletea/v2`). Top-level model in
`internal/ui/model/ui.go` (large file). Chat rendering is split across focused
files in `internal/ui/chat/` — one per content kind (`bash.go`, `fetch.go`,
`mcp.go`, `todos.go`, etc.). When
adding a new tool with bespoke rendering, add a sibling file there and wire
it from `chat.go`.

Sidebar/header/status/pills/landing/skills/subagents components are under
`internal/ui/model/`. The sub-agent → sidebar event bus introduced in commit
`091aa63` flows through `internal/agent/event.go`.

### Persistence (`internal/db/`)

`sqlc generate` against SQLite migrations under `internal/db/migrations/`
(7 goose-style migrations). Never hand-edit generated `*.sql.go`.

### Acceptance tests (`acceptance/`)

`acceptance/run.sh` discovers `*.sh` and `*.py` scenarios under
`acceptance/scenarios/`. Default glob is `*`; pass an arg to filter. Exit 2
when no scenarios match; exit 0 only if all pass. Artifacts (screenshots,
JSONL traces, golden output) land in `acceptance/artifacts/`. Existing
scenarios include `smoke_landing.sh`, `nimlsp_restart_via_manager.sh`,
`dag_trace_fields.sh`, `nimlsp_custom_endpoints.py`.

### Golden tests

`internal/ui/diffview/` and similar use `github.com/charmbracelet/x/exp/golden`
with `golden.RequireEqual`. Testdata trees live next to the test file. The
update mechanism is the upstream package's `-update` flag; pass `-update`
through `go test`.

VCR cassettes for agent tests live in `internal/agent/testdata/` and are
managed only by `task test:record` — do not stage cassette diffs by hand
unless intentional.

---

## Config

**Single location, two files, no scopes.** Config lives ONLY in
`~/.config/crush/` (override dir via `CRUSH_GLOBAL_CONFIG`). There is no
project-level walk-up, no `.crush/` workspace config, and no
`~/.local/share/crush/` config scope — those were all removed. The split is by
**writer**, not by format:

- `crush.yaml` — **declarative**, hand-authored, you own it: `providers`
  (url/model catalog/`api_key` via `${ENV}` interpolation), `mcp`, `agents`,
  `options` (context window, auto-summarize threshold, tools), `skills`, and a
  `models` block that serves as the **default** model per role. Env
  interpolation (`${CRUSH_MOCK_LLM_BASE:-http://127.0.0.1:43917}`,
  `${CRUSH_MOCK_API_KEY:-${CRUSH_MOCK_KEY:?...}}`) is resolved at use time.
- `state.yaml` — **runtime state the app writes**, never hand-edit: current
  `models` selection (picker / think toggle / reasoning effort), `recent_models`,
  and oauth tokens / api keys obtained at runtime. It is merged at the **highest
  precedence**, so a selection here overrides the `crush.yaml` default. Created
  lazily on first write; delete it to fall back to the `crush.yaml` default.

`internal/config/file_format.go:isStateKey` decides routing: keys matching
`models.`, `recent_models.`, `providers.*.oauth`, `providers.*.api_key` go to
`state.yaml`; everything else goes to `crush.yaml`. `.json`/`.yml` variants are
still read if present; writes go to `.yaml`.

The old `config.Scope` type and the `crush.llm.yaml` sidecar are gone — do not
reintroduce either.

- `schema.json` — auto-generated JSON Schema; regen via `task schema` after
  touching the config Go structs.

Loading lives in `internal/config/load.go` (`lookupConfigs` →
`configCandidates(crush) ++ stateConfigCandidates(state)`); persistence routing
in `internal/config/store.go` (`configPathForKey`).

---

## Conventions / Gotchas

- **Log capitalisation lint.** `scripts/check_log_capitalization.sh` runs as
  part of `task lint`. All log/slog messages must start with a capital
  letter — including new ones. Lowercase first letter will fail CI.
- **`-failfast` in `task test`.** A single failure aborts the run; if you're
  triaging multiple failures, call `go test` directly.
- **Embedded asset rebuild.** Touching `internal/agent/templates/*.md.tpl`,
  any embedded `.md`, or `internal/agent/hyper/provider.json` requires
  `task build`; tests already pick them up.
- **One binary, two launchers from `task build`.** Both `~/.local/bin/crush`
  and `~/.local/bin/crush-dev` re-exec the same cached binary in
  `$XDG_CACHE_HOME/crush-prod/`. `crush-dev` differs only in the runtime
  flags it passes: `--debug` (slog Debug + HTTPRoundTripLogger), `--trace-file`
  (runtime task DAG JSONL), and `CRUSH_ANTHROPIC_DUMP` (raw Anthropic
  HTTP req/resp bodies). dev logs land in `$XDG_STATE_HOME/crush-dev/`.
  If `which crush` points at the launcher but behaviour is stale, the cached
  binary didn't get replaced — check `$XDG_CACHE_HOME/crush-prod/crush.tmp`.
- **Race mode opt-in.** Drop a `race.log` file at repo root to enable
  `-race` builds; `task run` will redirect stderr into it.
- **Coordinator role fallback.** A missing `AgentBrain` config is fatal
  (`coordinator.go:154-162`). Worker/explore are optional.
- **Known TODO/FIXME hot spots.** `internal/cmd/root.go` (slog-during-config
  ordering), `internal/ui/chat/assistant.go` (manual style application), and
  `internal/ui/dialog/models.go` (suspected config-mutation-during-read) all
  carry comments worth reading before touching nearby code.
---

## 架构边界与下钻导航 (external SSOTs — do not duplicate locally)

1.  **LSP/MCP 接口与交互契约** — [AGENT_GUIDE.md](file:///home/junknet/linege/nim-src/langserver/AGENT_GUIDE.md). Non-standard LSP methods (`nimCheckFile` 等), `Diagnostic.data` 格式, Agent helper contracts.
2.  **nim-core 元能力消费规约** — [docs/CLAUDE.md](file:///home/junknet/linege/nim-core/docs/CLAUDE.md). Result 错误流, 环境接入, CCache.
3.  **ncoder 调度设计 (DAG 扇区)** — [recursive_dag_design.md](file:///home/junknet/linege/nim-core/docs/ncoder/recursive_dag_design.md). Task/EvidenceRef/Ownership 三元组与调度队列。
4.  **全局命名与编写哲学** — [全局 CLAUDE.md](file:///home/junknet/.claude/CLAUDE.md)。

## 本地开发要求

- **可观测性**: UI/状态变更必须输出结构化 debug log（且首字符大写，见 lint 规则）。
- **独狼原则**: 单人开发，无下游消费者。陈旧接口/双跑逻辑双删，**不**保留 fallback 或多轨道兼容层。
- **CGO**: `CGO_ENABLED=0`、`GOEXPERIMENT=greenteagc` 由 Taskfile 强制，lint 任务显式置空。
