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
#   need_mock_llm / need_nimlsp / need_tui 前置环境检查(失败 SKIP)

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

need_mock_llm() {
  local base="${CRUSH_MOCK_LLM_BASE:-${MOCK_LLM_BASE:-http://127.0.0.1:43917}}"
  local key="${MOCK_LLM_API_KEY:-${CRUSH_MOCK_API_KEY:-${CRUSH_MOCK_KEY:-}}}"
  [[ -n "$key" ]] \
    || skip "MOCK_LLM_API_KEY / CRUSH_MOCK_API_KEY / CRUSH_MOCK_KEY not set"
  curl -s -m 2 -o /dev/null -w '%{http_code}' "$base" 2>/dev/null | grep -q '^[23]' \
    || skip "Mock LLM backend $base unreachable"

  local root="${base%/}"
  local completions="$root/v1/chat/completions"
  if [[ "$root" == */v1 ]]; then
    completions="$root/chat/completions"
  fi

  local code
  code="$(curl -s -m 5 -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer $key" \
    -H 'Content-Type: application/json' \
    -d '{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"ping"}],"max_tokens":1}' \
    "$completions" 2>/dev/null || true)"
  case "$code" in
    2*|3*) ;;
    401|403) skip "Mock LLM backend $base rejected the configured API key" ;;
    *) skip "Mock LLM backend $base chat probe failed with HTTP $code" ;;
  esac
}

need_nimlsp() {
  [[ -x "$NIMLSP_BIN" ]] || skip "nimlsp binary missing: $NIMLSP_BIN"
  [[ -d "$NIM_CORE_PATH" ]] || skip "nim-core project missing: $NIM_CORE_PATH"
}

SESS="acceptance-$SCENARIO_NAME"

# ── Global Test Configuration Isolation ─────────────────────────────
# We generate a temporary configuration directory for each test run to
# isolate it from the user's real ~/.config/crush/crush.yaml. This prevents
# rate limits, token conflicts, and cost on real providers.
# We map all model profiles to a mock provider.

TEST_CFG_DIR=""
if [[ -z "${CRUSH_GLOBAL_CONFIG:-}" ]]; then
  TEST_CFG_DIR="$(mktemp -d -t crush_test_cfg_XXXXXX)"
  export CRUSH_GLOBAL_CONFIG="$TEST_CFG_DIR"
  
  cat > "$TEST_CFG_DIR/crush.yaml" <<EOF
agents:
  explore:
    allowed_mcp: null
  auditor:
    allowed_mcp: null
models:
  brain:
    model: claude-opus-4-7
    provider: mock-anthropic
  explore:
    model: claude-haiku-4-5-20251001
    provider: mock-anthropic
  worker:
    model: claude-sonnet-4-6
    provider: mock-anthropic
  plan:
    model: claude-sonnet-4-6
    provider: mock-anthropic
  auditor:
    model: claude-sonnet-4-6
    provider: mock-anthropic
providers:
  mock-anthropic:
    api_key: \${MOCK_LLM_API_KEY:-\${CRUSH_MOCK_API_KEY:-\${CRUSH_MOCK_KEY:-test}}}
    base_url: \${CRUSH_MOCK_LLM_BASE:-http://127.0.0.1:43917/v1}
    models:
      - id: claude-opus-4-7
        name: Claude Opus 4.7
        context_window: 1000000
        default_max_tokens: 128000
        can_reason: true
      - id: claude-sonnet-4-6
        name: Claude Sonnet 4.6
        context_window: 1000000
        default_max_tokens: 64000
        can_reason: true
      - id: claude-haiku-4-5-20251001
        name: Claude Haiku 4.5
        context_window: 200000
        default_max_tokens: 64000
        can_reason: false
EOF
fi

cleanup_common() {
  "$TUI" kill "${SESS:-acceptance-$SCENARIO_NAME}" 2>/dev/null || true
  if [[ -n "${TEST_CFG_DIR:-}" && -d "$TEST_CFG_DIR" ]]; then
    rm -rf "$TEST_CFG_DIR"
  fi
}
trap cleanup_common EXIT
