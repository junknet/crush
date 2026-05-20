List all top-level and nested symbols in a single Nim file via the language server (`textDocument/documentSymbol`).

Use this to get a structural outline (procs, templates, macros, types, vars, consts) before reading a large file — much faster than scanning the source by hand. Returns a hierarchical tree: child symbols (e.g. fields of an object, helpers inside a template body) appear indented under their parent.

Input:
- `file_path`: absolute or relative path to a Nim source file already in the workspace.

Output: indented tree of `kind name [line:col]` entries. Empty result means the file has no top-level declarations (e.g. it's empty or comments-only).
