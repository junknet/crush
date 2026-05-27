# Changelog

> Crush 本仓库的改动 log,按时间倒序。模块代号是唯一稳定 ID,提 bug / 回滚都按这个走。
> 详细技术说明见 `docs/walkthrough.md`,测试同学验收清单见 `docs/QA_acceptance.md`。

---

## Wave 6 — Memory recall & context runtime (2026-05-26)

### Added
- **Project memory recall 注入** (`internal/memdir/scan.go`, `internal/agent/coordinator.go`) — brain turn 会扫描项目 memory manifest,按当前 prompt 选取最多 5 个相关 memory 文件作为 attachment 注入;单文件 200 行 / 25KB 截断,支持 CJK bigram 匹配,避免把整个 memory 目录无差别塞进上下文。
- **Session memory 实时压缩底座** (`internal/agent/session_memory.go`) — 成功 brain turn 后后台维护 `<dataDir>/sessions/<sessionID>/session-memory/summary.md`;10K token 初始化,5K token 增量,3 tool call 阈值。自动压缩前优先用 session memory 生成 summary message,失败再回退旧 LLM summarizer。
- **Memory extraction manifest 预注入** (`internal/agent/coordinator.go`) — 后台 memory extraction 先读项目 memory manifest,再并行读写目标 memory 文件,并强制输出落在项目 memory 目录内。

### Changed
- **Context runtime 可观测性闭环** (`internal/agent/llm_trace.go`, `internal/ui/model/status.go`) — LLM request trace 记录 context message/token/window/auto-threshold/tool-schema/attachment 指标;TUI 底栏显示 `ctx used/window auto@threshold`,用于确认 70% 自动压缩触发线。

### Tests
- `go test ./internal/memdir ./internal/agent` ✅
- `go test ./...` ✅
- `CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -trimpath -o /tmp/crush-memory-verify .` ✅
- `tui-test` real PTY run ✅ — `/tmp/crush-tui-memory-e2e/screen.txt`, `/tmp/crush-tui-memory-e2e/screen.png`, trace `/home/junknet/.local/state/crush-dev/trace-20260526-195038.jsonl`;屏幕返回 `MEMORY_RECALL_OK`,底栏显示 `dag 1 done · ctx 3% 33.3K/1M auto@70%`,trace 有 `attachment_count=1` 与 `task_output success=true`。

### Module IDs
- `memory_recall_v2` — prompt 相关 memory attachment 注入
- `session_memory_v1` — turn 后实时 session memory + compact 前复用
- `memory_extract_manifest_v1` — extraction 前 manifest 预注入 + memory 目录边界
- `context_runtime_meter_v1` — TUI ctx 百分比 + 70% threshold trace

---

## Wave 5 — TUI UX 闭环 & launcher 韧性 (2026-05-25)

### Added
- **`/accept` / `/run` / `/cancel-plan` / `/exit-plan` slash commands** (`ui.go:2174`) — plan-mode 闭环最后一公里。`/accept` 把会话 mode 切回 execute 并自动 `sendMessage("Implement the plan you produced above ...")`,plan 文本留在上下文里,brain 接手实施。`/cancel-plan` 静默退出 plan。
- **Plan EndTurn 自动预填 `/accept`** (`ui.go:1278`) — plan agent 一结束 turn,textarea 空时直接塞入 `/accept`,toast 用 `InfoTypeSuccess` 绿色高亮 + 15s TTL 提示"按 Enter 跑 /accept"。一键闭环。
- **Active model 行右侧 ✓ 标记** (`models_item.go`) — Switch Model 对话框现在用 `t.ToolCallSuccess` 在当前生效的 model 行右侧渲染绿色 ✓,光标焦点和"已生效"两个状态视觉解耦。`SetActive(bool)` 通过 `info` 槽走原有 renderItem,零侵入。

