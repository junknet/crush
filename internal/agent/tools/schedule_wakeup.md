Schedule the agent to be automatically woken after a delay, with a note describing what to do then.

Use this to wait on something this process cannot be notified about — state that lives in an external system the runtime can't observe: a remote CI run, a third-party deploy queue, a rate-limit window, a scheduled retry. The agent turn ends immediately; you are automatically continued after `delay_seconds`, with `reason` handed back so you know what to check.

Do NOT use this to wait on local background jobs — those already wake you on completion (just background the command), and use the `monitor` tool to watch their output streams. `schedule_wakeup` is specifically for "nothing here can tell me when it changes, so check again later."

Pick `delay_seconds` to match how fast the external state actually changes — short polls (30–120s) for an active CI run, longer (600s+) for slow queues. Keep `reason` concrete (e.g. "re-check GitHub Actions run 12345 status", not "check").
