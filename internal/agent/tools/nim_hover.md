Get the type signature and one-line doc for a Nim symbol via the language server (`textDocument/hover`).

Use this *before* opening a source file to read a proc/template/macro/type. The LSP returns a compact JSON payload (`AgentHoverPayload`) with:

- `signature`: full type signature (e.g. `proc(x: int, y: int): int`)
- `kind`: Nim symbol kind (e.g. `skProc`, `skTemplate`, `skType`, `skVar`)
- `srcFile`, `srcLine`: where the symbol is declared
- `docOneline`: first line of the docstring, if any

Input:
- `file_path`: absolute or relative path to a Nim source file already in the workspace.
- `line`: 1-based line of the cursor on the symbol.
- `character`: 1-based column of the cursor on the symbol.

Output: rendered fields from the payload, plus the source location. Empty result means the cursor is on a non-symbol token.
