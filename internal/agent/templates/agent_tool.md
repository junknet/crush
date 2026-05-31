为自包含的任务派生一个子智能体。

默认角色是 `explore`。角色选择规则：
- `explore`：只读代码库检查、符号遍历、证据收集（不能修改文件）
- `plan`：只读实现方案设计（不能修改文件）
- `worker`：执行编辑、重构、修复、验证（可修改文件）
- `auditor`：安全/数学/逻辑对抗审查（只读）

如果用户明确要求某个 agent（例如“让审计 agent 看看”、“调用规划 agent”、“派 worker 修”），该显式要求优先：必须调用本工具并设置对应 `role`，不要由 Brain 自己直接完成来替代委托。

## 子 agent 是事件驱动的，不是带超时的阻塞调用

子 agent 是一个完整的 agent 循环，**没有墙钟超时**：它会一直运行到自然完成、失败或意外崩溃，这三种终态**都会作为事件通知你**。不要假设有「N 秒超时」，不要 `sleep`/轮询/主动催问进度。

**默认 false（同步）**：brain await 子 agent 自然完成后拿到结果再继续。不会被超时打断（除非你或会话被取消）。适合需要立即用结果的短任务。

**设为 true（异步）**：立即返回 `agent_job_id`（这是一个 **handle**），brain 不阻塞、可继续其他工作。子 agent **完成/失败/崩溃** 时都会通过后台事件**自动唤醒**你——无需轮询。通过 handle 操作它：`Monitor`(等待)、`JobOutput`(读取结果/状态)、`JobKill`(取消/停止)。结果在完成后会持久化，即使 Crush 重启，`JobOutput(该id)` 仍能取回（或明确告知该任务已随重启丢失、需重派）。

```
# 启动后台 worker 不阻塞 brain
Agent(role=worker, prompt="实现 X 功能", run_in_background=true)
→ 返回: agent_job_id: 00A

# 然后 brain 可以做其他事，再用 Monitor 等待
Monitor(shell_id=00A, regex="agent_job_id: 00A|error:")

# 完成后用 JobOutput 取结果
JobOutput(shell_id=00A)
```

**何时用 run_in_background=true**：
- worker 预计耗时 > 1 分钟
- brain 有其他并行任务可以同时做
- 需要同时派发多个 worker 而不想串行等待

**何时同步（默认）**：
- 结果立即需要用于下一步决策
- 任务 < 3-4 次工具调用（无需派发，内联更快）
- explore 收集证据后 brain 要立即综合

## 委托准则

当任务是自包含且角色与工作匹配时委托。对于 1-3 次直接查找、已知文件读取或已知文件内的代码搜索，跳过子 agent。好的 prompt 应说明目标、列出已排除的路径/符号、要求具体输出格式。模糊 prompt 产生模糊报告。

**成本**：`explore` 并行化广泛搜索——父级链式调用 `Search` + 4-6 个 `Read` 消耗 8-15 轮并膨胀上下文；一次 `explore` 调用 1 轮内返回相同证据。范围不明确或可并行化时分发；搜索已有界时保持内联。
