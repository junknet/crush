管理多步工作的结构化任务列表。每个任务有且仅有以下四种状态：

**合法 status 值（严格枚举，不允许其他值）：**
- `pending` — 待处理（初始状态）
- `in_progress` — 正在处理（同时只能有一个任务处于此状态）
- `completed` — 已完成
- `failed` — 失败（需在 content 或 active_form 中说明原因）

**禁止使用**：`pendingLocked`、`done`、`running`、`cancelled`、`blocked` 或任何其他自造状态值——工具会直接报错 `invalid status`。

**用法规则：**
- 每次调用传入**完整列表**（包含所有任务），不支持增量更新
- 同时只能有 1 个 `in_progress` 任务
- 完成一项 → 立即将其改为 `completed`，下一项改为 `in_progress`
- 简单的单步任务跳过 todos，不要为每条命令都建 todo
- 任务数量以 3-7 项为宜；过细的分解适得其反