Run a short local script when structured code is denser than many shell calls.

Use this for one-shot analysis, JSONL aggregation, small transformations, and
evidence extraction where loops, maps, sorting, or structured parsing would be
awkward as many separate tool calls.

Parameters:
- `language`: `shell` (default), `python`, or `node`
- `script`: source code to execute
- `timeout_seconds`: optional timeout, default 60, max 300

Rules:
- Prefer native tools (`rg`, `view`, `ast_grep`) for repository search.
- Prefer `bash` with `run_in_background=true` plus `monitor` for long-running
  or externally waiting processes.
- Keep scripts small and deterministic. Print only the distilled result.
- Do not use this for package installation, daemons, interactive prompts, or
  foreground sleep polling.
