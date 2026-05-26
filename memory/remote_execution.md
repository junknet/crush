# Remote Execution Design (SSH-Backed Tools)

## Core Strategy
- **Local Control, Remote Execution**: The main LLM logic and TUI state stay local, while tool execution (bash, grep, file I/O) is proxied to remote servers via SSH.
- **AOP Driver Pattern**: Tools are abstracted using a `Driver` interface (e.g., `LocalDriver` vs. `SSHDriver`) to allow transparent switching between local and remote environments.
- **Persistent SSH Sessions**: For the `bash` tool, maintain a long-lived SSH PTY session to preserve shell state (current directory, environment variables, virtual environments) across tool calls.
- **POSIX-Native Detachment (Preferred over Tmux)**:
    - Use `setsid` and log redirection (`setsid your_command > /tmp/crush-job.log 2>&1 &`) for long-running tasks.
    - This ensures zero-dependency on the remote machine (avoiding `tmux` requirement).
    - Use `ps -p <PID>` and `tail -f` for job re-attachment.
- **Self-Provisioning**: The proxy driver automatically uploads required binaries (like `rg`) to the remote machine (e.g., to `~/.local/share/crush/bin/rg`) if missing.

## Key Decisions
- Avoid SSHFS/SFTP mounting due to I/O amplification and latency.
- Use `github.com/pkg/sftp` for direct file reads/writes.
- Maintain status inheritance via a single persistent SSH PTY Bash session.

## Next Steps
- Implement `WorkspaceDriver` interface to abstract `ExecuteCmd`, `ReadFile`, `WriteFile`, `Stat`, `Walk`, `Grep`, and `Glob`.
- Refactor all 11 IO-related tools in `internal/agent/tools/` to use the driver abstraction.
- Implement `set_workspace` tool to switch context (e.g., `remote://user@host:/path`).

## Implementation Plan
- A detailed Phase 2 implementation plan for SSH Workspace Driver is available in `memory/PHASE2_SSH_DRIVER_PLAN.md`.
