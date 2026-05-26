Search and optionally rewrite code using Abstract Syntax Tree (AST) patterns via `ast-grep`.

This tool is much more powerful than `rg` for code because it understands the structure of the source code. It can match nested expressions, handle comments/whitespace flexibly, and even perform structural rewrites.

### Parameters:
- `pattern`: The AST pattern to search for (e.g., `fmt.Println($MSG)`). Use `$` followed by a name to create a "metavariable" that matches any AST node.
- `path`: (Optional) The directory or file to search in. Defaults to the current working directory.
- `rewrite`: (Optional) An AST pattern to rewrite the matches to. If provided, the tool will modify the files.
- `lang`: (Optional) The language of the pattern (e.g., `go`, `javascript`, `python`, `rust`). If not provided, `ast-grep` will attempt to detect it based on file extensions.

### Examples:
- Find all calls to a function: `ast_grep(pattern="myFunc($$$ARGS)")`
- Find and replace: `ast_grep(pattern="if ($COND) { return true } else { return false }", rewrite="return $COND", lang="javascript")`
- Find specific struct fields in Go: `ast_grep(pattern="type $NAME struct { $$$FIELDS }", path="pkg/models")`

AST patterns use `$VARIABLE` for single nodes and `$$$VARIABLES` for multiple nodes/arguments.
