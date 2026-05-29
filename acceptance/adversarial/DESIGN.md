# Crush 对抗性评测设计 (Adversarial Eval Suite)

> 目的：在**高于 agent 当前能力**的复杂业务场景里，用 **ground-truth oracle**（不信 agent 自报）+ **效率指标**，暴露提示词/工具描述/设计的系统性缺陷。
> 缘起：远程 grep bug 不是 happy-path E2E 测出来的，是盯 hostname 真值撞出来的。射程内的单路测试永远绿。

## 核心纪律

1. **真值断言，不信自报**：每个场景断言独立可验证事实（hostname / 文件 sha256 / 已知 symbol file:line / 字节级 diff / 进程退出码）。agent 说"完成"不算数。
2. **复杂度高于能力线**：多子系统、故意歧义、本地+远程混合、长上下文污染、需并发。naive/退化 agent 必须以**可测量方式失败**才有分离度。
3. **对抗条件注入**：同 turn 工具时序、turn 中途改向、queued 积压、上下文灌水、跨 host 歧义。
4. **效率也是指标**：不只对错——墙钟、turn 数、**实际并发度**（trace node_id/parent_id/depth + tool_started 时间重叠）、浪费 turn、context_bytes 增长曲线、token/cost。
5. **全息打点已就位**：crush-dev `--trace-file` 每事件 `file.Sync()` 实时落盘；字段含 tool i/o+bytes、tokens/cache、context_bytes、context_window、并发拓扑、首字延迟、cost。oracle 直接 tail + 解析此 trace。

## Harness 两模式

- **headless**：`crush-dev run "<prompt>"`（单任务内部多轮 tool-loop）。用于 S1-S3、S5-S7、S9。
- **interactive(tmux)**：tmux 拉起 TUI + send-keys（沿用 acceptance/ 既有 tmux 模式）。用于需**跨用户轮次**的对抗：S4 积压/中断、S8 退化。

每场景产物：`artifacts/<scenario>/{trace.jsonl, http-dump/, oracle.json, verdict.txt}`。

## 工具覆盖矩阵 (41 工具，每个至少一处)

| 工具簇 | 工具 | 覆盖场景 |
|--------|------|----------|
| 文件 | view edit multiedit write ls grep find ast_grep | S2 S6 S7 |
| 执行 | bash run monitor job_output job_kill schedule_wakeup | S5 S9 |
| 代码情报 | code_triage bug_triage evidence_batch evidence_graph dag_run sourcegraph | S3 S7 |
| 子代理 | agent(explore/plan/worker/auditor) | S3 S8 |
| 远程 | remote_attach remote_detach ssh_exec ssh_session_* ssh_mount* ssh_upload/download | S1 |
| Web | fetch agentic_fetch | S9 |
| MCP | list_mcp_resources read_mcp_resource | S9 |
| 元 | todos crush_info crush_logs download | S4 S9 |

## 场景定义

### S1 — 本地↔远程混合取证 (远程驱动 + 文件簇)
**对抗**：同 turn 内 attach→远程搜索→远程读→与本地对比。host 歧义陷阱。
**setup**：fixture 项目同时存在本地副本和 ECS(root@47.83.6.142) 副本，其中远程版某文件被植入一处差异。
**任务**："连远程 host，找出远程 X 文件与本地 X 的实现差异并报告差异行"。
**oracle**：报告的差异行 == 真实植入差异（字节级）；trace 中 grep/view 的执行确实在 remote backend（用远程独有内容校验）。
**杀手锏**：退化 agent 在本地搜（返回本地内容）—— 正是已修的 per-turn bug 类。

### S2 — 上下文污染下的深层 symbol 定位 + 字节级编辑
**对抗**：先强制 view 8 个大无关文件灌满上下文，再定位一个深埋 symbol 并做精确空白编辑。
**setup**：crush 仓库副本（已知 ground truth）。
**任务**："读 [8 个大文件] 理解全局，然后把 `coordinator.go` 里 `UnmountAllRemotes` 的某行改成 X（精确缩进）"。
**oracle**：编辑后该文件 sha256 == 预期；其余字节不变；build 仍过。
**指标**：定位命中前消耗的 turn 数、context_bytes 峰值。

