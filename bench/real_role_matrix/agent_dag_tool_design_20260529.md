# Agent DAG Tool Design Notes — 2026-05-29

## Role Defaults

- `explore`: `antigravity/gemini-3.5-flash-low`.
- `worker`: `antigravity/gemini-3-flash-agent`.
- `plan` / `auditor`: strongest available reasoning tier because silent error
  cost is high.

## Parallelism Model

Crush needs two independent layers of parallelism:

1. Tool-level DAG parallelism inside one agent turn.
   - `evidence_batch`: independent evidence nodes run concurrently.
   - `evidence_graph`: dependent nodes run as a DAG with output interpolation.
2. Agent-level DAG parallelism across sub-agents.
   - Brain can spawn independent `explore` / `worker` branches.
   - Scheduler can run sibling tasks concurrently when they have no deps and
     no overlapping ownership.
   - Worker-side concurrent edits must use isolated `git worktree` directories.

## Tool Design Findings

- Model habit: Gemini models naturally write familiar native tool names
  (`ls`, `view`, `rg`, `bash`) into `evidence_batch.nodes[].kind`.
- Tool API reality: the DAG tool expects semantic kinds
  (`list_tree`, `read_file`, `search_text`, `run_short_command`).
- Fix: normalize common native aliases before execution.
- Fix: do per-node validation at execution time so a single bad node does not
  discard an otherwise useful batch.
- Fix: score benchmark evidence nodes, not just outer tool calls. One
  `evidence_batch` can represent 4-8 parallel reads/searches.

## Current E2E Evidence

Run directory:
`/home/junknet/.local/state/crush-real-bench/20260529-170345`

- Worker case used one `evidence_batch` with three parallel nodes:
  `pwd`, `search_text` in `internal/agent/coordinator.go`, and `search_text`
  in `internal/agentsdk/provider.go`.
- Trace:
  `/home/junknet/.local/state/crush-dev/trace-20260529-170414-515404136-2593641.jsonl`
- Result: PASS, `tools=1/1 failed=0 nodes=3`, duration 10.0s.

Corrected Explore low run:
`/home/junknet/.local/state/crush-real-bench/20260529-170515`

- Trace:
  `/home/junknet/.local/state/crush-dev/trace-20260529-170515-174293319-2596080.jsonl`
- Result: `tools=2/2 failed=0 nodes=11`, duration 23.0s.
- It used DAG correctly but missed several expected code-level guardrail hits,
  so this is a strategy-quality failure, not a tool-layer failure.

## TUI Thinking / Progress Design

Do not expose raw hidden chain-of-thought. The TUI should expose public,
debuggable progress artifacts:

- Current task DAG: nodes, role, status, elapsed time, deps.
- Current evidence batch: node IDs, kind, status, duration, failed/skipped.
- Public reasoning summary: “what I am checking next” and “why this branch
  exists”, generated as concise status text, not provider private CoT.
- Existing provider reasoning blocks may stay expandable when the provider
  emits visible reasoning content, but they should not be required for user
  trust.

Current TUI already has partial primitives:

- Runtime status line shows DAG running count and agent profile mix.
- Runtime status line shows active tool parallelism.
- Assistant reasoning blocks are expandable/collapsible.
- Sidebar tracks recent sub-agent lifecycle.

Next UI step should be a compact “DAG activity window” built from existing
`task_*` and `tool_*` trace events, not raw CoT text.
