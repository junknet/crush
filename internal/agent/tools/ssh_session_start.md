Start a persistent remote PTY session through OpenSSH and remote `tmux`.

This creates a detached tmux session on the remote host. Use it for long-lived
commands, REPLs, servers, and interactive workflows. Follow up with
`ssh_session_output` to read the pane, `ssh_session_send` to send input, and
`ssh_session_kill` to stop it.

The remote host must have `tmux` installed. Prefer host aliases from
`~/.ssh/config`.
The local OpenSSH process runs in batch mode with a short connect timeout, so
missing keys or unreachable hosts fail instead of prompting inside the TUI.
