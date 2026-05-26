Search for symbols across the entire workspace by name fragment (`workspace/symbol`).

Use this when you have a symbol name (or part of one) but don't yet know which file it lives in. Much faster than text search because the LSP returns precise declaration locations, not text matches in comments/strings.

Query semantics (per LSP spec): the server treats the query as a *relaxed* match — case-insensitive, characters appear in order, no strict prefix/substring requirement. Pass a short, distinctive substring of the symbol name.

Input:
- `query`: fragment of the symbol name (e.g. `"parseTick"`, `"NimChan"`). Empty string asks for all symbols, which can be very large.

Output: one line per match, `kind name @ path:line:col (container)` where `container` is the enclosing scope if any (e.g. type name for methods, module path for top-level procs).
