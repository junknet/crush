# TASK_CANDIDATE: transcript-mined Crush commit task

Selected calibration task: re-create commit `c22f44e` from its parent `a57d273`.

Why this is a better benchmark target than `apply_patch`:

- It came from real Crush work, not an isolated implementation kata.
- It crosses runtime behavior, DB-backed message history, tool validation, logging convention, and agent loop wiring.
- It is still bounded enough for an 8-cell model matrix: 5 files changed in the target commit, with objective oracle tests.
- The worker has to locate existing compaction, message, tool-result, and DAG validation patterns instead of only writing one standalone tool.

Harness shape:

- Build the benchmark binary from the current working tree with `run --role` support.
- For each case, create a git-stripped replica from `a57d273`.
- Plant target oracle tests from `c22f44e`.
- Copy real Crush provider config into an isolated config directory and override only the `worker` model for that cell.
- Run `crush run --role worker` with the task prompt.
- Verify with real commands: build, vet, targeted oracle tests, test hash/skip checks, and trace evidence that the root profile was `worker_agent`.

Files added for the calibration candidate:

- `bench/real_role_matrix/worker_context_compaction.md`
- `bench/real_role_matrix/cases_worker_context_compaction.jsonl`
- `scripts/bench_worker_commit_oracle.py`

Hard task selected for real separation:

- Target commit: `09574f8 fix: stabilize crush tool e2e flows`.
- Base commit: `f69d49a`.
- Scale: 82 files, 3050 insertions, 2368 deletions.
- Surface: agent loop, runtime session/trace, shell background monitor, schedule wakeup, general code triage tool, UI rendering, acceptance scripts and launchers.
- Why hard: the target is not one standalone implementation. It requires understanding how background jobs, monitor wakeups, trace dedupe, provider streaming tool calls, tool registration, and UI rendering interact.
- New files:
  - `bench/real_role_matrix/worker_hard_e2e_flows.md`
  - `bench/real_role_matrix/cases_worker_hard_e2e.jsonl`

Hard-task smoke:

```bash
GOEXPERIMENT=greenteagc go build -o /tmp/crush-bench .
CRUSH_BENCH_BIN=/tmp/crush-bench scripts/bench_worker_commit_oracle.py --task hard_e2e_flows --only gemini_medium --timeout 1 --keep-replica
```

Hard-task full dry run:

```bash
GOEXPERIMENT=greenteagc go build -o /tmp/crush-bench .
CRUSH_BENCH_BIN=/tmp/crush-bench scripts/bench_worker_commit_oracle.py --task hard_e2e_flows --only gemini_medium --timeout 1800 --keep-replica
```

Recommended dry run:

```bash
GOEXPERIMENT=greenteagc go build -o /tmp/crush-bench .
CRUSH_BENCH_BIN=/tmp/crush-bench scripts/bench_worker_commit_oracle.py --only gemini_medium --timeout 900 --keep-replica
```

Observed calibration result:

- `gemini_medium` (`antigravity/gemini-3.5-flash-low`) closed-loop PASS.
- Duration: 246.483s.
- First event latency: 12.370s.
- Turns/requests: 60.
- Tools: 58 started, 58 finished, 0 failed.
- Reasoning tokens: 24458.
- Root evidence: `worker_agent/gemini-3.5-flash-low`.

Interpretation: this task is valid for harness calibration and tool/runtime
observability, but it is probably still too easy for final accuracy separation
because the weakest matrix cell passed. Use a larger transcript-mined task (for
example `09574f8 fix: stabilize crush tool e2e flows`) for the final hard matrix
after this harness is generalized to a task-spec file.

Full matrix:

```bash
GOEXPERIMENT=greenteagc go build -o /tmp/crush-bench .
CRUSH_BENCH_BIN=/tmp/crush-bench scripts/bench_worker_commit_oracle.py --timeout 1800
```
