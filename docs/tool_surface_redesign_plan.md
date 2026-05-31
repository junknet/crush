# 工具面重构 — 背景 · 方案 · 执行 Plan · 验收

> 状态:设计已定稿(2026-05-31),尚未落地。本文是落地前的单一事实源(SSOT)。
> 决策账本镜像在 memory `tool-surface-redesign-2026-05-31`。
> 远程验收 ECS:`ssh root@47.110.255.240`。

---

## 0. 一句话目标

把 agent 工具面收敛为**语义清晰、底层透明、本地/远程一致、事件驱动**的最小集合;
删掉重复实现与遗留通路;顺手修掉 `Batch` 的 5s 超时 bug。

---

## 1. 背景与根因

### 1.1 触发问题(真实日志)

`crush-dev` trace(`~/.local/state/crush-dev/trace-20260531-081638-*.jsonl`)中:

```
tool_name":"Batch"  status":"failed"
"Tool Batch execution timed out after 5s. This tool is expected to finish
 quickly; the operation was canceled instead of being converted to a
 background job."
```

触发输入:一个 Batch 含 `{"kind":"bash","script":"cd nimony-private && ./bin/hastur seed"}` 节点。

### 1.2 根因:两层超时预算打架

| 层 | 位置 | 预算 |
|---|---|---|
| 外层 `timeoutTool` 包装 | `internal/agent/coordinator.go:1438` `wrapToolsWithTimeout(filteredTools, 5*time.Second)` | **5s 硬取消整个 Batch** |
| 内层 Batch 的 bash 节点 | `internal/agent/tools/dag_run.go:84` `dagRunShortCommandTimeout = 10s` | 每节点 **10s** |

`Batch` 是**复合/编排工具**(扇出 N 个节点,默认并行 4),合理运行时本就 ≫ 5s,却被
`wrapToolsWithTimeout` 当快工具套 5s——比它单个子节点预算(10s)还短。
`timeout_tool.go:93 isBackgroundCapableTool` 不含 `Batch`,所以报"应快速完成"那条误导文案。

### 1.3 深层架构缺陷(三连)

**缺陷 A — Batch 重新实现了每种节点,绕过抽象层。**
`internal/agent/tools/dag_run.go:497 executeDagRunNodeOutput` 用 150 行 switch,各 kind 直接调
**本地裸原语**:

```
dag_run.go:538  RgSearch          → rg.go:274 exec.CommandContext(rg)   # Grep 节点
dag_run.go:548  RgSearchFiles     → rg.go:356 exec.CommandContext(rg)   # Find 节点
dag_run.go:582  ListDirectoryTree → ls.go:142 os.Stat/os.ReadDir        # ReadDir 节点
dag_run.go:618  executeRunScript  → run.go:61 exec.CommandContext       # bash 节点
```

而顶层同名工具走的是 **iodriver backend**(本地/远程透明):

```
search.go:95  resolveRunner → GetBackendFromContext → 远程 backend.Exec / 本地 exec(bundled rg)
iohelpers.go  CtxReadFile/CtxReadDir → backend FileSystem   (Read/Edit/Write 用)
bash.go:500   GetBackendFromContext → Jobber/Execer          (Bash 用)
```

结论:**远程会话下,顶层 Grep 走 daemon,但 Batch 内的 Grep 跑本机**——同一语义两套行为。

**缺陷 B — DAG 依赖引擎是运行时死代码。**
注册的只有 `NewEvidenceBatchTool`(`coordinator.go:1338`),它执行前
`params.Nodes[i].DependsOn = nil`(`dag_run.go:108`)→ 已是纯并行。
`dag_run`/`evidence_graph` 工具仅测试引用/零引用。依赖引擎
(`hasDagCycle`/`shouldStopDagRun`/`readyDagRunNodes`/`dagRunDependencyOutputs`/
`interpolateDagRunNode`/`dagRunInterpolationPattern` + 拓扑波次 + `DependsOn`/`OnFailure`)
运行时从不触发。

