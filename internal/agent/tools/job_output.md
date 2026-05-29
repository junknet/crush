Get stdout/stderr from a background shell by ID.

Set `wait=true` to block until the shell completes — but the wait is bounded
(~50s) and returns the current output plus a "still running" status if the job
outlives it, rather than hanging. For jobs that may run longer, prefer the
`monitor` tool to wake on a completion/error pattern instead of repeatedly
blocking here.
