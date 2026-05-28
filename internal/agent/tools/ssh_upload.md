Upload a local file to a remote host through `scp`.

Use this for one-off file transfers to a remote system. For frequent access
or editing many files, prefer `ssh_mount`.
The upload command uses batch-mode SSH with a short connect timeout, so missing
keys or unreachable hosts fail instead of prompting inside the TUI.
