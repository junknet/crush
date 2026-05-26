#!/usr/bin/env bash
# relay_mobile_joint.sh — 真实联调: crush-test TUI + local NATS relay + mobile client
# 需要 WaitAI、adb、jq、go; mobile app 需已安装到设备。

source "$(dirname "$0")/../common.sh"

CRUSH_TEST_BIN="${CRUSH_TEST_BIN:-$HOME/.local/bin/crush-test}"
CRUSH_BIN="$CRUSH_TEST_BIN"

need_tui
need_waitai
command -v adb >/dev/null || skip "adb not installed"
command -v go >/dev/null || skip "go not installed"
command -v curl >/dev/null || skip "curl not installed"
command -v jq >/dev/null || skip "jq not installed"

NATS_TOKEN="${CRUSH_RELAY_TOKEN:-ymm_rpc_2026}"
NATS_PORT="${CRUSH_RELAY_PORT:-4222}"
NATS_HTTP_PORT="${CRUSH_RELAY_HTTP_PORT:-8222}"
NATS_WS_PORT="${CRUSH_RELAY_WS_PORT:-9091}"
NATS_HOST="${CRUSH_RELAY_HOST:-127.0.0.1}"
NATS_URL="${CRUSH_RELAY_NATS_URL:-ws://${NATS_HOST}:${NATS_WS_PORT}}"
WAITAI_BASE="${WAITAI_CRUSH_BASE:-${WAITAI_BASE:-http://127.0.0.1:43917}}"
WAITAI_KEY="${WAITAI_API_KEY:-${NCODER_WAITAI_KEY:-}}"
WECODE_KEY="${WECODE_API_KEY:-}"
if [[ -z "$WECODE_KEY" && -f "$HOME/.codex/auth.json" ]]; then
  WECODE_KEY="$(jq -r '.OPENAI_API_KEY // empty' "$HOME/.codex/auth.json" 2>/dev/null || true)"
fi
CRUSH_BINARY="$ART/crush-test.bin"
CRUSH_RUNNER="$ART/run_crush_joint.sh"
NATS_CONFIG="$(mktemp "$ART/nats.XXXXXX.conf")"
NATS_LOG="$ART/nats.log"
NATS_STORE_DIR="$ART/jetstream"
mkdir -p "$NATS_STORE_DIR"

cleanup() {
  if [[ -n "${NATS_PID:-}" ]]; then
    kill "$NATS_PID" 2>/dev/null || true
    wait "$NATS_PID" 2>/dev/null || true
  fi
  "$TUI" kill "$SESS" 2>/dev/null || true
  "$TUI" kill "${SESS}-2" 2>/dev/null || true
}
trap cleanup EXIT

log "building latest crush binary once"
( cd "$REPO" && env CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -o "$CRUSH_BINARY" main.go )
cat >"$CRUSH_RUNNER" <<EOF
#!/usr/bin/env bash
set -euo pipefail
export CRUSH_DISABLE_METRICS=1
export CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1
export CRUSH_DISABLE_DEFAULT_PROVIDERS=1
export WAITAI_CRUSH_BASE=$(printf '%q' "$WAITAI_BASE")
export WAITAI_API_KEY=$(printf '%q' "$WAITAI_KEY")
export NCODER_WAITAI_KEY=$(printf '%q' "$WAITAI_KEY")
export WECODE_API_KEY=$(printf '%q' "$WECODE_KEY")
export CRUSH_RELAY_NATS_URL=$(printf '%q' "$NATS_URL")
export CRUSH_RELAY_TOKEN=$(printf '%q' "$NATS_TOKEN")
cd $(printf '%q' "$REPO")
exec $(printf '%q' "$CRUSH_BINARY")
EOF
chmod +x "$CRUSH_RUNNER"

cat >"$NATS_CONFIG" <<EOF
port: $NATS_PORT
http: $NATS_HTTP_PORT
server_name: crush-joint

authorization {
  token: "$NATS_TOKEN"
}

jetstream {
  store_dir: "$NATS_STORE_DIR"
}

websocket {
  port: $NATS_WS_PORT
  no_tls: true
}
EOF

log "starting local NATS relay"
if ! command -v nats-server >/dev/null; then
  log "building cached nats-server"
  (cd /home/junknet/go/pkg/mod/github.com/nats-io/nats-server/v2@v2.10.24 && GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go build -o "$ART/nats-server")
  NATS_SERVER_BIN="$ART/nats-server"
else
  NATS_SERVER_BIN="nats-server"
