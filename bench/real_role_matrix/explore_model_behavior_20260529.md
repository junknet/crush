# Explore Model Behavior Evaluation — 2026-05-29

Real E2E run directory:
`/home/junknet/.local/state/crush-real-bench/20260529-164801`

Test prompt:
`bench/real_role_matrix/explore_pit_production_triage.md`

The test intentionally stays inside the Explore Agent boundary: read-only
repository inspection, evidence collection, path/function discovery, and
compact reporting back to a parent agent. It does not ask Explore to make a
production decision.

## Result Summary

| Model | Wall time | Tools | Score | Trace |
|---|---:|---:|---:|---|
| `antigravity/gemini-3.5-flash-extra-low` | 24.5s | 2/3, 1 failed | 12/18 | `/home/junknet/.local/state/crush-dev/trace-20260529-164801-653466111-2555462.jsonl` |
| `antigravity/gemini-3.5-flash-low` | 27.5s | 2/3, 1 failed | 11/18 | `/home/junknet/.local/state/crush-dev/trace-20260529-164827-200975747-2556666.jsonl` |
| `antigravity/gemini-3-flash-agent` | 25.1s | 2/3, 1 failed | 12/18 | `/home/junknet/.local/state/crush-dev/trace-20260529-164855-745651431-2557952.jsonl` |

All three models completed the user-visible summary, but all three had the
same first-tool failure. This means the run is more useful as tool/prompt
interface evidence than as a clean model ranking.

## Shared Error Pattern

All three models attempted to put native tool names into
`evidence_batch.nodes[].kind`:

- extra-low: `{"kind":"ls"}`
- low: `{"kind":"ls"}`
- flash-agent: `{"kind":"ls"}`

The tool accepted only semantic DAG kinds:

- `list_tree`
- `read_file`
- `search_text`
- `search_files`
- `search_structure`
- `check_file`
- `run_short_command`
- `web_search`
- `web_fetch`

Observed error:

```text
unsupported kind "ls"; use search_text, search_files, search_structure,
list_tree, read_file, check_file, run_short_command, web_search, or web_fetch
```

Root cause: the Explore prompt tells the model it can use `ls`, while the
batch tool schema calls the same operation `list_tree`. Small models naturally
prefer familiar tool names (`ls`, `view`, `rg`) over internal semantic names.

## Model Habits

### gemini-3.5-flash-extra-low

- Fast enough, but uses shallow tool planning.
- After the first `ls` kind failure, it recovered and produced a plausible
  evidence map.
- It was more likely to cite newly discovered files such as
  `docs/contracts/known_time_contract.md` and
  `docs/registry/dataset_pit_reconstruction.md`, but missed some benchmark
  target paths.
- Best fit: cheap broad scan where a parent agent will verify details.

### gemini-3.5-flash-low

- Similar speed to extra-low, not materially faster in this run.
- Better wording discipline and uncertainty framing than extra-low.
- Still hit the same `ls` kind failure and used only three batch attempts.
- Best fit: default Explore when the task needs cleaner summaries but not deep
  reasoning.

### gemini-3-flash-agent

- Did not beat the smaller models on speed in this run.
- Output had stronger guardrail/code structure: it found
  `kit/signals_schema.py` and `assert_signal_pit`, which the smaller models
  missed.
- Same first-tool `ls` kind failure means stronger model intelligence did not
  overcome an avoidable tool API mismatch.
- Best fit: Explore for more technical code-path discovery, not cheap scanning.

## Prompt Design Findings

- Explore role boundary should remain read-only evidence collection.
- The prompt must not blur final decision duties into Explore. Production
  decisions belong to Plan/Auditor.
- The prompt should explicitly name the semantic `evidence_batch` kinds because
  models otherwise map directory listing to `ls`.
- Asking for “current production truth” made the first version too close to
  Audit/Plan. The corrected prompt asks for “evidence map” and “unresolved
  questions.”

## Tool Design Findings

- `evidence_batch` should be tolerant of common native aliases:
  `ls -> list_tree`, `view/cat -> read_file`, `rg/grep -> search_text`,
  `glob/find -> search_files`, `bash/shell/run -> run_short_command`.
