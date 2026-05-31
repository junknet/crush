# Worker task: stabilize Crush tool E2E flows

You are in a clean Crush repository snapshot from just before commit `09574f8`.

Fix the real end-to-end stability problems in Crush's tool/runtime flow. This is intentionally a broad engineering task: you must inspect the existing code, find the right ownership boundaries, implement the missing behavior, and verify it through real commands. Do not solve this by only satisfying one tiny test file.

Required outcomes:

1. Background shell and monitor behavior is reliable.
   - Background jobs expose enough information for later monitor calls.
   - Monitoring a completed shell can still match buffered output.
   - Monitor wakeups must not race with or duplicate generic background-done wakeups.
   - Unknown monitor targets should report known shell IDs clearly.

2. Runtime trace/session flow is stable.
   - Runtime session snapshots and cloned runs keep persistent state isolated.
   - Duplicate trace entries are suppressed without collapsing genuinely distinct entries.
   - Sub-agent trace propagation must not duplicate parent trace events.

3. Provider/tool loop stability is improved.
   - Provider retry/error logging exposes useful fields.
   - OpenAI-compatible streaming tool calls, background bash, and monitor continuation remain coherent.
   - The agent should not mirror background events into the wrong event path.

4. Replace the brittle old Nim/LSP-specific inspection surface with a general code triage tool.
   - Add a `code_triage` tool that can run multiple text queries and optional check commands.
   - It should return structured metadata: intent, query/check outcomes, evidence, risk, and next-action guidance.
   - Add the chat/UI rendering path for this tool.
   - Remove obsolete narrow Nim inspection tools from the exposed surface where appropriate.

5. Schedule wakeups remain deterministic.
   - Same-key wakeups replace older ones.
   - Different keys coexist.
   - Metadata exposes key/replacement information.
   - Cron parsing and persisted scheduler behavior continue to work.

6. Acceptance and launcher flow is product-ready enough for the above.
   - Add or update the async monitor E2E scenario.
   - Keep acceptance/common configuration isolated from the user's real config.
   - Keep launch scripts aligned with real provider usage.

Acceptance commands:

- `go build ./...`
- `go vet ./internal/agent/... ./internal/agent/tools/... ./internal/runtime/... ./internal/shell/...`
- `go test ./internal/agent/tools -run 'TestCodeTriage|TestMonitorTool|TestScheduleWakeup|TestCron|TestScheduler|TestPublishWakeup' -count=1 -v`
- `go test ./internal/runtime -run 'TestRuntimeSession' -count=1 -v`
- `go test ./internal/shell -run 'TestBackgroundShell' -count=1 -v`
- `go test ./internal/agent -run 'TestProviderRetryLogFields|TestCoordinatorPropagateSubAgentTracesDeduplicatesParentTrace|TestHandleBackgroundJobEventDoesNotMirrorToEventbus' -count=1 -v`
- `go build -o crush .`

Do not skip, weaken, or delete tests. Do not depend on git history. When done, print exactly:

`WORKER_DONE hard_e2e_flows`
