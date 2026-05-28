Download a file from a remote host to the local machine through `scp`.

Use this for one-off file transfers from a remote system. For frequent access
or editing many files, prefer `ssh_mount`.
The download command uses batch-mode SSH with a short connect timeout, so
missing keys or unreachable hosts fail instead of prompting inside the TUI.
