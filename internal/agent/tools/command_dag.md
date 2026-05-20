Execute a structured DAG of shell commands with per-node trace and semantic exit interpretation.

<execution_model>
- Each command node has a unique `id`, a `command`, and optional `deps`.
- Nodes with satisfied dependencies run concurrently up to `max_parallel`.
- A node runs only after all dependency nodes semantically succeed.
- If a dependency fails, dependent nodes are skipped with `outcome=skipped_dependency_failed`.
- Each node runs in an independent shell. State does not persist between nodes.
</execution_model>

<exit_semantics>
- `exit_code` always preserves the process exit code.
- `outcome` is the interpreted command meaning.
- `grep` / `rg` with exit code `1` and empty stdout is `outcome=no_match`, `success=true`.
- Other non-zero exits are `outcome=failed`, `success=false`.
- Context cancellation or timeout is `outcome=interrupted`, `success=false`.
</exit_semantics>

<usage_notes>
- Prefer this tool over multiple bash calls when command dependencies matter.
- Use `bash` for one-off interactive shell commands and background jobs.
- Use explicit IDs that describe each node, for example `read-config`, `run-tests`, `inspect-trace`.
- Keep commands short and quote arguments carefully.
- Use `timeout_seconds` for commands that may hang.
</usage_notes>

