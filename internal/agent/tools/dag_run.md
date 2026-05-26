Execute a small local DAG in one tool call.

Use this when several independent reads/searches/short scripts can run in one
structured execution graph instead of many LLM turns.

Supported node tools:
- `fd`: filename/path search (`pattern`, optional `path`)
- `rg`: content search (`pattern`, optional `path`, `include`, `literal_text`)
- `view`: read a text file (`file_path`, optional `offset`, `limit`, `fold`)
- `run`: short script (`language`: shell/python/node, `script`)
- `shell`: short shell command (`command`)

Parameters:
- `nodes`: DAG nodes, each with unique `id`, `tool`, optional `depends_on`
- `max_parallel`: max concurrent ready nodes, default 4, max 16
- `timeout_seconds`: whole-DAG timeout, default 120, max 600

Dependency output interpolation:
- Use `${node_id.output}` inside string fields to insert a dependency output.
- Keep inserted outputs small; each node output is capped in the final result.

Rules:
- Use this for short, bounded work that benefits from parallelism.
- Do not use for long-running servers, cloud polling, deploy waits, or
  foreground sleep. Use `bash` with `run_in_background=true` and `monitor`.
- Do not use for mutations unless the user explicitly asked and the graph is
  small enough to audit.
