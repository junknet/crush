# 对抗评测结论 (Adversarial Eval — Findings)

> 方法：ground-truth oracle（不信 agent 自报）+ trace 效率指标 + 复杂度高于能力线。
> SUT：crush-dev（brain=gemini-3-flash-agent / antigravity）。harness：DESIGN.md + analyze_trace.py。
> 每场景一个 worktree 隔离 subagent，独立 fixture/data-dir/trace，自推导真值断言。

## 记分卡

| 场景 | 工具簇 | 判定 | 真值证据 |
|------|--------|------|----------|
| S2 上下文污染+字节编辑 | view/evidence_batch/edit | ✅ PASS | 8 大文件灌入（82% cache 吸收），`runRemoteBash` 上方标记字节精确，仅 1 文件改，build 绿。4 turn/23s |
| S5 后台+monitor+job_output | bash(bg)/monitor/job_output | ✅ PASS | monitor yield 非轮询，job_output 取到含 READY 全量；反前台轮询纪律守住 |
| S6 跨文件重构 | grep/multiedit/bash | ✅ PASS | 旧名 0 残留、12 处全迁 4 文件、build 绿、零 bash sed。grep 先全定位再改 |
| S7 结构化搜索 | ast_grep/grep | ✅ PASS | ast_grep 命中全部 12 个 `*RemoteBackend` 方法（含未导出 `call`），无漏无多 |
| S9 schedule_wakeup | schedule_wakeup | ✅ PASS | 精确 60s、reason=ADV_S9_PING 落盘 `.crush/scheduled_tasks.json` |
| S3 explore(已知库) | evidence_batch | ⚠️ 0 fan-out | 路径选错（已知库→evidence_batch 本就对）；串行 |
| S3' explore(陌生库 37 文件) | evidence_batch | ⚠️ 0 fan-out | 真测到 explore 路径仍 0 spawn；但 2 turn/23s 正确 |
| S3'' explore(大库 976 文件/8 子系统/显式并行指令) | evidence_batch×14 | ⚠️ 0 fan-out | **scale-invariant**：26× 文件量 + 显式"并行"指令 → spawn 仍 0；但 context 峰值仅 7.8%，串行客观正确，答案字节精确 |
| S1 远程混合取证 | remote_attach/ssh_* | ❌ 无效 | ECS sshd 被测试连接风暴限流（`Connection closed`/timeout）；非 crush 逻辑——早先直接运行已证 remote_attach 可用（读到 polysite-prod-new） |

## 核心结论

**crush 核心面非常稳**：文件定位/字节级编辑（抗上下文污染）、跨文件重构纪律、结构化搜索、后台+monitor 时序、调度落盘——**5 个干净 PASS**，全部 ground-truth 真值通过。这套工具+提示词在干实活，别动。

**唯一悬而未决：explore fan-out 从不触发**（S3/S3'/S3'' 三测、scale-invariant、连显式"并行"指令都被当噪声）。但：
- 所有已测场景里**串行 evidence_batch 是经济正确选择**（更快、答案精确、context 从未受压）——无用户可见缺陷。
- **未证伪**：强制上下文压力的 forcing-function 场景从未触发，无法区分"brain 正确拒绝委派" vs "结构上永不委派(死路)"。
- **不基于'fan-out 坏了'的未证前提盲改委派提示词**（会把小任务拖慢，过度修复比不修糟）。

## 已自愈

- ✅ **rule 11 ssh_mount 误导**（提示词审计发现，与 remote_attach 矛盾）→ 改指 remote_attach 首选 + 修编号。已提交 `499ea11`。

## 待决 / 下一步