- Tool errors should preserve successful sibling nodes where possible. The
  current validation rejects the whole batch before executing any node when one
  kind is unsupported, wasting a full model round.
- The tool description is correct but not enough; models follow examples and
  familiar names more strongly than schema prose.
- A compact example in the tool description would likely reduce first-round
  failures.

## Parameter/Scoring Findings

- Raw keyword score alone is not enough. It can mark a summary as close even
  when the tool strategy failed.
- Score needs separate dimensions:
  - `coverage`: required evidence terms and paths.
  - `tool_health`: failed tool count and unsupported-kind count.
  - `read_depth`: number of successful reads/searches, not just total tools.
  - `role_boundary`: forbidden decision phrases for Explore.
  - `latency`: first event, first text, and total wall time.
- For Explore, a good score should reward “correctly deciding what to read” as
  much as final prose quality.

## Fixes Applied

- `internal/agent/tools/dag_run.go`: normalize common native aliases in
  `evidence_batch` / `evidence_graph` node kinds.
- `internal/agent/templates/explore.md.tpl`: explicitly tells Explore to use
  `list_tree`, `read_file`, `search_text`, and `search_files` inside
  `evidence_batch`.

## Post-Fix Real Verification

Run directory:
`/home/junknet/.local/state/crush-real-bench/20260529-165234`

| Model | Wall time | Tools | Failed tools | Score | Trace |
|---|---:|---:|---:|---:|---|
| `antigravity/gemini-3.5-flash-extra-low` | 23.1s | 2/2 | 0 | 10/18 | `/home/junknet/.local/state/crush-dev/trace-20260529-165234-361188109-2566532.jsonl` |
| `antigravity/gemini-3.5-flash-low` | 25.6s | 2/2 | 0 | 14/18 | `/home/junknet/.local/state/crush-dev/trace-20260529-165303-452055983-2568132.jsonl` |
| `antigravity/gemini-3-flash-agent` | 37.7s | 3/3 | 0 | 14/18 | `/home/junknet/.local/state/crush-dev/trace-20260529-165330-016757022-2569326.jsonl` |

The unsupported-kind failure is gone. The remaining gap is no longer a tool
schema failure; it is reading strategy. The smaller models tend to do one broad
search batch, one read batch, then summarize. `gemini-3-flash-agent` reads one
extra batch and finds more external/root evidence, but pays about 12 seconds of
extra latency on this case.

Updated role guidance:

- `gemini-3.5-flash-extra-low`: fastest cheap scan; parent must verify.
- `gemini-3.5-flash-low`: best default Explore speed/quality tradeoff for
  evidence summaries.
- `gemini-3-flash-agent`: use when Explore must discover deeper code/contract
  links and the extra latency is acceptable.

## Benchmark Correction

The first bench runner version accidentally hard-coded the Explore role to
`antigravity/gemini-3.5-flash-extra-low` inside `state_yaml()`. Case names such
as `explore_gemini_low` were therefore misleading: the report label said low,
but the trace showed extra-low. This has been fixed so each case writes the
target model into the matching role (`explore`, `worker`, `plan`, `auditor`, or
`brain`).

Corrected run directory:
`/home/junknet/.local/state/crush-real-bench/20260529-170515`

Corrected `gemini-3.5-flash-low` result:

- Trace: `/home/junknet/.local/state/crush-dev/trace-20260529-170515-174293319-2596080.jsonl`
- Tool calls: 2/2, failed 0
- Evidence DAG nodes: 11
- Duration: 23.0s
- Score: 9/18, below the 12-hit threshold

Interpretation: true `gemini-3.5-flash-low` uses the evidence DAG correctly and
quickly, but it tends to satisfy “guard code” requests with contract documents
unless the task explicitly asks for concrete code files and function names.
This makes it a good default Explore model for broad evidence gathering, but
not a substitute for Auditor/Plan on high-silence-cost judgments.

## Next Real Verification

Rerun:

```bash
python3 scripts/bench_real_roles.py \
  --only explore_gemini_extra_low \
  --only explore_gemini_low \
  --only explore_gemini_agent \
  --timeout 240
```

Expected result after the alias fix: no first-round `unsupported kind "ls"`
failures. Model comparison should then reflect actual reading strategy and
summary quality instead of tool schema friction.