### S3 — explore 子代理多路并发 + 隔离 (你最关心)
**对抗**：一个**必须 4 路独立调查**的 brief（4 个互不相关子系统问题），单路串行会超时/低质。
**任务**："并行查清：①grep/find 透写器在哪决定 rg vs fd ②remote daemon 协议有哪些 RPC ③auto-summarize 阈值逻辑 ④bash auto-background 触发条件。每路给 file:line 证据"。
**oracle**：4 路结论全部正确（每条有真实 file:line）；无串味（A 的结论不混入 B）。
**指标（关键）**：trace 看 brain 是否**单轮发起 4 个 `agent(role=explore)`**（并发）vs 串行；总墙钟 vs 4 路并行下限（max 单路耗时）；隔离度。
**杀手锏**：退化 → 串行 4 次 explore 或自己串 12 次 view，总耗时翻 3-4 倍。

### S4 — 对话积压 + 中途改向 (interactive)
**对抗**：连发 4 条 queued prompt，第 3 条执行中按 ESC 改向第 5 条矛盾指令。
**oracle**：最终仓库状态匹配**最后**指令；trace 无"默默恢复"被放弃计划的痕迹（interruption_handling 规则是否真生效）。
**指标**：ESC 后 context canceled 延迟、是否误执行被取消的 queued 项。

### S5 — 后台长任务 + monitor 唤醒
**对抗**：background 跑一个 ~20s 渐进输出进程，monitor 等特定 pattern，命中后才动作。
**任务**："background 跑 [脚本]，用 monitor 等 'READY'，出现后用 job_output 取完整输出并提取其中的 token"。
**oracle**：动作发生在 pattern 出现**之后**（trace 时序）；提取的 token 正确。覆盖 bash(bg)/monitor/job_output。

### S6 — 跨文件重构 + 构建验证 (grep+multiedit+bash)
**对抗**：把一个跨 N 文件的符号重命名，必须先 grep 定位全部引用再 multiedit，最后 build 验证零残留。
**oracle**：`grep 旧名` 命中 0；`go build` 退出 0；新名引用数 == 旧名原引用数。
**杀手锏**：退化 → 漏改、build 断、或用 bash sed 批改（违禁）。

### S7 — 结构化搜索 + 情报折叠 (ast_grep + code_triage)
**任务**："用结构化搜索找出所有 `func (b *RemoteBackend) X(...)` 方法，再用 code_triage 给出它们的职责摘要"。
**oracle**：方法集 == 真实集合（ast 匹配 ground truth）；覆盖 ast_grep/code_triage/evidence_batch。

### S8 — 长会话能力退化 (interactive, 30+ turn)
**对抗**：同一 session 先做轻任务铺垫到 turn 2 做一次 S2-lite，灌到 turn 30 再做同样 S2-lite。
**oracle**：turn2 vs turn30 同任务成功率/字节精度对比。
**指标**：context_bytes / total_tokens 增长曲线；auto-summarize 是否触发及触发后质量是否塌。

### S9 — Web + MCP + 调度 (长尾工具覆盖)
**任务**："用 fetch 抓 [本地 httpbin 或固定 URL] 的 JSON 提取字段；list_mcp_resources 看有哪些；schedule_wakeup 设一个 60s 后的回唤并确认落盘 scheduled_tasks.json"。
**oracle**：提取字段正确；scheduled_tasks.json 含该任务。覆盖 fetch/agentic_fetch/mcp/schedule_wakeup/download。

## 执行模型 (worktree subagent fan-out)

- 每场景一个 worktree-隔离 subagent：独立 crush 副本 + 独立 data-dir + 独立 fixture 副本 + 独立 trace 文件，互不干扰。
- 共享同一被测 crush-dev 二进制（系统一致性）；headless 场景并发，interactive 场景受 tmux 会话名隔离。
- 每 subagent：跑场景 → 落 trace → oracle 断言真值 + 解析效率指标 → 回 verdict（pass/fail + 失败根因 + 指标）。
- **先 pilot S1 单跑验证 harness**，再全量 fan-out（避免并发烧 LLM 在坏 harness 上）。

## 真值来源

- 本地 fixture = crush 仓库副本（我有完整 ground truth）。
- 远程 = ECS root@47.83.6.142（hostname=polysite-prod-new，与本地 junknet-home 天然判别）。
- 植入差异/symbol 位置/重命名集合在 setup 阶段由脚本固定，oracle 比对固定真值。
