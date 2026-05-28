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
4. Auto-Background: Commands exceeding 1 minute (default, configurable via `auto_background_after`) automatically move to background and return shell ID
5. Output Processing: Truncate if exceeds {{ .MaxOutputLength }} characters
6. Return Result: Include errors, metadata with <cwd></cwd> tags
</execution_steps>

<usage_notes>
- Command required, working_dir optional (defaults to current directory)
- IMPORTANT: Use `rg`/`agent` tools instead of `grep`/`find` commands. Use `view`/`ls` tools instead of `cat`/`head`/`tail`/`ls`.
- **NEVER use `grep`, `rg`, or `find` under `bash` for repository search**. Use the `rg` tool for content and filenames/paths, and `ast_grep` for structural code search.
- **NEVER use foreground sleep polling**: commands like `sleep 10 && status-check`, `sleep 20; job-output`, or long standalone `sleep` are blocked. Use `run_in_background=true` and `monitor`, or `schedule_wakeup` for a pure time delay.
- Chain with ';' or '&&', avoid newlines except in quoted strings
- Each command runs in independent shell (no state persistence between calls)
- Prefer absolute paths over 'cd' (use 'cd' only if user explicitly requests)
</usage_notes>

<background_execution>
- Set run_in_background=true for long-running processes (servers, watchers, polling).
- Returns a shell ID. Use job_output (with wait=true to block) or monitor (regex).
- NEVER use `&` in command; use the tool parameter.
- Use monitor for terminal markers (DONE|FAILED|ERROR). Do not poll job_output.
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
