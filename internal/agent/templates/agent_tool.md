Spawn a sub-agent for a self-contained task.

Default role is `explore`. Use `role=explore` for read-only repository inspection, symbol walks, and evidence gathering. Use `role=worker` for edits, refactors, fixes, docs, and verification. The parent agent owns planning and final synthesis.

The explore role is Haiku-class plus a strong prompt plus tool execution. It has `bash` for read-only inspection commands, navigation tools (`glob`, `grep`, `ls`, `view`), `sourcegraph`, and the read-side `nim_*` LSP tools (`nim_definition`, `nim_hover`, `nim_references`, `nim_document_symbols`, `nim_workspace_symbols`, `nim_call_hierarchy`, `nim_check_file`, `nim_diagnostics`, `nim_macro_expand`, `nim_project_maps`, `nim_safe_to_delete`). It cannot edit, write, or commit.

Delegate when the task is self-contained and the role matches the work. Use `explore` when you need to find or read. Use `worker` when the result must mutate the workspace. Good prompts are specific: state the goal, name files/symbols already ruled out, and demand a concrete output shape. Vague prompts produce vague reports.

Skip the sub-agent for single-file lookups when you already know the path, or for follow-up that needs prior context.