1. **explore dead-vs-dormant 决定性测试 (S3''')**：forcing function——要求通读多个大文件全文 或 调小 context window，让串行自查溢出/触发 auto-summarize，看 brain 是否为保上下文而委派 explore。仅此能锤死。
2. **用户显式意图 vs 结果最优** 的判断分叉：当用户**显式要求并行委派**时，brain 该不该无视经济最优而遵从？（当前它遵从结果最优、忽略字面指令——得到了正确结果但违背显式意图。）这是产品判断，需用户定。
3. **S1 / SSH 限流韧性**：环境噪声，冷却后直接复验远程驱动；是否给 crush ssh 加瞬时失败重试退避属考量项（限流下重试未必有用）。
4. **未覆盖**：S4 积压/中断、S8 长会话退化——需 tmux 交互 harness（headless `crush run` 测不了跨用户轮次）。

## 提示词冗余（审计假设，未经 ablation 证实）

5 个 PASS 表明委派×5/精确匹配×4/验证×4 的重复**没害事**，但**未证明必需**。砍需走 ablation（删重复→重跑 S2/S6/S7 看是否仍 PASS），不盲砍。

## harness 经验

- fixture 拷贝必须排除 `mobile/`(9.2G)/`build`/`node_modules`/`*.so`（撞 tmpfs ENOSPC）。
- **远程场景不能用 worktree subagent 测**：subagent 沙箱/并发连接风暴会触发 ECS 限流，假阴性。远程必须直接上下文跑。
- analyzer 顶层并发指标看不到 evidence_batch/code_triage 的内部并发——批量工具的真并行被低估。

---

# 真实语料分析 (997 crush-dev traces, 05-21→05-29, 37G)

> 用 aggregate_corpus.py 单遍流式扫描 crush 自身迭代期积累的全息 trace。
> **这是比合成场景更权威的真实行为分布**——并直接纠正了上面合成测试的一个错误结论。

## 🔴 重大纠正：explore fan-out 是 work 的

合成 S3/S3'/S3'' 全 0 spawn，让我倾向"explore 委派休眠/死路"。**真实语料推翻**：
- `AGENT_SUBAGENT_SPAWNS_TOTAL: 88`（22,110 工具调用中），22 个 session 用子代理。
- `global_max_concurrent_tools: 13`（真达到 13 路并发），37 个 session >1 并发。
- **结论：explore fan-out + 并发在真实使用中正常触发**。我那 3 个合成测试是**假阴性**（brief 形态/单次模型行为不代表真实分布）。
- **方法论教训**：合成 eval 会假阴性；观测真实 trace 语料才是 ground truth。差点据此"修"一个没坏的东西。

## 真实效率痛点（合成场景没暴露的）

| 现象 | 数据 | 性质 |
|------|------|------|
| edit/multiedit 首试不中 | edit 23%、multiedit 19% "old_string not found" | **重试税非失败**：29 个失败 session **28 个恢复**、0 硬失败。crush 模糊诊断支撑恢复。浪费~20% edit 尝试=多花 turn/token |
| 60s 工具超时 | bash 166×、job_output 140×、view 51×、multiedit 40× | timeout 偏紧 / 长任务没转后台。job_output 阻塞到超时是 UX 疮——应返回当前 buffer + "仍运行"而非阻塞 60s 报错 |
| agentic_fetch 失败 | 43% (18/41) | web 抓取真不稳 |
| 上下文失控 | 3 session 达 240-338MB | 尾案；auto-summarize(63 次触发)兜底但偏晚 |

## 真实可靠的（别动）

bash 2% / view 2% / rg_tool 1% / evidence_batch 4% 失败——主线工具扎实。配合 5 个合成 PASS，核心面稳。

## 修正后的自愈优先级

1. ~~explore fan-out~~ —— **无需修，已证 work**（纠正前结论）。
2. **job_output 阻塞超时** → 改为返回当前输出+运行态，不阻塞 60s。真实高频(140×)、有界、可修。
3. **edit 重试税** → 低 ROI（已恢复），可选：更强 old_string 归一化降首试 miss。
4. **agentic_fetch 43% 失败** → 查根因。
