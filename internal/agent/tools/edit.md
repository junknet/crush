Edit a file by exact find-and-replace. Also creates a file (empty old_string) or deletes content (empty new_string). For renames/moves use bash; for large rewrites use write.

`old_string` must match the file's current bytes EXACTLY — every space, tab, and newline. To get it right:

- **Strip the `view` line-number prefix.** `view` prints each line as `<number>|<content>` (e.g. `45|\tfoo()`). Match only the part AFTER the `|`. NEVER include the `<number>|` prefix in old_string or new_string.
- **Copy indentation verbatim.** Preserve the exact leading whitespace as it appears after the `|` — tabs vs spaces and the exact count. Do not reflow or guess indentation; a tab-vs-space or count mismatch is the most common cause of "old_string not found".
- **Use the smallest UNIQUE old_string** — usually 2-4 adjacent lines is enough to identify the target. Avoid pasting 10+ lines. If old_string is not unique, add a little surrounding context (or use replace_all to change every occurrence).
- If an edit fails with "old_string not found", re-`view` the exact region and copy the bytes shown between the whitespace markers; do not retry the same string.
