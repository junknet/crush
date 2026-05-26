# Developer Bootstrap Notes

## crush-dev Launcher Behavior (Critical for Development)

### How crush-dev Works
- **Rebuilds on every launch**: `crush-dev` always rebuilds main.go from the current checkout via `go build`
- **Cache**: Binary cached in `$XDG_CACHE_HOME/crush-dev/` (not shared with prod `crush-prod`)
- **Timing**: Go build is content-addressed (unchanged tree = fast relink ~300ms, changed tree picks up diff)
- **Process lifecycle**: The built binary is exec'd in foreground; existing crush instances remain running

### Critical Understanding
**The process you're IN is frozen at startup time.** Even if you edit code and `crush-dev` rebuilds the binary on next launch, the CURRENT running crush session sees its tool list as fixed (evaluated when the binary started).

### Workflow for Testing Code Changes
1. Make code changes (e.g., add/modify tools, change coordinator wiring)
2. Exit the current crush session (Ctrl+C or `exit`)
3. Run `crush-dev` again → it rebuilds automatically → spawns new process with updated tool list
4. New session reflects all code changes

### Example: set_workspace Tool
- Code was added in commits 7494619 and f156074
- Tool is properly registered in `coordinator.go:1079` (unconditional append for brain agent)
- Tool appears in `allToolNames()` config list
- BUT: Running old crush-dev session doesn't have it → `crush_info` shows 32 tools (missing set_workspace)
- FIX: Kill old session, rerun `crush-dev` → rebuilds → new session has 33 tools with set_workspace

## Build Commands Reference
- **Full rebuild**: `task build` (rebuilds main binary into `$XDG_CACHE_HOME/crush-prod`)
- **Dev rebuild**: `crush-dev` launcher (always rebuilds to its own cache)
- **Test rebuild**: `crush-test` launcher (rebuilds + runs tests)
- **Source build**: `cd /home/junknet/Desktop/_cli_bases/crush && CGO_ENABLED=0 go build -o /tmp/crush .`

## Tool Integration Pattern (iodriver Refactor)

### Context-Based Driver Routing (May 2026)
With SSH driver integration (M1-M5), tools now receive a `Driver` via context:

**In coordinator (line 607)**:
```go
taskCtx = c.attachDriverCtx(taskCtx, sessionID)
```

**In tool Execute method (NEW PATTERN)**:
```go
driver := iodriver.GetDriverFromContext(ctx)
if driver == nil {
    driver = iodriver.NewLocalDriver(t.cfg.WorkingDir())
}
// Use driver instead of direct os.* calls
data, err := driver.ReadFile(ctx, "/path/to/file")
```

### Tools Currently Requiring Refactor (M2 Blockers)

11 tools still hardcoded to local I/O:
- `bash.go` — needs `driver.OpenShell(ctx)`
- `view.go` — needs `driver.ReadFile(ctx, path)`
- `grep.go` — needs `driver.Grep(ctx, ...)`
- `glob.go` — needs `driver.Glob(ctx, ...)`
- `ls.go` — needs `driver.Walk(ctx, ...)` + `driver.Stat(ctx, ...)`
- `edit.go`, `write.go`, `multiedit.go` — need `driver.WriteFile(ctx, path, data)`
- `download.go`, `fetch.go`, `crush_logs.go` — need driver read/write

**Symptom**: After `set_workspace ssh://...`, tools still execute locally instead of remotely.

### Testing SSH Tool Integration

Once refactored, verify with:
```bash
# Start crush-dev
crush-dev

# In crush session:
set_workspace(uri="ssh://root@47.110.255.240/root", validate=true)

# Should now execute on remote:
bash(cmd="pwd")           # Should return /root, not local pwd
view(file_path="/etc/hostname")  # Should read remote file
grep(pattern="...", path="/tmp")  # Should grep on remote machine
```

Expected output: All commands execute on SSH host, not local.

### PTY Shell State Preservation (bash.go)

The SSH driver's PTY session (`OpenShell()`) preserves state:
```bash
bash(cmd="cd /tmp")
bash(cmd="pwd")  # Should return /tmp, not previous cwd
bash(cmd="export MY_VAR=test")
bash(cmd="echo $MY_VAR")  # Should echo "test"
```

This differs from spawning a new local shell each time (which has no state).

## Tool & Workflow Optimization (May 2026)

### Default Tool Timeout (60s)
Tool execution is wrapped with a default timeout in `internal/agent/coordinator.go`. It has been increased from **3s to 60s** to accommodate complex engineering tasks (compilation, large greps, LSP checks).

### To-Do System "Failed" Status
- A new `failed` status was added to `TodoStatus` in `internal/session/session.go`.
- Agents should use the `todos` tool to mark tasks as `failed` if they hit blocking errors.
- Visualized in the sidebar as `(N failed)` and in chat with a red `×`.

### Agent Iteration Drive
To prevent the agent from stopping prematurely when tasks remain, a strong system reminder is injected whenever the todo list is non-empty. This instructs the agent to continue working until all tasks have a terminal status (completed or failed).

## Mobile Development & Testing
- **Test Device**: 192.168.0.106:5555 (Android)
- **App Package**: com.junknet.crushmobile
- **Connection**: `adb connect 192.168.0.106:5555`
- **Performance**: High emphasis on `React.memo` and reference stability in `app/index.tsx` due to the size of the component tree. Always check `displayMessages` aggregation stability when modifying message flow.

### Mobile Performance & NATS Bridge Patterns (May 2026)

**1. NATS Bridge Congestion (Critical)**
- **Problem**: Sending massive `tool_result` payloads (e.g. 500KB logs) over the NATS bridge to mobile devices blocks the JS thread during JSON decoding and object allocation.
- **Fix**: Truncate large payloads at the Relay layer (`internal/relay/events.go`). Limit `ToolResult.Content` to 8KB for mobile mirrors.

**2. React Native "Render Storms"**
- **Problem**: Fast event playback (history replay) can trigger dozens of React renders in a few seconds, overwhelming the CPU.
- **Pattern**: Use debounced state updates for high-frequency streams. In `app/index.tsx`, initial history sync uses a 300ms debounce to collapse hundreds of events into 1-2 renders.

**3. FlatList Bottom-Loading Performance**
- **Problem**: `scrollToEnd` on a `FlatList` without `getItemLayout` is $O(N)$ because it must measure every item from the top.
- **Requirement**: Always implement `getItemLayout` with a heuristic height for chat lists to enable instant $O(1)$ jumping and scrolling.

**4. Regex Caching for Markdown**
- Handcrafted Markdown parsers in JS are slow. Use global LRU-style caches for expensive regex operations (`preprocessHtmlToMarkdown`, `renderMarkdownInline`) to keep streaming "typewriter" effects smooth at 60fps.
