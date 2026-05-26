Spawn a sub-agent for a self-contained task.

Default role is `explore`. Use `role=explore` for read-only repository inspection, symbol walks, and evidence gathering. Use `role=worker` for edits, refactors, fixes, docs, and verification. The parent agent owns planning and final synthesis.

The explore role is a fast, read-only investigator. It searches and reads the repository by path (`fd`), text (`rg`), structure (`ast_grep`), and symbol (LSP-level definitions, references, call hierarchy). It chooses its own tools for the job, so give it a goal, not a method. It cannot edit, write, mutate, or commit; if the next step is a change, do it yourself or use `role=worker`.

Delegate when the task is self-contained and the role matches the work. Use `explore` for broad repo search, unknown-symbol lookup after a bounded inline pass, multi-module diagnosis, or any task where raw search output would pollute the parent context. Skip it for 1-3 direct lookups, known-file reads, or code search within 2-3 known files. Use `worker` when the result must mutate the workspace. Good prompts are specific: state the goal, name files/symbols already ruled out, and demand a concrete output shape. Vague prompts produce vague reports.

Skip the sub-agent for bounded discovery expected to finish in 3-4 tool calls, single-file lookups when you already know the path, or follow-up that needs prior context.

**Cost:** `explore` parallelises wide searches — a chain of `fd` + `rg` + 4-6 `view`s in the parent costs 8-15 turns and bloats the parent context; one `explore` call returns the same evidence in 1 turn and frees the parent for synthesis. Dispatch when the scope is unclear, broad, or parallelizable; stay inline when the search is already bounded.
