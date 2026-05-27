#!/usr/bin/env bash
# dag_run_tui.sh — real TUI regression for dag_run/run/tool-parallel wiring.

source "$(dirname "$0")/../common.sh"
need_tui
need_waitai

FINAL_MARKER="TUI_DAG_DONE_7391"
PROMPT="${PROMPT:-Your first tool call must be dag_run. Do not use bash, rg, or view directly. Use one dag_run call with max_parallel=3 and these nodes: (1) rg node id files pattern dag_run path internal/agent/tools files_only=true, (2) rg node id symbol pattern DagRunToolName path internal/agent/tools/dag_run.go literal_text=true, (3) view node id docs file_path internal/agent/tools/dag_run.md limit 80, (4) run node id hold language python script that imports time, sleeps for 4 seconds, then prints status-visible. After the tool result, reply exactly with the concatenation of these seven fragments and then mention the completed node count: TUI, _, DAG, _, DONE, _, 7391.}"

log "starting crush against WaitAI"
"$TUI" start "$SESS" 160 45 -- \
  "cd $REPO && WAITAI_API_KEY=\"${WAITAI_API_KEY:-}\" NCODER_WAITAI_KEY=\"${NCODER_WAITAI_KEY:-}\" CRUSH_GLOBAL_CONFIG=$CRUSH_GLOBAL_CONFIG CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 $CRUSH_BIN --data-dir $ART/data --trace-file $TRACE" \
  | tee -a "$LOG"

log "waiting for landing"
"$TUI" expect "$SESS" 'Skills' 15

log "sending prompt"
"$TUI" send "$SESS" "$PROMPT"
"$TUI" key  "$SESS" Enter

log "waiting for visible tool activity"
status_seen=0
for _ in $(seq 1 80); do
  screen=$("$TUI" text "$SESS" 2>/dev/null || true)
  if echo "$screen" | grep -E -q 'tools [1-9][0-9]* running|tool-parallel [2-9][0-9]*|active (rg|view|run|dag_run)'; then
    printf '%s\n' "$screen" > "$ART/tool_activity.txt"
    "$TUI" png "$SESS" "$ART/tool_activity.png" >>"$LOG" 2>&1 || fail "tool activity png capture failed"
    status_seen=1
    break
  fi
  sleep 0.5
done
(( status_seen == 1 )) || fail "dag_run/tool runtime activity never became visible in TUI"
assert_file_nonempty "$ART/tool_activity.txt"
assert_file_nonempty "$ART/tool_activity.png"
grep -E -q 'ctx [0-9]+% [0-9.]+[KM]?/[0-9.]+[KM]?' "$ART/tool_activity.txt" \
  || fail "tool activity does not show concrete context window usage"
! grep -q 'ctx --' "$ART/tool_activity.txt" \
  || fail "tool activity still shows unknown context window"
log "  ✓ dag_run/tool activity visible"

log "waiting for final answer"
"$TUI" expect "$SESS" "$FINAL_MARKER" 120 || {
  "$TUI" text "$SESS" >> "$LOG" 2>&1 || true
  fail "$FINAL_MARKER final answer not shown"
}
"$TUI" text "$SESS" > "$ART/post_prompt.txt"
assert_file_nonempty "$ART/post_prompt.txt"

log "capturing post-prompt visual"
"$TUI" png "$SESS" "$ART/post_prompt.png" >>"$LOG" 2>&1 || fail "png capture failed"
assert_file_nonempty "$ART/post_prompt.png"

log "graceful quit so trace flushes"
"$TUI" quit "$SESS"

for _ in 1 2 3 4 5 6 7 8 9 10; do
  [[ -s "$TRACE" ]] && break
  sleep 1
done
assert_file_nonempty "$TRACE"

log "--- dag_run trace summary ---"
"$TUI" trace_dump "$TRACE" 'select((.tool_name // "") == "dag_run" or (.tool_name // "") == "run") | {seq:.sequence, kind, status, success, tool:.tool_name, dur:.duration_ms, err:.error}' 2>/dev/null \
  | tee -a "$LOG"

trace_has '.kind == "tool_started" and .tool_name == "dag_run"'
trace_has '.kind == "tool_finished" and .tool_name == "dag_run" and .success == true'
trace_has '.kind == "task_finished" and .profile == "brain_agent"'
trace_count_eq '.kind == "task_failed"' 0

if ! grep -q "$FINAL_MARKER" "$ART/post_prompt.txt"; then
  "$TUI" text "$SESS" >> "$LOG" 2>&1 || true
  fail "visual output does not contain $FINAL_MARKER"
fi
grep -E -q 'ctx [0-9]+% [0-9.]+[KM]?/[0-9.]+[KM]?' "$ART/post_prompt.txt" \
  || fail "post prompt output does not show concrete context window usage"
! grep -q 'ctx --' "$ART/post_prompt.txt" \
  || fail "post prompt output still shows unknown context window"

pass