### Changed
- **Switch Model: Plan → Auditor tab** (`models.go:22-27`) — 4-tab 由 `Brain · Plan · Worker · Explore` 改为 `Brain · Worker · Auditor · Explore`。plan 角色现在跟随 brain(`config.SelectedModelForType` 已有 fallback),腾出的 slot 让 auditor 显式可配置。enum 顺序、`String()`/`Config()`/`Placeholder()`、radio strip 同步重排,`% 4` 魔数不变。
- **Chat sticky-bottom autoscroll**:
  - `AppendMessages` 追加完无条件调 follow-aware `ScrollToBottom()` (`chat.go:271`),tool call / 流式 chunk 自动贴底;用户上滑后 `follow=false` 仍会停。
  - `sendMessageMsg` / `RelayPromptMsg` 用 `ForceScrollToBottom` 替代 `ScrollToBottom` (`ui.go:707`) — 用户明确发消息 = 强意图回底,buffered 滚轮事件不再可能在 commit 之间抢回旧位置。free-code REPL `repinScroll` 等价模式。
- **ESC cancel 保 draft** (`ui.go:3670`,`cancelAgent`) — 按 ESC 取消 agent 时,把 textarea 当前内容 snapshot 到 `promptHistory.draft` 并 `index=-1`。之前按 ↑ 会直接吞掉未发送文本跳到上一条 user msg;现在 ESC 后 ↑ 从草稿开始往回翻。
- **Sub-agent narration ban + MaxTurns 8→16** (`config.go:868`, `explore.md.tpl`) — 复杂调研常常 8 turn 不够,explore 中途吐出 "现在查看 X..." 这种 narration 然后 budget 用完,parent 看着像"返回不完整"。MaxTurns 提到 16,prompt 显式 ban "tool 之间写 prose",final report 才允许 prose。
- **`rg` mandate** (`bash.md.tpl`, `grep.md.tpl`, `plan.md.tpl`, `explore.md.tpl`) — 提示词显式禁用 `grep` / `find`,要求用 `rg` (Grep 工具内部就是 ripgrep) 和 `rg files_only=true`。bash 的 usage_notes 同步改成"NEVER use grep/find"。
- **Attachment chip 缩进对齐 prompt** (`ui.go:renderEditorView`) — `[image]` chip 用 `lipgloss.PaddingLeft(promptWidth)` 缩进 4 列对齐 `::: ` prompt 列,视觉上 chip 像输入文字的延续(free-code inline-token 风格的轻量复刻)。
- **Plan agent 提示词** (`plan.md.tpl`) — 写明闭环:plan 结束后 UI 自动预填 `/accept`,plan 要写得 self-contained(brain 接手能不问就实施)。output 块改成强制结构化七段。

### Fixed
- **`http_dump.go` 双重 panic** (`http_dump.go:108,154`) — `drainBody` 在 ReadFrom/Close 失败时返回 `nil, b, err`;`io.ReadAll(nil)` 会 panic,把整个 TUI 拖垮。req/resp 两条路径都加 `if savedXxx != nil` 守卫。触发场景:Anthropic CCH transport (cch.go:101) 已经 Drain 过 body 再传给 dumpTransport。`crush-dev` 启动 anthropic provider 时必现。
- **launcher 在 mvdan/sh 下打印源码** (`scripts/launch_crush.sh`, `launch_crush_dev.sh`) — 旧 launcher 用 `zsh -lic '...heredoc...'`,crush 内嵌 bash 工具走 `mvdan/sh` 解释器时把整段大字符串当 token,glob `'*.go'` 触发 `no matches found`,直接把 launcher 源码 echo 到屏。两个 launcher 改成纯 POSIX sh,`find -newer` 替代 zsh while-stat 循环,ANSI 用 `\033`。
- **`launch_crush.sh` smart auto-rebuild** — prod launcher 现在检测源码 mtime > binary mtime 自动 `go build`,避免"我刚改了代码但 crush 还跑旧版"的 footgun。无变化时跳过(0 cost)。crush-dev 维持每次 unconditional rebuild(用户期望的 "always latest")。

### Module IDs (本轮稳定 ID)
- `models_dialog_v2` — Switch Model 4-tab 重排 + active ✓
- `chat_sticky_bottom_v2` — autoscroll force / follow-aware 分离
- `plan_mode_loop_v1` — /accept 闭环
- `esc_draft_snapshot_v1` — ESC 保 draft
- `subagent_narration_ban_v1` — explore turns + prompt
- `rg_mandate_v1` — grep/find 工具禁用
- `http_dump_nil_guard_v1` — panic fix
- `launcher_posix_v1` — POSIX 兼容 + smart rebuild

---

## Wave 4 — Polishing & Cancel-discard (2026-05-24)

