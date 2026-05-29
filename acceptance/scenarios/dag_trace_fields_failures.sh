#!/usr/bin/env bash
# dag_trace_fields_failures.sh
# ──────────────────────────────
# 真路径,验证 sub-agent 失败时 trace 链路完整:
#   1. brain 把一个一定会失败的命令 delegate 给 worker
#   2. worker 触发 bash command_failed
#   3. propagateSubAgentTraces 应把 worker 的 command_failed
#      + task_finished(success=false) 拷回 parent runtime
#   4. 主 trace.jsonl 必须能看见这些失败事件,而不是 sub-agent
#      runtime 自己藏起来
#
# 这条用例补 dag_trace_fields.sh 的盲区——
# 那条只跑 happy path,没有任何 task_failed / command_failed,
# 一旦失败路径的传播逻辑回归,我们不会知道。

source "$(dirname "$0")/../common.sh"
need_tui
need_mock_llm

PROMPT="${PROMPT:-Please run a check. Use the 'agent' tool to delegate a task to an explore agent (role='explore'). The explore agent MUST use the bash tool to run ONLY the command \`exit 2\` to simulate a failure. The explore agent must NOT run any other command. After the explore agent reports back that the command failed with exit code 2, reply with 'task failed as expected' in one short sentence.}"

log "starting crush against Mock"
"$TUI" start "$SESS" 160 45 -- \
  "cd $REPO && CRUSH_MOCK_API_KEY=\"${CRUSH_MOCK_API_KEY:-}\" CRUSH_MOCK_KEY=\"${CRUSH_MOCK_KEY:-}\" CRUSH_GLOBAL_CONFIG=$CRUSH_GLOBAL_CONFIG CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 $CRUSH_BIN --data-dir $ART/data --trace-file $TRACE" \
  | tee -a "$LOG"

log "waiting for landing"
"$TUI" expect "$SESS" 'Skills' 15

log "sending failure-path prompt"
"$TUI" send "$SESS" "$PROMPT"
"$TUI" key  "$SESS" Enter

log "waiting for delegation flow"
"$TUI" expect "$SESS" 'Task started|Plan|agent' 30

log "waiting for completion"
last_snapshot=""
stable_count=0
for i in $(seq 1 120); do
  cur=$("$TUI" text "$SESS" 2>/dev/null | tail -25)
  if [[ -n "$cur" && "$cur" == "$last_snapshot" ]]; then
    stable_count=$((stable_count+1))
  else
    stable_count=0
  fi
  if (( stable_count >= 3 )) && echo "$cur" | tail -5 | grep -q 'Ready'; then
    log "  completion + 屏幕稳定 at t+${i}s"
    break
  fi
  last_snapshot="$cur"
  sleep 1
done

log "capturing post-prompt visual"
"$TUI" png "$SESS" "$ART/post_prompt.png" >>"$LOG" 2>&1
assert_file_nonempty "$ART/post_prompt.png"

log "graceful quit so trace flushes"
"$TUI" quit "$SESS"

for _ in 1 2 3 4 5 6 7 8 9 10; do
  [[ -s "$TRACE" ]] && break
  sleep 1
done
assert_file_nonempty "$TRACE"

log "--- trace summary ---"
"$TUI" trace_dump "$TRACE" '{seq:.sequence, depth, kind, status, success, profile, dur:.duration_ms}' 2>/dev/null \
  | tee -a "$LOG" | head -30

# ── 断言 1: 失败事件确实存在 ────────────────────────────────────
# 至少一次 bash command_failed (探针 ls 不存在的文件)
trace_count_ge '.kind == "command_failed"' 1

# 失败属于 sub-agent (explore_agent 在跑 ls 检查时触发的)
trace_has '.kind == "command_failed" and .profile == "explore_agent"'

# ── 断言 2: sub-agent 的失败 trace 必须出现在 parent trace.jsonl ─
# 如果 sub-agent runtime 是独立的、propagateSubAgentTraces 不工作,
# parent 文件里不会有 profile=explore_agent 的 entry。这就是 §1.11
# trace 传播的铁证。
trace_has '.profile == "explore_agent"'

# ── 断言 3: brain 自己 task_finished.success=true ───────────────
# 失败语义在 explore 内部消化,brain 收到「file absent」回复后正常
# 完成,不应 propagate 成 brain 整体失败。
trace_has '.kind == "task_finished" and .profile == "brain_agent" and .success == true'

# ── 断言 4: 整体 DAG 结构(root + 至少 1 个 child) ────────────────
trace_count_ge '.kind == "task_planned"'  2
trace_count_ge '.kind == "task_started"'  2
trace_count_ge '.kind == "task_finished"' 2

# ── 断言 5: 失败路径的 trace 字段也要填齐 ───────────────────────
# explore 的 task_finished 必须仍带 model_id + provider_id —
# preBindTaskTreeModels 在失败路径上同样工作。
trace_has '.kind == "task_finished" and .profile == "explore_agent" and (.model_id // "") != ""'
trace_has '.kind == "task_finished" and .profile == "explore_agent" and (.provider_id // "") != ""'

# command_failed 必须有 error / exit_code
trace_has '.kind == "command_failed" and ((.error // "") != "" or (.exit_code // 0) != 0)'

pass
