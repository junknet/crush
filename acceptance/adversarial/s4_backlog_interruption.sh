#!/usr/bin/env bash
# s4_backlog_interruption.sh — adversarial S4: backlog + mid-turn interruption.
#
# Drives the live crush-dev TUI via tmux. Submits a slow task, queues 2-3 more
# prompts while it runs, sends ESC to interrupt, then submits a CONTRADICTORY
# final directive. Ground truth: the LAST directive wins (1+1=2) and the
# abandoned/queued work does NOT silently resume after the cancel.

set -uo pipefail

TUI="$HOME/.claude/skills/tui-test/scripts/tui.sh"
FIX="${FIX:-$HOME/.cache/crush-adversarial-fixture}"
ART="${ART:-$HOME/.cache/crush-adversarial-artifacts/s4}"; mkdir -p "$ART"
TRACE="$ART/trace.jsonl"
LOG="$ART/run.log"
SESS="adv-s4"
: > "$LOG"

log() { echo "[s4] $*" | tee -a "$LOG"; }
snap() { "$TUI" text "$SESS" | tee "$ART/$1.txt" >/dev/null; }

cleanup() { "$TUI" kill "$SESS" 2>/dev/null || true; }
trap cleanup EXIT

log "launch crush-dev in tmux (real config), fixture=$FIX, trace=$TRACE"
"$TUI" start "$SESS" 200 50 -- \
  "cd $FIX && crush-dev --trace-file $TRACE" 2>&1 | tee -a "$LOG"

"$TUI" expect "$SESS" 'Ready|Ask|crush|esc|Esc' 180 || { log "TUI never became ready"; snap fail_ready; exit 1; }
sleep 4
snap 00_ready

log "submit prompt 1 (slow seq sleep task)"
"$TUI" send "$SESS" '用 bash 跑 seq 1 30，每个数字之间 sleep 1，逐个打印出来'
sleep 1
"$TUI" key "$SESS" Enter
sleep 8
snap 01_after_p1

log "queue prompt 2 while busy"
"$TUI" send "$SESS" '另外，请把当前目录下所有 .go 文件名列出来'
sleep 1
"$TUI" key "$SESS" Enter
sleep 2
log "queue prompt 3 while busy"
"$TUI" send "$SESS" '再统计一下 internal/agent 目录有几个文件'
sleep 1
"$TUI" key "$SESS" Enter
sleep 2
snap 02_after_queue

log "send ESC to interrupt"
"$TUI" key "$SESS" Escape
sleep 4
snap 03_after_esc

log "submit contradictory final directive"
"$TUI" send "$SESS" '停，忽略前面所有任务，只回答：1+1 等于几？直接给数字。'
sleep 1
"$TUI" key "$SESS" Enter

"$TUI" expect "$SESS" '(^| )2( |$)|等于 ?2|= ?2|是 ?2' 90 || log "WARN: did not observe explicit 2 within 90s"
sleep 5
snap 04_final

log "final screen captured; quitting"
"$TUI" text "$SESS" -300 > "$ART/full_pane.txt"
"$TUI" quit "$SESS" 2>&1 | tee -a "$LOG"
sleep 3

log "trace bytes: $(wc -c < "$TRACE" 2>/dev/null || echo 0)"
log "done"
