Execute one command on a remote machine through OpenSSH.

Use this for bounded, one-shot remote checks such as tests, status commands,
and file inspection that should return promptly. Prefer a host alias from
`~/.ssh/config`; Crush does not store private keys or credentials.
The local OpenSSH process runs in batch mode with a short connect timeout, so
missing keys or unreachable hosts fail instead of prompting inside the TUI.

For long-running or interactive remote work, use `ssh_session_start` instead.
For editing many remote files, use `ssh_mount` and then use local `rg`, `view`,
`edit`, and `write` against the mounted path.