### Added
- **dag_trace_fields_failures.sh** — acceptance: 失败路径回归(sub-agent ls 探针 → command_failed → 通过 `propagateSubAgentTraces` 抵达 parent trace)。10/10 断言。
- **propagateSubAgentTraces** + **preBindTaskTreeModels** (`coordinator.go`) — sub-agent 在 cloned RuntimeSession 跑完后,trace 拷回 parent;dispatch 前递归绑 model/provider 到 task tree。

### Changed
- **Cancel = discard** (`agent.go`):
  - 不再 prepend `interruptMarker` 到 `call.Prompt` → DB 存的是用户原文,**上下方向键回滚不再被 `[Previous turn was interrupted ...]` 污染**。
  - `preparePrompt` 加 pre-pass:cancelled assistant + 它对应的 user msg 都从 LLM history 跳过;tool 残留通过现有 orphan filter 兜底。
  - 现状:cancel 等同"撤销"——LLM 视角整轮当没发生。
- **#19 SessionMeta** 加 `Provider` / `Model` / `Models map[role]SelectedModel` / `AvailableModels []SelectedModel` (`relay.go`):presenceLoop 每 5s 把 brain 当前 model 写进去,顺带 publish 整个 model catalog 给 mobile picker。
- **#20 mobile 自动追最 recent alive session** (`index.tsx`):当前选中的 session 已 dead → 自动跳到 `updated_at desc` 的 alive 第一名。不再卡在旧 session 上跟 TUI 内容对不上。

### Fixed
- **#16 mobile session offline 状态渲染** (`api.ts` + `index.tsx`):listSessions 加 8s `kv.keys()` reconcile timer,drop 消失的 + stale (>12s) 标 alive=false;UI `isSessionOnline` 改用 `session.alive` 而非全局 `isConnected`。
- **mobile model badge "未就绪" bug**:`activeSession?.model` 进 fallback chain;依赖上面 #19 把 model 字段填进 presence。

### Docs
- `CHANGELOG.md` (本文)
- `docs/walkthrough.md` 更新到 wave 4。

---

## Wave 3 — Edits + Mobile UX (2026-05-23)

### Added — F 系列(Edit + Mobile)
| 代号 | 描述 | 主要文件 |
|:---|:---|:---|
| **F1** | Edit "old_string not found" 智能诊断:fuzzy hit excerpt + whitespace 可视化 (`·`/`→`/`¶`) | `tools/edit_diagnostic.go` |
| **F3** | Edit 三层归一化:curly↔straight quotes + 19 条 sanitized token (`<fnr>`→`<function_results>` 等) + markdown 外的 trailing whitespace strip | `tools/edit_normalize.go` |
| **F7** | Mobile model picker → state.yaml 同步:role chip + provider/model 输入框 + NATS `set_model` 命令 + relay `applySetModel` 写 `~/.config/crush/state.yaml` | `relay.go` + mobile `app/index.tsx` + `api.ts` |
| **F8** | Mobile 抽屉 cwd 二级折叠:同 pwd 多 session 分组 + chevron 折叠 + `alive/total` 数 | mobile `index.tsx` |

---

## Wave 2 — Async + 渐进装载 (2026-05-23)

### Added — F5 / F6
| 代号 | 描述 | 主要文件 |
|:---|:---|:---|
| **F5** | ToolSearch + deferred MCP 渐进装载:MCP 工具默认只送 name+desc;`tool_search` 按 `select:` / 关键词 / `+term` 加载 schema。system-reminder 每轮注入 deferred 列表 + connecting servers | `tools/deferred.go` + `tools/tool_search.go` |
| **F6** | 持久 Cron + 统一事件总线:`schedule_wakeup` 支持 5 字段 cron;落盘 `scheduled_tasks.json`;1s tick + 抖动(±10%/15min 上限/整点±90s)+ 7天 TTL;bash done / monitor / cron fire publish 到 eventbus;PrepareStep drain 为 `<task-notification>` 注入 | `internal/eventbus/` + `tools/cron.go` + `task_notification.go` |

---

## Wave 1 — Core overhaul (2026-05-23)

