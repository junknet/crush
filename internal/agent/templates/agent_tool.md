Spawn a sub-agent for a self-contained task.

Default role is `explore`. Use `role=explore` for read-only repository inspection, symbol walks, and evidence gathering. Use `role=worker` for edits, refactors, fixes, docs, and verification. The parent agent owns planning and final synthesis.

The explore role is a fast, read-only investigator. It searches and reads the repository — by text, by meaning (semantic code search), and by symbol (LSP-level definitions, references, call hierarchy) — to locate code and gather evidence. It chooses its own tools for the job, so give it a goal, not a method. It cannot edit, write, mutate, or commit; if the next step is a change, do it yourself or use `role=worker`.

Delegate when the task is self-contained and the role matches the work. Use `explore` when you need to find or read. Use `worker` when the result must mutate the workspace. Good prompts are specific: state the goal, name files/symbols already ruled out, and demand a concrete output shape. Vague prompts produce vague reports.

Skip the sub-agent for single-file lookups when you already know the path, or for follow-up that needs prior context.

**Cost:** `explore` parallelises wide searches — a chain of `glob` + `grep` + 4–6 `view`s in the parent costs 8–15 turns and bloats the parent context; one `explore` call returns the same evidence in 1 turn and frees the parent for synthesis. Default to dispatch when scope spans more than two files.