**缺陷 C — 无统一 name→tool 分发。**
`CLAUDE.md` 明示"No central registry; each tool is a constructor invoked during
buildAgent()"。所以 Batch 无法像 jcode `registry.execute(name,params,ctx)` 那样复用真实工具,
只能重实现。这一个缺陷同时制造了 A(重复)、本地/远程割裂、转义/别名自愈重复三件事。

### 1.4 参考实现:jcode batch(Rust)

`/home/junknet/Desktop/jcode`:

- `crates/jcode-app-core/src/tool/batch.rs`:纯并行 `FuturesUnordered`,`MAX_PARALLEL=10`,
  子调用 `registry.execute(&tool_name, parameters, sub_ctx)` 走**同一工具链**;无 DAG/依赖。
  输出预算公平 `max_per_tool = 50_000 / num_tools`;`normalize_batch_input` 自愈 LLM 常见错(`name→tool`、`args/arguments→parameters`)。
- `crates/jcode-batch-types/src/lib.rs`:`BatchProgress { total, completed, running, subcalls[] }` +
  `BatchSubcallState { Running, Succeeded, Failed }`,经全局 Bus 流式发布。
- `crates/jcode-tui/src/tui/ui_status.rs:174`:状态行 `Running batch: 2/5 done, running: …, last done: …`。
- `crates/jcode-tui/src/tui/ui_messages.rs:1710` + `ui_tools.rs:1351 render_batch_subcall_line`:
  每个子调用 `✓/✗` + 名称 + intent + token badge 渲染。

---

## 2. 设计决策账本(均已逐项确认)

| # | 决策 | 备注 |
|---|---|---|
| D1 | 删 `dag_run`/`evidence_graph` 工具 + 整套 DAG 依赖引擎 | 运行时死代码 |
| D2 | 全量改名 `dag_run.go→batch.go`,`DagRun*→Batch*` | 去 dag 误名;ReadDir 独立保留 |
| D3 | Batch 改纯并行扇出,子调用经**统一 name→tool 分发表**打真实工具 | 除 `Batch`/`Agent` 外全部可派发 |
| D4 | 子调用各自继承各工具超时 → 5s-on-whole-Batch bug 结构消失 | — |
| D5 | 加 jcode 式实时进度(事件总线 + 状态行 + 子调用 ✓/✗ 渲染) | — |
| D6 | 删 `ast_grep`、`Nu` 工具 | — |
| D7 | 删 `rg.go`(RgSearch/RgSearchFiles)+ caps 检测 + fd 回退 | — |
| D8 | 合 `Grep`+`Find` → 单一 **`Search`**(内容/文件两模式) | 局部反转 2026-05-30「不折叠」 |
| D9 | 合 `View` → `Read` | — |
| D10 | `Ls`→`ReadDir`,改走 `CtxReadDir`(backend-aware) | 修远程正确性 |
| D11 | Search 用**内置 rg(getRg)唯一来源**,无 fallback,启动 fail-fast | — |
| D12 | Bash 裸调 `rg/grep/find/ls/cat` → **透明路由(不报错)** | 复用 `scriptDispatchHandler` |
| D13 | 删全部 **13 个 SSH\*** 工具,只留 `RemoteAttach`/`RemoteDetach` | daemon 已透明覆盖 |
| D14 | `serve.go` 改为派发到 `LocalBackend` 实例,删 os.* 第二份拷贝 | io 逻辑一份 |
| D15 | `cmd/crush-remote` `//go:embed` rg,启动解压;远程 Search 用它 | daemon 2.4MB→~7MB |
| D16 | 核心 job 加 stdin/PTY 腿;daemon 加交互 exec RPC;输出 **push** 进 `backgroundBroker` | 吸收 SSHSession* 能力 |

