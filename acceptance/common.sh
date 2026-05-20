#!/usr/bin/env bash
# acceptance/common.sh — 共享 bootstrap,每个 scenario source 它
#
# 提供:
#   $TUI               tui-test skill 入口
#   $CRUSH_BIN         crush binary 绝对路径
#   $REPO              crush fork 仓库根
#   $ART               本次 scenario 的 artifacts 目录(已 mkdir)
#   $TRACE             本次 scenario 的 trace JSONL 路径
#   $NIMLSP_BIN        nimlsp binary(若存在)
#   $NIM_CORE_PATH     nim-core 仓库根(若存在,用作真 Nim 项目 fixture)
#   assert / refute / log / fail / pass 辅助函数
#   need_waitai / need_nimlsp / need_tui 前置环境检查(失败 SKIP)

set -uo pipefail

# ── 路径解析 ─────────────────────────────────────────────────────────
COMMON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$COMMON_DIR/.." && pwd)"
SCENARIO_NAME="${SCENARIO_NAME:-$(basename "${0%.sh}")}"
ART_ROOT="${ART_ROOT:-$REPO/acceptance/artifacts}"
ART="$ART_ROOT/$SCENARIO_NAME"
mkdir -p "$ART"

TUI="${TUI:-$HOME/.claude/skills/tui-test/scripts/tui.sh}"
CRUSH_BIN="${CRUSH_BIN:-$REPO/crush}"
TRACE="$ART/trace.jsonl"
LOG="$ART/run.log"

NIMLSP_BIN="${NIMLSP_BIN:-/home/junknet/linege/nim-src/langserver/nimlangserver}"
NIM_CORE_PATH="${NIM_CORE_PATH:-/home/junknet/linege/nim-core}"

# ── 输出 ─────────────────────────────────────────────────────────────
log()  { echo "[$SCENARIO_NAME] $*" | tee -a "$LOG"; }
fail() { echo "[$SCENARIO_NAME] ✗ FAIL: $*" | tee -a "$LOG" >&2; exit 1; }
pass() { echo "[$SCENARIO_NAME] ✓ PASS" | tee -a "$LOG"; exit 0; }
skip() { echo "[$SCENARIO_NAME] ⊘ SKIP: $*" | tee -a "$LOG"; exit 77; }

assert()    { eval "$1" || fail "assert failed: $1"; log "  ✓ $1"; }
assert_eq() { [[ "$1" == "$2" ]] || fail "expected '$2' got '$1' ($3)"; log "  ✓ $3 = '$1'"; }
assert_file_nonempty() { [[ -s "$1" ]] || fail "$1 empty/missing"; log "  ✓ $1 non-empty ($(wc -c < "$1") bytes)"; }

# trace 断言(包装 tui-test skill 的 trace_has / trace_count)
trace_has()   { "$TUI" trace_has "$TRACE" "$1" >>"$LOG" 2>&1 || fail "trace_has '$1'"; log "  ✓ trace_has $1"; }
trace_count() {
  local n; n=$("$TUI" trace_count "$TRACE" "$1" 2>/dev/null)
  echo "$n"
}
trace_count_eq() {
  local n; n=$("$TUI" trace_count "$TRACE" "$1" 2>/dev/null)
  assert_eq "$n" "$2" "trace_count $1"
}
trace_count_ge() {
  local n; n=$("$TUI" trace_count "$TRACE" "$1" 2>/dev/null)
  (( n >= $2 )) || fail "trace_count $1 = $n, expected ≥ $2"
  log "  ✓ trace_count $1 ≥ $2 (got $n)"
}

# ── 前置 ─────────────────────────────────────────────────────────────
need_tui() {
  command -v tmux >/dev/null   || skip "tmux not installed"
  command -v jq >/dev/null     || skip "jq not installed"
  [[ -x "$TUI" ]]               || skip "tui-test skill not installed at $TUI"
  [[ -x "$CRUSH_BIN" ]]         || skip "crush binary missing: $CRUSH_BIN (run: go build -o crush . in $REPO)"
}

need_waitai() {
  local base="${WAITAI_CRUSH_BASE:-${WAITAI_BASE:-http://127.0.0.1:43917}}"
  [[ -n "${WAITAI_API_KEY:-${NCODER_WAITAI_KEY:-}}" ]] \
    || skip "WAITAI_API_KEY / NCODER_WAITAI_KEY not set"
  curl -s -m 2 -o /dev/null -w '%{http_code}' "$base" 2>/dev/null | grep -q '^[23]' \
    || skip "WaitAI backend $base unreachable"
}

need_nimlsp() {
  [[ -x "$NIMLSP_BIN" ]] || skip "nimlsp binary missing: $NIMLSP_BIN"
  [[ -d "$NIM_CORE_PATH" ]] || skip "nim-core project missing: $NIM_CORE_PATH"
}

# 关掉孤儿 session
trap '"$TUI" kill "${SESS:-acceptance-$SCENARIO_NAME}" 2>/dev/null || true' EXIT
SESS="acceptance-$SCENARIO_NAME"
