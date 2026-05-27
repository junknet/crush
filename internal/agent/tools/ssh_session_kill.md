Kill a persistent remote PTY session.

This runs `tmux kill-session` on the remote host for the session created by
`ssh_session_start`. Use it when a remote command, server, or REPL is no longer
needed.
