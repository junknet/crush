Spawn a read-only sub-agent to investigate something across many files or symbols.

The sub-agent has navigation tools only: `glob`, `grep`, `ls`, `view`, plus the read-side `nim_*` LSP tools (`nim_definition`, `nim_hover`, `nim_references`, `nim_document_symbols`, `nim_workspace_symbols`, `nim_call_hierarchy`, `nim_check_file`, `nim_diagnostics`, `nim_macro_expand`, `nim_project_maps`, `nim_safe_to_delete`). It **cannot** edit, write, run bash, or commit — anything mutating must happen in the parent agent.

Delegate when ALL three hold:
1. Exploration cost is high — multiple searches, recursive symbol walks, or whole-call-graph traversal.
2. The work is read-only — you only need to *find* or *read*, not change anything.
3. The question fits in a self-contained brief — the sub-agent gets only your `prompt`, with no view of this conversation's history.

Good prompts are specific: state the goal, name files/symbols already ruled out, and demand a concrete output shape (a list of paths, a quoted snippet, a definition location). Vague prompts produce vague reports.

Skip the sub-agent for: single-file lookups when you already know the path (just `view`), any task ending in an edit (do it yourself), or follow-up that needs prior context (do it yourself).
