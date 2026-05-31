Apply multiple find-and-replace edits to a single file in one operation; edits run sequentially. Prefer over edit for multiple changes to the same file.

Each edit follows the same exact-match rules as `edit`:
- Strip the `view` `<number>|` line-number prefix; match only the content after the `|`, and copy its indentation (tabs vs spaces) verbatim.
- Use the smallest UNIQUE old_string per edit (2-4 lines).
- Large old_string blocks are rejected. Split broad changes into small unique replacements or use `Write` after reading the whole file.

A later edit's old_string must match the content AFTER earlier edits in the batch have applied. If any one edit's old_string is not found, that edit fails; when none apply you get a per-edit diagnostic — fix the specific edit it names rather than re-sending the whole batch unchanged.