**架构定性(关键认知)**:daemon = `cmd/crush-remote`,独立最小二进制(~2.4MB,只 import
`internal/iodriver`,stdlib-only),是 **remote-io AOP 透明层的"远程外围 io 手脚"**,
**不是 crush 本体**(~100MB,agent/LLM/UI 全在控制端)。`local==remote` 因两端跑同一套 io 原语。
`cmd/crush-remote/main.go` 注释已自证此设计。

---

## 3. 现状代码地图(改动落点)

### 3.1 工具注册与过滤
- `internal/agent/coordinator.go:1316-1352` `allTools = append(...)` — 全部构造器。
- `internal/agent/coordinator.go:1365-1367` `filteredTools` 按 `AllowedTools` 过滤。
- `internal/agent/coordinator.go:1438` `wrapToolsWithTimeout(filteredTools, 5*time.Second)`(bug 点)。
- `internal/agent/coordinator.go:1336-1340` `wrapToolsWithHooks`/`wrapToolsWithTimeout`/`wrapToolsWithTrace` 顺序。

### 3.2 超时包装
- `internal/agent/timeout_tool.go:103 wrapToolsWithTimeout`、`:93 isBackgroundCapableTool`、`:86 toolTimeoutMessage`。

### 3.3 Batch / DAG
- `internal/agent/tools/dag_run.go`(800 行)、`dag_run.md`(20 行)、`dag_run_test.go`(213 行)。
- 构造器:`NewDagRunTool:94` / `NewEvidenceGraphTool:98` / `NewEvidenceBatchTool:102`。
- 节点执行器:`executeDagRunNodeOutput:497`;依赖引擎见 §1.3-B。
- `internal/agent/agent.go:2775` 三名只读检查(改只留 `EvidenceBatchToolName`)。

### 3.4 工具名清单(改名/删除都要同步)
- `internal/config/config.go:754 allToolNames()`(含 11 个 SSH*、`Grep`、`Find`、`ReadDir`、`Read`…)。
- `internal/config/config.go:803 resolveExploreTools`(含 `Grep,Find,ReadDir,Read,Batch`)。
- `internal/proto/tools.go`:`RgToolName="rg"`、`LSToolName="ReadDir"`、`BashToolName` 等(relay/mobile 用)。
- `internal/ui/chat/tools.go` dispatch + `normalizeToolName`(旧名别名→canonical)。
- 移动端 `index.tsx` getToolCallSummary / viewType(见 [[tool-surface-semantic-rename]] 同步清单)。

### 3.5 Search 底层
- `internal/agent/tools/search.go:95 resolveRunner`(backend/本地双路 + caps 检测,要简化)。
- `internal/agent/tools/rg.go`(RgSearch/RgSearchFiles,要删)。
- `internal/agent/tools/embed_rg.go:12 //go:embed bin/rg-linux-amd64`、`EnsureEmbeddedToolsExist`(getRg 来源)。
- `internal/agent/tools/ast_grep.go`(删)、`nu.go`(删)。

### 3.6 远程 / daemon
- `cmd/crush-remote/main.go`(daemon 入口,`iodriver.Serve(ctx, stdin, stdout)`)。
- `internal/iodriver/serve.go:24 Serve`、`:45 handleRequest`(裸 os.*,改派发 LocalBackend)。
- `internal/iodriver/local.go LocalBackend`(唯一 io 实现)。
- `internal/iodriver/remote.go RemoteBackend`、`:172 Exec`、`:200 StartJob`、`:219 JobOutput`(poll,要改 push)。
- `internal/iodriver/proto.go:23-33 rpcMethod` 常量(加交互 exec method)。
- `internal/iodriver/ssh.go`(传输 + daemon 部署)、`embedbin/embed.go`(`//go:embed crush-remote_*`)。
- `scripts/build_remote_daemon.sh`、Taskfile `task daemon`(daemon 构建;加 rg 嵌入)。
- `internal/agent/tools/ssh.go`(13 个 SSH* 工具,删)、`remote.go`(RemoteAttach/Detach,留)。

