Mount a remote directory locally through `sshfs`.

Use this when you need to inspect or edit remote files as if they were local.
After mounting, use local tools such as `rg`, `view`, `edit`, and `write`
against the returned mount path.

Requires `sshfs` on the local machine. Prefer host aliases from `~/.ssh/config`.
The mount command uses batch-mode SSH with a short connect timeout, so missing
keys or unreachable hosts fail instead of prompting inside the TUI.
Do not mount huge build artifact trees or dependency directories unless needed;
sshfs can be slow on many small files.
