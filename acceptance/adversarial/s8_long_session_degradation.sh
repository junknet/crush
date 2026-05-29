#!/usr/bin/env bash
# s8_long_session_degradation.sh — adversarial S8: long-session degradation.
#
# ONE session. Turn ~2 asks a precise question (backend.go line 45). Then ~18
# context-bloating turns (read large files, run commands). At the END, repeat
# the IDENTICAL precise question. Ground truth: backend.go:45 == "type Backend
# interface {". Compare early vs late correctness; capture context_bytes curve,
# auto_summarize firing, token trend, runaway-context signal.

set -uo pipefail

TUI="$HOME/.claude/skills/tui-test/scripts/tui.sh"
FIX="${FIX:-$HOME/.cache/crush-adversarial-fixture}"
ART="${ART:-$HOME/.cache/crush-adversarial-artifacts/s8}"; mkdir -p "$ART"
TRACE="$ART/trace.jsonl"
LOG="$ART/run.log"
SESS="adv-s8"
: > "$LOG"

log() { echo "[s8] $*" | tee -a "$LOG"; }
snap() { "$TUI" text "$SESS" | tee "$ART/$1.txt" >/dev/null; }

# Submit a prompt and wait until the model finishes this turn (back to Ready /
# dag done) before proceeding — keeps turns sequential within one session.
turn() {
  local label="$1" text="$2" wait_s="${3:-70}"
  log "TURN $label: $text"
  "$TUI" send "$SESS" "$text"
  sleep 1
  "$TUI" key "$SESS" Enter
  sleep 3
  local t=0
  while (( t < wait_s )); do
    local pane; pane="$("$TUI" text "$SESS" 2>/dev/null)"
    if echo "$pane" | grep -Eq 'dag [0-9]+ done|Ready\?|Ready!'; then
      if ! echo "$pane" | grep -Eq 'model running|Waiting for model|running/'; then
        break
      fi
    fi
    sleep 2; t=$((t+2))
  done
  snap "turn_${label}"
}

cleanup() { "$TUI" kill "$SESS" 2>/dev/null || true; }
trap cleanup EXIT

log "launch crush-dev in tmux (real config), fixture=$FIX, trace=$TRACE"
"$TUI" start "$SESS" 200 50 -- "cd $FIX && crush-dev --trace-file $TRACE" 2>&1 | tee -a "$LOG"
"$TUI" expect "$SESS" 'Ready|Ask|esc|Esc' 180 || { log "TUI never ready"; snap fail_ready; exit 1; }
sleep 4
snap 00_ready

# Turn 1: warm-up
turn 01 '你好，这是一个 Go 项目。请简短确认你能看到当前目录。' 50

# Turn 2: PRECISE question (early baseline)
turn 02_EARLY 'view 文件 internal/iodriver/backend.go 的第 45 行，原样告诉我那一行的内容。' 70

# Turns 3-20: context bloat — read large files + run commands
turn 03 '读取 internal/ui/model/ui.go 整个文件并简述它的职责。' 90
turn 04 '读取 internal/agent/coordinator.go 整个文件，列出其中定义的所有函数名。' 90
turn 05 '读取 internal/agent/agent_tool.go 整个文件，解释 ParallelAgentTool 的用途。' 90
turn 06 '运行 bash: find . -name "*.go" | head -50，把结果贴出来。' 60
turn 07 '运行 bash: wc -l internal/**/*.go 2>/dev/null || wc -l $(find . -name "*.go")，统计行数。' 60
turn 08 '读取 internal/agent/event.go 整个文件并总结事件类型。' 80
turn 09 '运行 bash: cat internal/ui/model/ui.go | head -200，展示前 200 行。' 70
turn 10 '运行 bash: grep -rn "func " internal/agent/ | head -80，列出函数定义。' 60
turn 11 '再读一遍 internal/agent/coordinator.go，这次列出所有 import。' 80
turn 12 '运行 bash: cat internal/agent/coordinator.go | tail -300，展示尾部。' 70
turn 13 '运行 bash: ls -la internal/iodriver/ 并解释每个文件。' 60
turn 14 '读取 internal/iodriver/remote.go 整个文件并总结。' 70
turn 15 '读取 internal/iodriver/serve.go 整个文件并总结。' 70
turn 16 '运行 bash: cat internal/ui/model/ui.go | tail -400，展示尾部。' 70
turn 17 '运行 bash: grep -rn "context" internal/agent/ | wc -l，给个数字。' 60
turn 18 '读取 internal/iodriver/ssh.go 整个文件并总结。' 70
turn 19 '运行 bash: for f in $(find . -name "*.go"); do echo "== $f =="; head -30 "$f"; done | head -300' 80
turn 20 '总结一下到目前为止你读过的所有文件的整体架构。' 90

# Final turn: IDENTICAL precise question (late)
turn 21_LATE 'view 文件 internal/iodriver/backend.go 的第 45 行，原样告诉我那一行的内容。' 80

log "final captured; full pane dump"
"$TUI" text "$SESS" -400 > "$ART/full_pane.txt"
"$TUI" quit "$SESS" 2>&1 | tee -a "$LOG"
sleep 3
log "trace bytes: $(wc -c < "$TRACE" 2>/dev/null || echo 0)"
log "done"
