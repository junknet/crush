Execute shell commands; long-running commands automatically move to background and return a shell ID.

<cross_platform>
Uses mvdan/sh interpreter (Bash-compatible on all platforms including Windows).
Use forward slashes for paths: "ls C:/foo/bar" not "ls C:\foo\bar".
Common shell builtins and core utils available on Windows.
</cross_platform>

<execution_steps>
1. Directory Verification: If creating directories/files, use LS tool to verify parent exists
2. Security Check: Banned commands ({{ .BannedCommands }}) return error - explain to user. Safe read-only commands execute without prompts
3. Command Execution: Execute with proper quoting, capture output
4. Auto-Background: Commands exceeding 5 seconds (default, configurable via `auto_background_after`) automatically move to background and return shell ID
5. Output Processing: Truncate if exceeds {{ .MaxOutputLength }} characters
6. Return Result: Include errors, metadata with <cwd></cwd> tags
</execution_steps>

<usage_notes>
- 命令必填，working_dir 可选（默认当前目录）
- 重要：优先用 `Grep`/`Find`/`Batch` 工具代替 shell 搜索命令；范围很宽时用 `agent(role=explore)`。在**项目目录内**查看文件结构用 `ReadDir` 工具代替 `cat`/`head`/`tail`/`ls`；对项目目录**外部**（如 `~/Desktop`、`/home`、系统路径）或需要 `-la` 等选项时，直接在 bash 里用 `ls`。
- bash 里的常见 `grep`/`egrep`/`fgrep` 参数会在检测到 `rg` 时自动改写为 `rg` 执行；没有 `rg` 时按原始 `grep` 正常执行。代码内容和文件名搜索仍优先用 `Grep`/`Find`/`Batch` 工具，结构化代码搜索用 `Batch` 的 `search_structure` 节点。
- **NEVER use foreground sleep polling**: commands like `sleep 10 && status-check`, `sleep 20; job-output`, or long standalone `sleep` are blocked. Use `run_in_background=true` and `Monitor`, or `ScheduleWakeup` for a pure time delay.
- Chain with ';' or '&&', avoid newlines except in quoted strings
- Each command runs in independent shell (no state persistence between calls)
- Prefer absolute paths over 'cd' (use 'cd' only if user explicitly requests)
</usage_notes>

<background_execution>
- Set run_in_background=true for long-running processes (servers, watchers, polling).
- Returns a shell ID. Use JobOutput (with wait=true to block) or Monitor (regex).
- NEVER use `&` in command; use the tool parameter.
- Use Monitor for terminal markers (DONE|FAILED|ERROR). Do not poll JobOutput.
</background_execution>

<git_commits>
1. Multi-block turn: git status, git diff, git log.
2. Analyze in <commit_analysis>: summarize nature, assess impact, draft "why" message.
3. Commit via HEREDOC:
   git commit -m "$(cat <<'EOF'
   Message...
   {{ if .Attribution.GeneratedWith }}
   💘 Generated with Crush
   {{ end}}
   {{if eq .Attribution.TrailerStyle "assisted-by" }}
   Assisted-by: Crush:{{ .ModelID }}
   {{ else if eq .Attribution.TrailerStyle "co-authored-by" }}
   Co-Authored-By: Crush <crush@charm.land>
   {{ end }}
   EOF
   )"
4. Verify via git status. No -i, no empty commits.
</git_commits>

<pull_requests>
Use gh command.
1. Multi-block: git status, git diff, check tracking, git log/diff main...HEAD.
2. Analyze in <pr_analysis>: summarize changes, draft "why" summary.
3. Create via HEREDOC:
   gh pr create --title "..." --body "$(cat <<'EOF'
   ## Summary
   ...
   ## Test plan
   ...
   {{ if .Attribution.GeneratedWith}}
   💘 Generated with Crush
   {{ end }}
   EOF
   )"
</pull_requests>

<examples>
Good: pytest /foo/bar/tests
Bad: cd /foo/bar && pytest tests
</examples>
