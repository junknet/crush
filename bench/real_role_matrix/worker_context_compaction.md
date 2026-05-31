# Worker task: redundant tool-result compaction + DAG sleep-block

You are in a clean Crush repository snapshot from just before commit `c22f44e`.

Implement the production behavior described below. Work directly in the repository, use the normal Crush source layout, and verify through real commands before finishing.

Goal:

1. Add redundant tool-result compaction to the agent runtime.
   - After the existing micro-compaction step, compact old redundant tool outputs in the session message history.
   - Repeated read-only tool results for the same normalized input should keep the newest useful result and replace older redundant output content with a short placeholder.
   - Recent tool results must be protected so the active turn is not stripped.
   - Successful mutating tools (`write`, `edit`, `multiedit`) may be considered redundant only by file path; tool errors must not be compacted away.
   - Normalization should tolerate JSON key ordering and harmless formatting differences.

2. Block foreground sleep-polling in `dag_run` validation for `run_short_command` nodes.
   - A node like `sleep 2 && echo done` should be rejected before execution with the same policy as the bash tool's foreground sleep guard.

3. Clean up image compression log labels so they use the current log-capitalization convention.

Acceptance:

- `go build ./...` passes.
- `go vet ./internal/agent/... ./internal/agent/tools/...` passes.
- `go test ./internal/agent -run 'TestCompactRedundantToolResults|TestNormalizeToolInput' -count=1 -v` passes.
- `go test ./internal/agent/tools -run 'TestDagRunToolBlocksForegroundSleepPolling' -count=1 -v` passes.
- Do not weaken or skip tests.

When done, print exactly:

`WORKER_DONE context_compaction`
