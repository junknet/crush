# 代码三段式定位工具（Code/Bug Triage）

该工具用于“先搜寻再执行快速校验，再做轻量分析”的定位流程，避免从 `bash` 直接跑大范围搜索+长脚本导致的低效。

`bug_triage` 是 `code_triage` 的兼容别名，参数完全一致，适用于缺陷定位场景。

## 输入

- `intent`：语义意图，建议使用 `inspect`、`understand`、`locate_bug`、`review`、`verify`、`refactor`。该字段会进入 metadata，帮助后续步骤判断“下一步该做什么”。
- `queries`：一组 `rg` 查询任务，至少一项可用（`query` 必填）。
  - `path`：搜索路径，默认当前工作目录。
  - `include`：文件过滤 glob（例如 `**/*.go`、`*.nim`）。
  - `files_only`：仅按文件名搜索（对应 `rg` 文件名模式）。
  - `literal_text`：将关键词按文本而非正则处理。
- `check_commands`：可选的编译/测试命令清单（`shell`/`python`/`node`）。
  - 空命令会被忽略。
  - `timeout_seconds` 可覆盖默认超时。
- `timeout_seconds`：给每个 rg 查询默认超时（秒，默认 20，最大 120）。
- `max_results`：每次查询返回的最大命中条数（默认 50，最大 200）。

## 适用场景

- 先并行跑多条证据查询，再串行跑 1~N 条 compile/check；`code_triage` 和 `bug_triage` 都支持同样能力。
- 只读探索阶段优先用 `queries`；需要确认可执行性再加 `check_commands`。
- 输出附带结构化 metadata，包含 `evidence` 折叠摘要和 `guidance` 下一步建议，方便 LLM 做后续决策，不必再次解析全文。
- 单个查询失败会以 `queries[].outcome=failed` 和 `queries[].error` 返回，不会让整次 triage 丢失其他证据。

## 使用原则

- 不要用它替代所有底层工具。它适合“带意图的多证据定位”；明确只读单文件时继续用 `view`，明确单模式搜索时继续用 `rg`。
- 当任务目标是定位、理解、review 或验证时，优先用本工具包装底层搜索和短校验，避免直接把裸 `rg`/shell 输出交给 LLM 自行归纳。