### 3.7 事件管线 / job
- `internal/shell/background.go`:`backgroundBroker pubsub:68`、`linePublishWriter:225`、
  `BackgroundShell{ done chan }:113`、`Start/StartWithRewriters/StartMonitor/Kill`。
  **无 stdin writer**(要加)。
- `internal/agent/tools/monitor.go` / `job_output.go` / `job_kill.go`(事件唤醒/读/杀,已有)。

---

## 4. 分阶段执行 Plan(DAG)

依赖关系:`P0 ∥ P4a` → `P1` → `P2 ∥ P3` → `P4b` → `P5`。

### P0 · 独立删除(无依赖,可并行)
**目标**:删 ast_grep / Nu / 13 SSH*。
- 删 `tools/ast_grep.go`+`.md.tpl`、`tools/nu.go`+`.md.tpl`;删 `tools/ssh.go` 全部 13 构造器+常量。
- 同步 `coordinator.go` 注册移除;`config.go allToolNames` 去 11 个 SSH* 名 + Nu(若在列);
  `agent.go:2775` 三名检查去 dag/evidence 留 Batch;prompt 模板 prose 去引用。
- `proto/tools.go` 若有 ast/nu/ssh 常量一并清。
**验收**:`go-task build` 通过;`go test ./...` 绿;`crush` 启动工具列表无 ast_grep/Nu/SSH*。

### P1 · 工具面收敛
**目标**:Search(合 Grep+Find)、Read(合 View)、ReadDir(修 backend)、统一分发表。
- 新 `Search` 工具:结构化参数 `mode: content|files`;content=`rg <pattern>`,files=`rg --files`+glob。
  底层走 `search.go resolveRunner`(本地 getRg / 远程 backend.Exec),**删 caps 检测/fd 回退**。
- 删 `rg.go`(RgSearch/RgSearchFiles);删 `Grep`/`Find` 旧构造器,`allToolNames`/`resolveExploreTools` 用 `Search` 替换。
- `View`→`Read`:合并构造器,`allToolNames` 去 `View`(若存),保留 `Read`。
- `Ls`→`ReadDir`:`ls.go ListDirectoryTree` 改用 `CtxReadDir`(iohelpers,backend-aware)。
- **统一分发表**:在 `coordinator.go` `filteredTools` 包装完成后(§3.1 顺序之后),
  建 `map[string]fantasy.AgentTool{name: tool}`,经 setter 回填进 Batch 实例(仿 `agent.go:2393 SetDeferredRegistry`)。
- `proto/tools.go`/`ui/chat/normalizeToolName`/`index.tsx` 加 `Search` 新名 + 旧 `Grep/Find/View` 别名映射。
**验收**:`Search {"mode":"content","pattern":"func"}` 与 `Search {"mode":"files","query":"*.go"}` 真机命中;
`Read`/`ReadDir` 渲染正常;`go test ./...` 绿。

### P2 · Batch 重构(依赖 P1)
**目标**:`batch.go` 纯并行扇出 + 实时进度;删 DAG 引擎;5s bug 消失。
- `dag_run.go→batch.go`;`DagRun*→Batch*`;删 §1.3-B 依赖引擎 + `executeDagRunNodeOutput` switch +
  `RgSearch/ListDirectoryTree/executeRunScript` 调用。
- Batch 执行体:对每个 `tool_calls[i]` 经分发表 `dispatch[name].Run(ctx, ToolCall{Name,Input})`,
  `errgroup`+信号量(默认并行 4,上限 16)扇出;禁 `Batch`/`Agent` 子调用;输出预算公平分配(仿 jcode `50_000/n`)。
- 进度:仿 jcode 加 `BatchProgress{total,completed,running,subcalls[]}` + `BatchSubcallState`;
  经 crush 现有事件总线(`internal/agent/event.go` / pubsub)发布;
  UI 加状态行 `Running batch: x/n done` + `internal/ui/chat/` 子调用 ✓/✗ 渲染(参考 §1.4)。
