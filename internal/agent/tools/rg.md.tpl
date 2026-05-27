Search file contents or filenames using ripgrep (`rg`); regex by default, literal text when `literal_text=true`; returns results sorted by file modification time (for content) or path length (for filenames). Respects `.gitignore`.

Use `files_only=true` to search for filenames matching the pattern.

This is the ONLY content and filename search tool. Do not call `grep`, `rg`, or `find` via `bash` for repository search. Use `ast_grep` for structural code search.
