Edit, create, delete, and move files with a single structured patch. This is the primary editing tool: it applies one envelope describing changes across one or more files atomically (all hunks must locate cleanly or the whole call aborts and nothing is written).

The `patch` argument is a plain-text envelope:

```
*** Begin Patch
*** Update File: path/to/file.go
@@ optional context header (e.g. the enclosing func)
 unchanged context line
-removed line
+added line
*** End Patch
```

## Operations

- `*** Add File: <path>` — create a new file. Every following line starts with `+`; that text (minus the `+`) becomes the file content. Fails if the file already exists.
- `*** Delete File: <path>` — remove an existing file. Fails if it does not exist.
- `*** Update File: <path>` — edit an existing file with one or more hunks. May be immediately followed by `*** Move to: <new/path>` to rename/move the file while applying the edits.

## Hunk format (Update File)

- `@@` or `@@ <context>` begins a hunk. The text after `@@ ` is an informational anchor (usually the enclosing function/class); it is NOT required to match exactly. Multiple hunks per file are allowed and are applied in source order.
- Each subsequent line is a diff line:
  - ` ` (leading space) = context line, present in both old and new.
  - `-` = line removed from the file.
  - `+` = line added to the file.
- `*** End of File` after a hunk's lines anchors that hunk to the end of the file (use it when adding lines at the very end).

## Locating context

The applier finds each hunk's `(context + removed)` block in the file with increasing tolerance: exact match first, then ignoring trailing whitespace, then ignoring leading indentation (with the added lines reindented to the file's actual indentation). If a hunk's context cannot be located UNIQUELY, the entire patch is rejected — fix the context and retry the whole patch.

## Rules

- **Preserve exact indentation.** Copy leading whitespace (tabs vs spaces, exact count) verbatim from the file into context and removed lines. Indentation drift is the most common reason a hunk fails to apply.
- **Strip any `view` line-number prefix.** `view` prints `<number>|<content>`; only the part after `|` belongs in the patch.
- **Give enough context to be unique.** Include a few surrounding ` ` context lines so the hunk matches exactly one location. If it matches several, add more context.
- **Do not wrap the envelope in a markdown code fence** unless unavoidable; a single ```` ```patch ```` fence around the whole thing is tolerated and stripped, but bare envelopes are preferred.

## Example

```
*** Begin Patch
*** Update File: internal/app/run.go
@@ func Run() error {
 	cfg, err := loadConfig()
 	if err != nil {
-		return err
+		return fmt.Errorf("run: load config: %w", err)
 	}
*** End Patch
```
