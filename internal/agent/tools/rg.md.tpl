Search file contents using ripgrep (`rg`); regex or literal text; returns matching file paths sorted by modification time (max {{ .MaxResults }}); respects .gitignore. Use search to filter by filename, not contents.

This tool is the dedicated content-search tool. Internally it shells out to ripgrep with `--json`, so it is fast on large repos, binary-safe, and honors `.gitignore` by default. Do not call `grep` via `bash` — use this `rg` tool instead.
