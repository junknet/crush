#!/usr/bin/env bash
# smoke_landing.sh — crush 启动后 landing 页正确渲染(无 LLM)
# 不需要 WaitAI,仅验证 TUI shell + skills 加载 + 配置解析

source "$(dirname "$0")/../common.sh"
need_tui

log "starting crush in tmux"
"$TUI" start "$SESS" 160 45 -- \
  "cd $REPO && WAITAI_API_KEY=\"${WAITAI_API_KEY:-}\" NCODER_WAITAI_KEY=\"${NCODER_WAITAI_KEY:-}\" CRUSH_GLOBAL_CONFIG=$CRUSH_GLOBAL_CONFIG CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 $CRUSH_BIN --data-dir $ART/data --trace-file $TRACE" \
  | tee -a "$LOG"

log "waiting for landing page"
"$TUI" expect "$SESS" 'Skills' 15 || fail "landing page Skills column not shown"

log "asserting landing components"
"$TUI" assert "$SESS" 'Claude|Gemini|GPT'    || fail "Model line missing"
"$TUI" assert "$SESS" 'LSPs'               || fail "LSPs column missing"
"$TUI" assert "$SESS" 'MCPs'               || fail "MCPs column missing"
"$TUI" assert "$SESS" 'Skills'             || fail "Skills column missing"
"$TUI" refute "$SESS" 'panic|fatal|segfault' || fail "panic/fatal detected"

log "capturing visual baseline"
"$TUI" png "$SESS" "$ART/landing.png" >>"$LOG" 2>&1 || fail "png capture failed"
assert_file_nonempty "$ART/landing.png"

log "graceful quit"
"$TUI" quit "$SESS"
sleep 1

# trace file should exist but be empty(没发任何 prompt,coordinator 也没建 root task)
[[ -f "$TRACE" ]] && log "trace file present: $(wc -c < "$TRACE") bytes" \
  || log "trace file not written (acceptable — no agent activity)"

pass
