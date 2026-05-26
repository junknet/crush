# Phase 2 — SSH Workspace Driver 落地计划

接续 commit b683801 (phase 1 UX 修复 + 删 server)。本计划是
"local 中控 + 远端透明执行 + `set_workspace ssh://...` 切换上下文"
的工程拆解，目标体量 4-8K 行 / 12-20 次 round-trip。

## 现状（已落地的 in-flight）

用户已提交（前置工作）：
- `internal/agent/tools/embed_rg.go` + `embed_rg_fallback.go`
- `internal/agent/tools/bin/rg-linux-amd64` (6.3MB)
- `internal/agent/tools/rg.go` 已调 `EnsureEmbeddedToolsExist()` 注入 PATH

这意味着 ripgrep 自包含分发的"本地侧"已完成。SSH driver 直接复用同一份 embed
字节通过 SFTP 推送到远端 `~/.local/share/crush/bin/rg`。

## 包结构

```
internal/agent/iodriver/
├── driver.go          # interface + WorkspaceURI 解析
├── local.go           # LocalDriver: 包装 os.* / 本地 shell
├── ssh.go             # SSHDriver: crypto/ssh PTY + reconnect
├── sftp.go            # SSHDriver 的 fs 部分 (sftp client)
├── remote_grep.go     # 远端 rg 单 RTT 调用
├── remote_bash.go     # 持久 PTY shell session + setsid 异步任务
├── bootstrap.go       # rg 自动 SFTP 推送 + PATH 注入
├── auth.go            # ssh-agent / identity_file / password / known_hosts
├── factory.go         # URI → driver 缓存（按 host pin 复用连接）
└── driver_test.go     # local + ssh (用 dockertest 起 sshd) end-to-end
```

## Driver Interface

```go
type Driver interface {
    Kind() string                                                 // "local" | "ssh"
    WorkingDir(ctx context.Context) string

    // 文件系统（小文件，单次 read/write 走块传输）
    Stat(ctx context.Context, path string) (fs.FileInfo, error)
    ReadFile(ctx context.Context, path string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
    Remove(ctx context.Context, path string) error
    MkdirAll(ctx context.Context, path string, perm fs.FileMode) error
    Walk(ctx context.Context, root string, fn fs.WalkDirFunc) error

    // 命令执行（一次性 + 持久 shell）
    Exec(ctx context.Context, argv []string, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error)
    OpenShell(ctx context.Context) (Shell, error)  // 持久 PTY，cd / env 状态保持

    // 搜索（远端单 RTT，落地用 rg）
    Grep(ctx context.Context, opts GrepOpts) ([]GrepHit, error)
    Glob(ctx context.Context, opts GlobOpts) ([]string, error)

    // 异步任务（setsid 脱钩，断网不死，重连 tail -f）
    SpawnDetached(ctx context.Context, argv []string, logPath string) (pid int, err error)
    Tail(ctx context.Context, logPath string, fromOffset int64) (io.ReadCloser, error)

    // 临时文件 / 工作区根目录探测
    TempDir(ctx context.Context) string

    Close() error
}

type Shell interface {
    Run(ctx context.Context, line string) (string, error)  // 同步执行一条 line
    SetCwd(ctx context.Context, dir string) error
    SetEnv(ctx context.Context, kv map[string]string) error
    Close() error
}
```

## WorkspaceURI 语法

- `local` 或 `local:/path` → LocalDriver，可选 chdir
- `ssh://user@host[:port]/remote/path?identity=~/.ssh/id_ed25519&strict=true`
- 解析在 `iodriver.ParseURI`，session 字段持久化为字符串

## Session 改造

`internal/session/session.go` Session struct 加：
```go
WorkspaceURI string `json:"workspace_uri,omitempty"`
```

DB migration（goose）：
```sql
-- internal/db/migrations/00000000000008_session_workspace_uri.sql
-- +goose Up
ALTER TABLE sessions ADD COLUMN workspace_uri TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE sessions DROP COLUMN workspace_uri;
```

`task sqlc` 重生成 `sql.go`。

## Coordinator 工具注册改造

`internal/agent/coordinator.go` 的 buildAgent：
1. 工具实例化时不再传 `cfg.WorkingDir()`，改为从 `ctx` 取 `Driver`
2. Driver 通过 `iodriver.NewContext(ctx, driver)` 注入；在 RunTask 入口
   根据 `session.WorkspaceURI` 调 `iodriver.Factory.Get(uri)` 取 driver
3. tools 改造 N 处：bash / view / edit / write / multiedit / grep / glob /
   ls / crush_logs / download / fetch；每个把 `os.ReadFile` → `driver.ReadFile` 等

## 新工具 `set_workspace`

`internal/agent/tools/set_workspace.go`：
- 参数：`uri string`（可选 `validate=true` 先 ping）
- 行为：更新当前 session.WorkspaceURI，下次工具调用自动用新 driver
- 验证：解析 URI、对 ssh 立即 ping `echo ok`、bootstrap rg、报告 PATH/uname

## SSH 实现要点

1. **持久 PTY shell**（remote_bash.go）：
   - `ssh.Session.RequestPty(...)` + `Shell()`
   - stdin/stdout 用 pipe，sentinel 字符串（uuid）划分命令边界
   - 每条 `Shell.Run` 写 `cmd; echo __DONE_$uuid_$? __`，读到 sentinel 切片
   - cd / export 自然保留（同一 bash 进程）

2. **断网重连**（ssh.go）：
   - Keep-alive 30s
   - 连接挂掉后惰性重连，SpawnDetached 任务用 `setsid` 脱钩、记 PID
   - 重连后 `ps -p $PID` + `tail -f /tmp/crush-job-$id.log` 续接

3. **远端 rg**（remote_grep.go）：
   - 第一次调用时通过 SFTP 推送 `rgEmbedBytes` 到
     `~/.local/share/crush/bin/rg`（沿用本地 EnsureEmbeddedToolsExist 的字节）
   - chmod +x；后续调用直接 exec

4. **认证**（auth.go）：
   - 默认走 SSH_AUTH_SOCK (ssh-agent)
   - 兜底 `~/.ssh/id_ed25519` / `id_rsa`，passphrase 用 term 提示
   - known_hosts 校验，第一次连接走 prompt 加白

## 依赖

```
require (
    golang.org/x/crypto/ssh v0.x
    github.com/pkg/sftp v1.x
)
```

## 测试

- `iodriver/driver_test.go` 用 dockertest 起一个 sshd 容器
- 单元测试覆盖：ParseURI、Bootstrap（首次/已存在/版本不匹配）、
  Shell 状态保持、SpawnDetached + Tail、reconnect after kill -9 sshd

## 风险

1. SFTP 推送 6.3MB rg 在慢网络上首次 1-2 秒，UI 要显示进度
2. `setsid` 在 macOS sshd 默认不允许 PTY 脱钩，需走 nohup + disown 兜底
3. known_hosts TOFU 决策需要用户交互 → 走 PermissionRequest 机制

## 落地分块

- M1: iodriver 包骨架 + LocalDriver + ctx 注入（工具不动） ~600 行
- M2: 11 个 IO 工具改造走 driver（行为不变） ~600 行
- M3: SSHDriver + sftp + persistent shell ~1200 行
- M4: set_workspace + Session.WorkspaceURI + DB migration ~400 行
- M5: rg 自动推送 + 真机集成测试 ~400 行

每个 M 一个独立 commit，方便回滚。
