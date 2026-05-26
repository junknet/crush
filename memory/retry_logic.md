# Retry and Backoff Mechanism

The system has three layers of retry logic:

1. **Fantasy Transport Layer**: Built-in 4 attempts with 500ms initial exponential backoff. Handles network dial timeouts, 5xx, and 429 errors. (`fantasy-patched/streamretry/streamretry.go`)
2. **Fantasy Agent Layer**: Built-in 3 attempts with 2s initial exponential backoff for stream failures. (`fantasy-patched/agent.go`)
3. **Crush Scheduler Layer**: Implements an exponential backoff retry loop (1s, 2s, 4s, 8s, 16s, 30s cap) in `internal/scheduler/scheduler.go`.
    - **Current Setting**: `MaxRetries` is set to **10** in `internal/agent/coordinator.go` for both root and child tasks.
    - **Time Budget**: Total wall-clock time for retries per task is capped at **10 minutes**.
    - **Context Awareness**: The retry loop respects `ctx.Done()` and interrupts between attempts if the session is canceled.
    - **Context Limit Handling**: If a `context_length_exceeded` error is received from the provider, the system should automatically trigger a conversation summary (auto-compact) and then retry the task, rather than failing immediately.

**Key Files**:
- `internal/agent/coordinator.go`: Where `MaxRetries` is configured.
- `internal/scheduler/scheduler.go`: Implementation of `retryBackoff` and the retry loop logic.
