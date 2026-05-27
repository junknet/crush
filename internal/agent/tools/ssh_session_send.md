Send input to a persistent remote PTY session.

Use this after `ssh_session_start` to type into the remote tmux pane. `text`
sends literal text. `enter=true` sends Enter. `key` sends one tmux key name
such as `C-c`, `Escape`, `Up`, `Down`, or `Enter`.

Set `read_after=true` when you need an immediate output snapshot after sending.