- `timeout_tool.go`:Batch 不再被 5s 包装(子调用各自带预算);清理 `isBackgroundCapableTool`/文案。
**验收**:含 6s bash 节点的 Batch **不再 5s 超时**;并行多节点真机跑通;TUI 实时进度 `ttyd`+`playwright` 截图比对;`go test ./...` 绿。

### P3 · Bash 命令拦截(依赖 P1)
**目标**:裸调结构化工具的命令透明路由。
- `internal/shell/` `scriptDispatchHandler`/`ExecHandlerFunc` 层加检测:
  `rg|grep|ag|ack` → Search content;`find|fd` → Search files;`ls|tree` → ReadDir;`cat|head|tail` → Read。
  **透明路由,不报错**;无法安全映射的复杂调用透传原命令。
**验收**:Bash 里 `grep -rn foo` 与 `Search {pattern:foo}` 走同一实现(本地/远程一致);路由有结构化 trace log。

### P4 · daemon 一致性 + 交互管线(P4a 独立 / P4b 依赖 P1 的 Search)
**P4a(独立)**
- `serve.go handleRequest` 改为持有一个 `LocalBackend` 实例并派发(`b.Stat/ReadFile/...`),删 os.* 第二份拷贝。
**P4b**
- `cmd/crush-remote` + `embedbin`:daemon `//go:embed` rg(linux-amd64/arm64);启动解压到 tmp;
  远程 `Exec(["rg",...])` 用它。`scripts/build_remote_daemon.sh` 加 rg 嵌入步骤。
- 交互腿:`proto.go` 加 `methodExecInteractive`(流式 stdin);`serve.go` 起 PTY;
  `BackgroundShell` 加 stdin writer;`RemoteBackend` 加交互通道;daemon job 输出 **push** 进 `backgroundBroker`(替 `JobOutput` poll)。
- 删除 SSHSession* 后,交互能力由核心 Bash/job(本地)+ daemon(远程)提供。
**验收**:见 §6 远程 ECS 验收。

### P5 · 全量验证
`go-task build` + `go test ./...` + TUI playwright + 远程 ECS E2E(§6)。

---

## 5. 测试方法与验收标准(通用)

> 遵循全局公理:**真数据、真组件、真路径**;禁 mock/桩/smoke/合成。

### 5.1 编译与单测
```bash
go-task build          # 必须:嵌入 *.md.tpl / provider.json / embedbin
go test ./...          # 全绿;Batch/Search/iodriver 包重点
go-task lint           # 日志首字母大写 + golangci-lint
```
**标准**:零编译警告;无 FAIL;lint 通过。

### 5.2 TUI 真终端(进度 UI / 工具渲染)
用 `tui-test` skill:`ttyd` 起 `crush-test`,`playwright` 发真按键、读 ANSI 屏、截图比对。
**标准**:
- `Search`/`Read`/`ReadDir` 调用渲染 clean label(`✓ Search {…}`)。
- Batch 运行中显示 `Running batch: x/n done`,完成后每子调用 ✓/✗ 行;含慢节点不超时。

### 5.3 本地 io 一致性
本地跑 Search/Read/ReadDir/Bash,与对应 shell 命令(rg/cat/ls)结果字段级一致。

---

## 6. 远程 ECS 端到端验收(`ssh root@47.110.255.240`)

> daemon = io 手脚的核心验收:**远程操作与本地行为字面一致**。

### 6.1 前置
```bash
# 控制端确认可达(交互登录请用会话内 `! ssh ...`)
ssh -o BatchMode=yes root@47.110.255.240 'uname -m && cat /etc/os-release | head -1'
# 预期 x86_64 → 需 daemon 内嵌 rg-linux-amd64(P4b);若 aarch64 则需 arm64 版
```