fi
"$NATS_SERVER_BIN" -c "$NATS_CONFIG" >"$NATS_LOG" 2>&1 &
NATS_PID=$!

for _ in $(seq 1 120); do
  if curl -s "http://127.0.0.1:${NATS_HTTP_PORT}/varz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -s "http://127.0.0.1:${NATS_HTTP_PORT}/varz" >/dev/null 2>&1 \
  || fail "NATS monitor never became ready"

log "starting crush-test with relay"
"$TUI" start "$SESS" 160 45 -- \
  "$CRUSH_RUNNER" \
  | tee -a "$LOG"

log "starting second crush-test session"
"$TUI" start "${SESS}-2" 160 45 -- \
  "$CRUSH_RUNNER" \
  | tee -a "$LOG"

log "waiting for crush relay connections"
relay_name=""
for _ in $(seq 1 120); do
  relay_count="$(
    curl -s "http://127.0.0.1:${NATS_HTTP_PORT}/connz?subs=1" \
      | jq '[.connections[] | select(.name | startswith("crush-tui-"))] | length'
  )"
  if [[ "${relay_count:-0}" -ge 2 ]]; then
    break
  fi
  sleep 1
done
[[ "${relay_count:-0}" -ge 2 ]] || fail "TUI relay connections never reached 2"
relay_name="$(
  curl -s "http://127.0.0.1:${NATS_HTTP_PORT}/connz?subs=1" \
    | jq -r '.connections[] | select(.name | startswith("crush-tui-")) | .name' \
    | head -n 1
)"
relay_session_id="${relay_name#crush-tui-}"
log "relay session id: $relay_session_id"

log "waiting for session presence"
for _ in $(seq 1 120); do
  if curl -s "http://127.0.0.1:${NATS_HTTP_PORT}/jsz?streams=1" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if [[ -n "${CRUSH_MOBILE_ADB_SERIAL:-}" ]]; then
  device_serial="$CRUSH_MOBILE_ADB_SERIAL"
else
  device_serial="$(
    adb devices | awk 'NR > 1 && $2 == "device" { print $1; exit }'
  )"
fi
if [[ -z "$device_serial" ]]; then
  skip "no adb device found"
fi

mobile_server_url="${CRUSH_MOBILE_SERVER_URL:-}"
if [[ -z "$mobile_server_url" ]]; then
  qemu_flag="$(adb -s "$device_serial" shell getprop ro.kernel.qemu 2>/dev/null | tr -d '\r')"
  if [[ "$qemu_flag" == "1" ]]; then
    mobile_server_url="ws://10.0.2.2:${NATS_WS_PORT}"
  else
    skip "set CRUSH_MOBILE_SERVER_URL for non-emulator device"
  fi
fi

deep_link="crushmobile://connect?serverUrl=$(printf '%s' "$mobile_server_url" | jq -sRr @uri)"
log "opening mobile deep link: $deep_link"
adb -s "$device_serial" shell am force-stop com.junknet.crushmobile >/dev/null 2>&1 || true
adb -s "$device_serial" shell am start -W -a android.intent.action.VIEW -d "$deep_link" >/dev/null

log "waiting for mobile NATS connection"
for _ in $(seq 1 120); do
  mobile_name="$(
    curl -s "http://127.0.0.1:${NATS_HTTP_PORT}/connz?subs=1" \
      | jq -r '.connections[] | select(.name == "crush-mobile") | .name' \
      | head -n 1
  )"
  if [[ "$mobile_name" == "crush-mobile" ]]; then
    break
  fi
  sleep 1
done
[[ "$mobile_name" == "crush-mobile" ]] || fail "mobile client never connected"

if [[ "${CRUSH_JOINT_HOLD_AFTER_CONNECT:-0}" == "1" ]]; then
  sleep "${CRUSH_JOINT_HOLD_SECONDS:-20}"
fi

log "sending one prompt through TUI"
"$TUI" send "$SESS" "echo 联合测试"
"$TUI" key "$SESS" Enter
"$TUI" expect "$SESS" 'echo 联合测试|Task started|Sautéed for' 30 || fail "prompt was not accepted"

log "capturing relay state"
curl -s "http://127.0.0.1:${NATS_HTTP_PORT}/connz?subs=1" \
  | jq '[.connections[] | {name, subs: .subscriptions_list}]' >"$ART/connz.json"
assert_file_nonempty "$ART/connz.json"

log "graceful quit"
"$TUI" quit "$SESS"
sleep 1

pass
