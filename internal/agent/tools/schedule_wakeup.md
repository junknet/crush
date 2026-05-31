Schedule the agent to be automatically woken after a delay or on a recurring cron schedule, with a note describing what to do then.

Use this to wait on something this process cannot be notified about — state that lives in an external system the runtime can't observe: a remote CI run, a third-party deploy queue, a rate-limit window, a scheduled retry. The agent turn ends immediately; you are automatically continued when the timer fires, with `reason` handed back so you know what to check.

Two scheduling modes:

- **One-shot delay** — set `delay_seconds` (5..86400). Fires once, then is removed.
- **Recurring cron** — set `cron_expression` to a standard 5-field cron string (`minute hour day-of-month month day-of-week`). Fires every time the expression matches and is persisted across crashes (`<DataDir>/scheduled_tasks.json`). Recurring tasks auto-expire after 7 days unless re-scheduled. Examples: `*/5 * * * *` (every 5 minutes), `0 9 * * 1-5` (weekdays 09:00).

If both `cron_expression` and `delay_seconds` are given, `cron_expression` wins. Exactly one must be set.

Use `key` (or `task_key`) for wake-ups tied to a specific async task, such as `github-actions:run-12345` or `download:https://example.com/file.zip`. A new pending wake-up with the same session and key replaces older pending wake-ups by default, so stale reminders do not wake the agent after a newer attempt has superseded them. Set `replace_existing:false` only when you intentionally want multiple pending wake-ups with the same key.

The tool response metadata includes `task_id`, `key`, `replaced_count`, `next_fire_at`, and the schedule details. Use `task_id` for logging and `key` for semantic cancellation/replacement.

Do NOT use this to wait on local background jobs — those already wake you on completion (just background the command), and use the `Monitor` tool to watch their output streams. `ScheduleWakeup` is specifically for "nothing here can tell me when it changes, so check again later."

Pick the schedule to match how fast the external state actually changes — short polls (30–120s) for an active CI run, longer (600s+) or a cron expression for slow queues. Keep `reason` concrete (e.g. "re-check GitHub Actions run 12345 status", not "check").
