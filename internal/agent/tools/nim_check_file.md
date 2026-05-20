Check a single Nim file for compiler errors, warnings, and hints. Stricter and quieter than `nim_diagnostics`: this tool **requires** a file path, refreshes that file with the LSP, waits for fresh diagnostics, and reports **only** that file's results (no project-wide noise).

Use this after editing a Nim file to confirm the change compiles, or before reading an unfamiliar file to spot known-broken regions. Prefer this over `nim_diagnostics` whenever you have a specific file in mind — it's the same nimlsp checker behind the MCP `nimCheckFile`, surfaced as a Crush tool.

Input:
- `file_path`: absolute or relative path to a Nim source file.

Output: one diagnostic per line, `Severity: path:line:col [source] message`. Empty result means the file currently compiles clean.
