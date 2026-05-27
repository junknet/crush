Read recent output from a persistent remote PTY session.

This captures the remote tmux pane created by `ssh_session_start`. It is a
snapshot, not a streaming tail. If you need to wait for a condition, read
periodically only when useful; do not flood the conversation with repeated
output.
