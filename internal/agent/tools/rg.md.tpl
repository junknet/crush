Search file contents using ripgrep (`rg`); regex by default, literal text when `literal_text=true`; returns matching lines sorted by file modification time (max {{ .MaxResults }}). Respects `.gitignore`.

This is the ONLY content-search tool. Do not call `grep`, `rg`, or `find` via `bash` for repository search. Use `fd` for filename/path search and `ast_grep` for structural code search.
