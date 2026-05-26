Jump to the definition of a Nim symbol via the language server (`textDocument/definition`).

Use when you need to know *where* a symbol is declared — proc/template/macro body, type definition, variable declaration, imported module path. This is faster and more precise than text search because the LSP resolves overloads, generics, and re-exports.

Input:
- `file_path`: absolute or relative path to a Nim source file already in the workspace.
- `line`: 1-based line of the cursor on the symbol.
- `character`: 1-based column of the cursor on the symbol (point at the symbol's name, not surrounding punctuation).

Output: one or more `path:line:col` definition locations. Empty result means the LSP could not resolve the symbol (often because the cursor is on whitespace, a literal, or an unparseable region).
