#!/usr/bin/env bash
# dag_trace_fields.sh — 真路径:发 prompt → 3-profile DAG 完整跑 → trace 字段填齐
# 需要 Mock 后端 + API key

source "$(dirname "$0")/../common.sh"
need_tui
need_mock_llm

PROMPT="${PROMPT:-Please use the 'agent' tool to delegate all parts of this task. You must not run bash commands directly in the brain agent. First, delegate to an explore agent (role='explore') to check if the file 'test_done.txt' exists in the repository. Second, delegate to a worker agent (role='worker') to write 'done' into a new file named 'test_done.txt' in the repository root. Third, delegate to an explore agent (role='explore') to view 'test_done.txt' and verify it contains 'done'. Fourth, delegate to a worker agent (role='worker') to delete the file 'test_done.txt'. You must use the agent tool for each step.}"

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

log "waiting for completion(verify 步骤的 ✓ 或最终 ◇ 完成不带秒数)"
# crush 完成 verify 步骤后,最后一个 ◇ 没有 "Ns" 后缀。给 240s 上限。
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

# ── 断言:DAG 结构(root + 3 children) ────────────────────────────
trace_count_ge '.kind == "task_planned"' 4
trace_count_ge '.kind == "task_started"' 4
trace_count_ge '.kind == "task_finished"' 4

# 3 个 profile 必须都跑了
trace_has '.profile == "brain_agent" and .kind == "task_finished"'
trace_has '.profile == "worker_agent" and .kind == "task_finished"'
trace_has '.profile == "explore_agent" and .kind == "task_finished"'

# ── 断言:trace 字段填齐(task_finished depth=1) ─────────────────
# duration_ms > 0
trace_has '.kind == "task_finished" and .depth == 1 and .duration_ms > 0'

# model_id 不为空
trace_has '.kind == "task_finished" and .depth == 1 and (.model_id // "") != ""'

# provider_id 不为空
trace_has '.kind == "task_finished" and .depth == 1 and (.provider_id // "") != ""'

# input_tokens 或 output_tokens > 0(至少有一个 LLM 真的调过)
trace_has '.kind == "task_finished" and ((.input_tokens // 0) > 0 or (.output_tokens // 0) > 0)'

# task_started 也要带 model_id(本次 fix 的新行为)
trace_has '.kind == "task_started" and .depth == 1 and (.model_id // "") != ""' \
  || log "  ⚠ task_started.model_id 未填 — 可能需要重新 build crush"

# ── 断言:无失败 ──────────────────────────────────────────────────
if ! "$TUI" trace_count "$TRACE" '.kind == "task_failed"' | grep -q '^0$'; then
  log "  ✗ task_failed 事件存在"
  "$TUI" trace_dump "$TRACE" 'select(.kind == "task_failed") | {goal, error}' >>"$LOG"
  fail "task_failed events detected"
fi

pass
