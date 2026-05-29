#!/usr/bin/env bash
# dag_trace_optimized.sh — Optimized parallel & consolidated execution test.
# Runs the consolidated prompt and asserts that delegation happens with
# fewer sequential turns (fewer task dispatches).

source "$(dirname "$0")/../common.sh"
need_tui
need_mock_llm

PROMPT="${PROMPT:-Please use the 'agent' tool to delegate all parts of this task. First, delegate to an explore agent to check if the file 'test_done.txt' exists in the repository. Second, delegate to a worker agent to write 'done' into a new file named 'test_done.txt' in the repository root, verify it contains 'done' and then delete the file. You must NOT use sequential explore-then-worker roundtrips for writing, verifying, and deleting; consolidate those actions into a single worker task.}"

log "starting crush against Mock"
"$TUI" start "$SESS" 160 45 -- \
  "cd $REPO && CRUSH_MOCK_API_KEY=\"${CRUSH_MOCK_API_KEY:-}\" CRUSH_MOCK_KEY=\"${CRUSH_MOCK_KEY:-}\" CRUSH_GLOBAL_CONFIG=$CRUSH_GLOBAL_CONFIG CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 $CRUSH_BIN --data-dir $ART/data --trace-file $TRACE" \
  | tee -a "$LOG"

log "waiting for landing"
"$TUI" expect "$SESS" 'Skills' 15

log "sending prompt: $PROMPT"
"$TUI" send "$SESS" "$PROMPT"
"$TUI" key  "$SESS" Enter

log "waiting for brain_agent / worker_agent / explore_agent flow"
"$TUI" expect "$SESS" 'Task started|Plan' 30

log "waiting for completion"
last_snapshot=""
stable_count=0
for i in $(seq 1 240); do
  cur=$("$TUI" text "$SESS" 2>/dev/null | tail -25)
  if [[ -n "$cur" && "$cur" == "$last_snapshot" ]]; then
    stable_count=$((stable_count+1))
  else
    stable_count=0
  fi
  if (( stable_count >= 3 )) && echo "$cur" | tail -5 | grep -q 'Ready'; then
    log "  completion marker seen + 屏幕稳定 at t+${i}s"
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

# 等 trace 落盘
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [[ -s "$TRACE" ]] && break
  sleep 1
done
assert_file_nonempty "$TRACE"

log "--- trace summary ---"
"$TUI" trace_dump "$TRACE" '{seq:.sequence, depth, kind, status, success, profile, dur:.duration_ms, model:.model_id, ti:.input_tokens, to:.output_tokens, cost:.estimated_cost_usd}' 2>/dev/null \
  | tee -a "$LOG" | head -20

# ── 断言:DAG 结构优化 ──────────────────────────────────────────
# The old sequential flow had 1 root task + 4 sub-agent dispatches = 5 planned tasks total (depth=1).
# The optimized flow consolidates steps 2, 3, 4 into a single worker dispatch:
# 1 explore sub-agent (check existence) + 1 worker sub-agent (consolidated task) = 2 planned sub-agent tasks total!
# We assert that the number of planned sub-agent tasks is <= 2.
sub_agent_count=$(trace_count '.kind == "task_planned" and .depth == 1')
log "  Number of planned sub-agent tasks: $sub_agent_count"
if (( sub_agent_count > 2 )); then
  fail "Sequential delegation chain detected! Sub-agent count: $sub_agent_count (expected <= 2 due to consolidation)"
fi

# Assert that both explore_agent and worker_agent ran successfully
trace_has '.profile == "brain_agent" and .kind == "task_finished"'
trace_has '.profile == "worker_agent" and .kind == "task_finished"'
trace_has '.profile == "explore_agent" and .kind == "task_finished"'

# ── 断言:无失败 ──────────────────────────────────────────────────
if ! "$TUI" trace_count "$TRACE" '.kind == "task_failed"' | grep -q '^0$'; then
  log "  ✗ task_failed 事件存在"
  "$TUI" trace_dump "$TRACE" 'select(.kind == "task_failed") | {goal, error}' >>"$LOG"
  fail "task_failed events detected"
fi

pass
