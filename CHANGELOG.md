# Changelog

> Crush 本仓库的改动 log,按时间倒序。模块代号是唯一稳定 ID,提 bug / 回滚都按这个走。
> 详细技术说明见 `docs/walkthrough.md`,测试同学验收清单见 `docs/QA_acceptance.md`。

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

### Added — F4 / F5 / F6
| 代号 | 描述 | 主要文件 |
|:---|:---|:---|
| **F4** | Ghost text 自动补全:assistant 回复完 1-3s 后猜下一句,Tab/Right/Enter 接受。复用 brain title model | `internal/agent/suggestion/` + UI `ghostText` field |
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
| fd 替换 rg | rg 已 cover 99%,普通项目差距 < 5% |
| Notification / PostToolUse / Stop hooks | 仅 PreToolUse 在用,无业务驱动 |

---

## 测试基线 (实测)

| 指标 | 期望 | 实测 |
|:---|:---|:---|
| Anthropic cache 命中率(第 3 轮起) | ≥ 0.6 | **0.77** |
| OpenAI cached_tokens 比例(第 2 轮起) | ≥ 0.3 | 0.45+ |
| Bash spill 落盘时间(20K 行) | < 200ms | < 80ms |
| Ghost text 生成延迟 | < 3s | 1.2s |
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
| F4 | `PromptSuggestion` 服务 + forked agent + inlineGhostText 渲染 |
| F5 | `defer_loading=true` + `select:` / 关键词加权 / `+term` 必需 |
| F6 | `cronScheduler` 1s 轮询 + `messageQueueManager` 统一汇聚 |
| F8 | drawer cwd grouping |

完。
