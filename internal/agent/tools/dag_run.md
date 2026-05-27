Deprecated compatibility alias for `evidence_graph`.

Use `evidence_batch` for independent parallel repository evidence. Use
`evidence_graph` only when a later evidence node depends on output from an
earlier node.

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
