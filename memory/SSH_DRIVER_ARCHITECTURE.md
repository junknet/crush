# SSH Driver Architecture Reference

## Core Abstractions

### 1. Driver Interface
Location: `internal/agent/iodriver/driver.go`

Key methods:
- **File I/O**: `ReadFile`, `WriteFile`, `Stat`, `Remove`, `MkdirAll`, `Walk`
- **Execution**: `Exec` (one-shot), `OpenShell` (persistent PTY for state preservation)
- **Search**: `Grep` (remote rg invocation), `Glob`
- **Async Tasks**: `SpawnDetached` (setsid-backed), `Tail` (log re-attachment)
- **Introspection**: `Kind()` returns "local" or "ssh", `WorkingDir()`, `TempDir()`

### 2. WorkspaceURI Format
```
local                           → LocalDriver, default cwd
local:/tmp/project              → LocalDriver, chdir to /tmp/project
ssh://user@host:22/remote/path?identity=~/.ssh/id_ed25519&strict=true
```

Parsed by `ParseURI()` in driver.go, results stored in session.WorkspaceURI.

### 3. Factory Pattern
Location: `internal/agent/iodriver/factory.go`

- Caches drivers by URI host+user combo
- Deduplicates SSH connections (one persistent conn per host)
- Thread-safe (sync.Mutex)
- Called by coordinator in `attachDriverCtx()` at task dispatch time

### 4. Context-Based Injection
Location: `internal/agent/iodriver/context.go`

```go
// In coordinator at task dispatch:
taskCtx = iodriver.NewContext(taskCtx, driver)

// In tool Execute:
driver := iodriver.GetDriverFromContext(ctx)
data, err := driver.ReadFile(ctx, "path")
```

## SSH Implementation Details

### PTY Shell Session (Persistent State)
Location: `internal/agent/iodriver/ssh.go` lines 571-695

Maintains bash state across multiple tool calls:
- cd operations stick (state preserved in same ssh.Session)
- export/env vars persist
- Alias definitions persist
- Currently active shell history retained

Implementation:
- Single `ssh.Session` with `RequestPty()` + `Shell()`
- stdin/stdout piped; sentinel pattern (UUID) delimits command boundaries
- Each command: `cmd; echo __DONE_$uuid_$? __`
- Parse stdout to extract exit code + output

### Auto-Reconnect & Keep-Alive
- TCP keep-alive packet every 30s
- On connection drop, next Exec/Glob/Grep triggers lazy reconnect
- Detached tasks (setsid) unaffected by network — re-attach via `ps -p $PID` + `tail -f`

### Bootstrap: Auto-Push Ripgrep
Location: `internal/agent/iodriver/bootstrap.go`

Idempotent ripgrep deployment:
1. First call to Grep triggers `bootstrapRemoteRg()`
2. SFTP writes embedded binary to `~/.local/share/crush/bin/rg`
3. chmod +x
4. Subsequent Grep calls use that binary
5. Already-installed binary skipped (idempotent)

### SFTP File Operations
Location: `internal/agent/iodriver/ssh.go` lines 200-500 (approximate)

Uses `github.com/pkg/sftp` client wrapping ssh.Client.
Operations:
- ReadFile: Full file read in single chunk
- WriteFile: Full file write (truncate-on-open)
- Stat: sftp.Stat
- Remove: sftp.Remove
- MkdirAll: sftp.Mkdir recursive
- Walk: sftp DirHandle walk

## Coordinator Integration

Location: `internal/agent/coordinator.go`

**Initialization** (lines 219-220):
```go
driverFactory:      iodriver.NewFactory(cfg.WorkingDir()),
uriRegistry:        iodriver.NewURIRegistry(),
```

**Task Dispatch** (line 607):
```go
taskCtx = c.attachDriverCtx(taskCtx, sessionID)
```

**Tool Registration** (line 1078):
```go
allTools = append(allTools, tools.NewSetWorkspaceTool(c.uriRegistry, c.driverFactory))
```

Only Brain agent gets set_workspace (guarded by `!isSubAgent`).
Sub-agents inherit driver via context to prevent concurrent workspace switches.

## Known Limitations & TODOs

1. **AllowedTools Config**: `set_workspace` may be filtered out if not in config whitelist (see PHASE2_STATUS.md)
2. **Tools Not Yet Refactored**: bash, view, edit, write, etc. still use `cfg.WorkingDir()` directly
3. **Session Schema**: WorkspaceURI field + DB migration not yet applied
4. **macOS PTY Decouple**: setsid may not work in macOS sshd; fallback to nohup + disown needed
5. **known_hosts TOFU**: First-time SSH needs user interaction (PermissionRequest mechanism pending)

## Testing

### Unit Tests
- `iodriver/driver_test.go`: Mock drivers, context injection
- `tools/set_workspace_test.go`: (if exists)

### Integration Tests (tag: integration_ssh)
- `iodriver/ssh_integration_test.go`: Real sshd (localhost:22)
  - Tests: Exec, SFTP, Glob, Grep, Bootstrap, reconnect

**Run integration tests**:
```bash
go test -tags=integration_ssh -v ./internal/agent/iodriver -run SSH
```

## Migration Path for Tools

Each tool needs a similar pattern:

**Before**:
```go
func (t *viewTool) Execute(ctx context.Context, ...) (string, error) {
    data, err := os.ReadFile(opts.FilePath)
}
```

**After**:
```go
func (t *viewTool) Execute(ctx context.Context, ...) (string, error) {
    driver := iodriver.GetDriverFromContext(ctx)
    if driver == nil {
        driver = iodriver.NewLocalDriver(t.workingDir)
    }
    data, err := driver.ReadFile(ctx, opts.FilePath)
}
```

The fallback to LocalDriver ensures backward compatibility if context is missing.