### 6.2 RemoteAttach 透明性(P4a + P13)
在 `crush` 中对 `root@47.110.255.240` 执行 `RemoteAttach`,然后**不切换工具**依次:
| 操作 | 期望 | 证据 |
|---|---|---|
| `ReadDir /root` | 列出远程 `/root` 内容 | 与 `ssh … ls -la /root` 一致 |
| `Read /etc/hostname` | 远程主机名 | 与 `ssh … cat /etc/hostname` 一致 |
| `Search {mode:content,pattern:root,path:/etc/passwd}` | 远程命中 | daemon 用内嵌 rg(P4b);远程无系统 rg 也成功 |
| `Write /root/_crush_probe.txt` + `Read` 回读 | 字节级一致 | `ssh … cat` 校验 |
| `Bash "hostname -I"` | 远程网卡 IP | 非控制端 IP |
| `RemoteDetach` 后同样操作 | 回到本机 | 路径/结果切回本地 |

**标准**:以上全部命中远程;trace 显示 `backend=remote:47.110.255.240`;无任何 SSH* 工具参与。

### 6.3 daemon 内嵌 rg(P4b/D15)
远程**刻意不装系统 rg**(`ssh … 'which rg || echo NO_RG'` 应为 NO_RG),
RemoteAttach 后 `Search content` 仍成功 → 证明 daemon 解压了内嵌 rg。
**标准**:无系统 rg 下远程 Search 成功;daemon tmp 目录出现解压的 rg。

### 6.4 io 逻辑一份(P4a/D14)
代码层:`serve.go` 不再含 `os.ReadFile/os.Stat...` 直调,改 `LocalBackend` 派发。
行为层:同一 Read/ReadDir 在本地(LocalBackend 直调)与远程(daemon→LocalBackend)字段一致。

### 6.5 交互 job + 事件管线对称(P4b/D16)
```
Bash 起远程交互命令(如 `python3 -i` 或 `read x; echo got:$x`)→ 后台 → shell_id
→ (新)向 shell_id 写 stdin "hello\n"
→ Monitor pattern 命中 push 来的输出行(非 poll)
→ JobKill
```
**标准**:能向运行中**远程** job 喂 stdin 并拿到响应;输出经 `backgroundBroker` push(trace 显示 push 事件,非 `JobOutput` 轮询);本地同流程行为一致。

### 6.6 清理
```bash
ssh root@47.110.255.240 'rm -f /root/_crush_probe.txt'
# RemoteDetach 后 daemon 进程随 SSH 通道 EOF 退出(serve.go Serve loop on EOF)
ssh root@47.110.255.240 'pgrep -fa crush-remote || echo CLEAN'
```

---

## 7. 风险与回滚

| 风险 | 缓解 |
|---|---|
| P4b 动 iodriver 协议(交互 RPC + push 流),最大块 | 独立分支;先 P4a(纯重构,行为不变)落地验证,再 P4b |
| 改名/删名遗漏同步点(渲染/派发对不上) | 按 §3.4 + [[tool-surface-semantic-rename]] 同步清单逐项核;`normalizeToolName` 保留旧名别名兜底 |
| 内嵌 rg 跨架构(ECS arm64) | `build_remote_daemon.sh` 同时嵌 amd64+arm64;attach 按远程 `uname -m` 选 |
| Batch 子调用派发到 timeout-wrapped 工具的递归/重复包装 | 分发表用已包装工具;显式禁 `Batch`/`Agent` 子调用 |

**回滚**:每 Phase 独立 commit;P4 单独分支。任一 Phase 验收失败即 revert 该 Phase,不双跑过渡(独狼原则)。

---

## 8. 落地顺序建议

`P0`(纯删,立即可验证)→ `P1`(Search/Read/ReadDir + 分发表)→ `P2`(Batch 闭环,bug 消失)
→ `P3`(拦截)→ `P4a`(serve 重构)→ `P4b`(rg 内嵌 + 交互管线)→ `P5`(全量 + ECS E2E)。