### Added — M 系列
| 代号 | 描述 | 主要文件 |
|:---|:---|:---|
| **M1** | DYNAMIC BOUNDARY:`env_dynamic` (date,后续加 time minute 精度) 从 system prompt 移到当轮 user msg 前缀;system prefix hash 全天稳定 | `prompt.go DynamicPrefix()` + 4 模板 |
| **M2** | 三厂商 LLM cache 统一抽象:Anthropic `cache_control:ephemeral` + OpenAI Chat/Responses `prompt_cache_key=sessionID` + Gemini 隐式 (≥1024 token);env switch `CRUSH_DISABLE_OPENAI_CACHE_KEY` | `prompt/cached.go` + `coordinator.getProviderOptions` |
| **M3** | Brain → Explore 决策表:`<delegation_decision>` if-then 表 + 校准成本对比 + agent_tool.md 加 cost 提示 | `brain.md.tpl` + `agent_tool.md` |
| **M4** | Bash 保头丢尾 + 落盘:`MaxOutputLength=30000` 头保留尾截断;>30KB spill 到 `.crush/tool-results/<sess>/bash-*.log`;blocker 加 `watch` / `tail -f/--follow`;7天 lazy GC | `tools/bash.go` |
| **M5** | microCompact 工具结果清理:>50KB 且 >5min 未引用 → 替换占位符;不走 LLM;复用 spill 路径 | `microcompact.go` |
| **M7** | memdir 注入:`~/.crush/projects/<slug>/memory/MEMORY.md` 自动建,seed frontmatter;200 行/25KB 截断;brain 静态段尾 inject | `internal/memdir/` |

### Decided — NOT to do (明确否决)
| 项 | 否决理由 |
|:---|:---|
| extractMemories 后台 forked agent | 用户已有全局 `~/.claude` memdir,手维护够 |
| outputStyles 三套预设 (Explanatory/Learning) | 独狼场景无团队/教学受众 |
| Task* 工具套件持久化 + DAG | `todos.go` 已满足 |
| sg / sd / nushell | Crush 不做 AST 重构;shell 是子进程不是宿主 |
| 独立文件查找工具 | rg files_only 已 cover 99%,普通项目差距 < 5% |
| Notification / PostToolUse / Stop hooks | 仅 PreToolUse 在用,无业务驱动 |

---

## 测试基线 (实测)

| 指标 | 期望 | 实测 |
|:---|:---|:---|
| Anthropic cache 命中率(第 3 轮起) | ≥ 0.6 | **0.77** |
| OpenAI cached_tokens 比例(第 2 轮起) | ≥ 0.3 | 0.45+ |
| Bash spill 落盘时间(20K 行) | < 200ms | < 80ms |
| Mobile → TUI 同步延迟 | < 1s | < 500ms |
| Session offline → 红 dot | 12-13s | 13s |
| Dead session 从抽屉移除 | 20-21s | 20-22s |
| set_model → state.yaml 写入 | < 500ms | < 100ms |

---

## Acceptance 套件

`acceptance/run.sh` 一键全跑:

- `smoke_landing.sh` ✅
- `nimlsp_restart_via_manager.sh` ✅
- `nimlsp_custom_endpoints.py` ✅
- `dag_trace_fields.sh` (happy path:brain→explore→worker 四步 delegation) ✅
- `dag_trace_fields_failures.sh` (failure path:sub-agent command_failed 传播) ✅
- `relay_mobile_joint.sh` (本地 NATS + TUI relay + mobile client) ✅

---

## 来源对照(free-code → Crush)

如果你想知道哪个能力是从 free-code 移植的:

| Crush 代号 | 对应 free-code 模块 |
|:---|:---|
| M1 | `systemPromptSection` vs `DANGEROUS_uncachedSystemPromptSection` |
| M2 | OpenAI `PromptCacheKey` + Anthropic `ephemeral` + Gemini 隐式三家抽象 |
| M3 | AgentTool delegation 强制指令 |
| M4 | `EndTruncatingAccumulator` + 30KB→spill + preview/path 三件套 |
| M5 | 时间冷工具结果就地清理(不走 LLM) |
| M7 | `~/.claude/projects/<slug>/memory/` + 4 类 frontmatter |
| F1/F3 | `findActualString` + `desanitizeMatchString` + `stripTrailingWhitespace` |
| F5 | `defer_loading=true` + `select:` / 关键词加权 / `+term` 必需 |
| F6 | `cronScheduler` 1s 轮询 + `messageQueueManager` 统一汇聚 |
| F8 | drawer cwd grouping |

完。
