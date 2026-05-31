Watch a running background job's output and get automatically woken when something happens — instead of polling `JobOutput` in a loop.

Use this after a command was moved to the background (you have a `shell_id` from the bash tool). Provide a `pattern` (regular expression). The agent turn ends immediately; you are then automatically continued when one of these occurs:

- a new output line matches `pattern` (e.g. a server printing `Listening on`, a build printing `BUILD SUCCESSFUL`, a log line containing `ERROR`)
- the job ends before the pattern ever appears
- the `timeout_seconds` window elapses with no match

Prefer this over repeatedly calling `JobOutput`: it is event-driven, costs no tokens while waiting, and does not block the session.

Typical uses:
- Wait for a dev server / database to become ready: pattern `Listening on|ready to accept connections`.
- Watch a long build/test stream for the first failure: pattern `FAILED|ERROR|panic`.
- Confirm a deploy step reached a milestone.

`pattern` is a Go regular expression matched per line. Keep `timeout_seconds` sane (default 300) so a never-matching watch eventually wakes you to decide.
