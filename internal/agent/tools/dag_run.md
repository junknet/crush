**[已废弃]** 这是 `evidence_graph` 的兼容别名，**禁止使用**，直接用 `evidence_graph` 或 `evidence_batch`。

- 独立并行子任务 → 用 `evidence_batch`
- 后续节点依赖前序节点输出 → 用 `evidence_graph`

Nodes use semantic `kind` values, not primary tool names:
- `search_text`: text search (`query`, optional `path`, `include`,
  `literal_text`)
- `search_files`: filename search (`query`, optional `path`, `include`)
- `search_structure`: AST search (`query`, optional `path`, `language`)
- `list_tree`: directory tree (`path`, optional `depth`, `ignore`)
- `read_file`: text read (`path`, optional `offset`, `limit`, `fold`)
- `check_file`: diagnostics (`path`)
- `run_short_command`: bounded command (`script` or `command`, optional
  `language`)

`run_short_command` has a 10 second deadline. It must not run servers,
background commands, remote commands, polling, or foreground sleeps.

Use `${node_id.output}` in a dependent node field only in `evidence_graph`.
Each node requires an `id`. Outputs are returned as compact `[evidence]`
sections followed by one `[summary]` section.
