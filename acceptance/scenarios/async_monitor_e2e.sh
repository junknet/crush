#!/usr/bin/env bash
# async_monitor_e2e.sh — end-to-end test for background jobs, monitor wakeups,
# OpenAI-compatible streaming tool calls, and trace duplicate suppression.

source "$(dirname "$0")/../common.sh"
need_tui

PY_SERVER="$ART/local_openai_server.py"
PORT_FILE="$ART/local_openai.port"
REQ_LOG="$ART/llm_requests.jsonl"
CFG_DIR="$(mktemp -d -t crush_async_monitor_cfg_XXXXXX)"
DATA_DIR="$(mktemp -d -t crush_async_monitor_data_XXXXXX)"
SERVER_PID=""

cleanup_async_monitor() {
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$CFG_DIR" "$DATA_DIR"
}
trap 'cleanup_async_monitor; cleanup_common' EXIT

cat > "$PY_SERVER" <<'PY'
#!/usr/bin/env python3
import http.server
import json
import re
import socket
import sys
import threading

port_file = sys.argv[1]
request_log = sys.argv[2]


def sse_chunk(payload):
    return "data: " + json.dumps(payload, separators=(",", ":")) + "\n\n"


def text_chunks(text):
    yield {
        "id": "chatcmpl-async-e2e",
        "object": "chat.completion.chunk",
        "choices": [
            {
                "index": 0,
                "delta": {"content": text},
                "finish_reason": None,
            }
        ],
    }
    yield {
        "id": "chatcmpl-async-e2e",
        "object": "chat.completion.chunk",
        "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
    }


def tool_chunks(name, arguments, call_id):
    yield {
        "id": "chatcmpl-async-e2e",
        "object": "chat.completion.chunk",
        "choices": [
            {
                "index": 0,
                "delta": {
                    "tool_calls": [
                        {
                            "index": 0,
                            "id": call_id,
                            "type": "function",
                            "function": {
                                "name": name,
                                "arguments": json.dumps(arguments),
                            },
                        }
                    ]
                },
                "finish_reason": None,
            }
        ],
    }
    yield {
        "id": "chatcmpl-async-e2e",
        "object": "chat.completion.chunk",
        "choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}],
        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
    }


def flatten_text(value):
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        return "\n".join(flatten_text(item) for item in value)
    if isinstance(value, dict):
        return "\n".join(flatten_text(v) for v in value.values())
    return ""


def response_for(body):
    text = flatten_text(body)
    messages = body.get("messages") or []
    last = flatten_text(messages[-1]) if messages else text

    if "Generate a concise title" in text:
        return text_chunks("Async monitor e2e")

    if (
        "Your monitor on background job" in text
        or "matched pattern" in text
        or ("ASYNC_E2E_READY" in last and "monitor" not in last.lower())
    ):
        return text_chunks("ASYNC_E2E_DONE")

    if "Monitoring job" in text:
        return text_chunks("WAITING_FOR_MONITOR")

    shell_match = re.search(r"Background shell started with ID:\s*([A-Za-z0-9_-]+)", text)
    if shell_match:
        return tool_chunks(
            "monitor",
            {
                "shell_id": shell_match.group(1),
                "pattern": "ASYNC_E2E_READY",
                "timeout_seconds": 10,
            },
            "call_monitor_1",
        )

    return tool_chunks(
        "bash",
        {
            "command": "sleep 1; printf 'ASYNC_E2E_READY\\n'",
            "description": "Start async monitor marker",
            "run_in_background": True,
        },
        "call_bash_1",
    )


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        body = b"ok\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        with open(request_log, "ab") as f:
            f.write(raw + b"\n")
        try:
            body = json.loads(raw)
        except Exception:
            body = {}

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        for payload in response_for(body):
            self.wfile.write(sse_chunk(payload).encode())
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def log_message(self, *_args):
        return


sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
sock.listen(5)
port = sock.getsockname()[1]
with open(port_file, "w", encoding="utf-8") as f:
    f.write(str(port))

server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler, False)
server.socket = sock
server.server_bind = server.server_close = lambda self=server: None
threading.Thread(target=server.serve_forever, daemon=False).start()
PY

python3 "$PY_SERVER" "$PORT_FILE" "$REQ_LOG" >"$ART/local_openai.out" 2>"$ART/local_openai.err" &
SERVER_PID=$!
for _ in {1..50}; do
  [[ -s "$PORT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$PORT_FILE" ]] || fail "local OpenAI-compatible test server did not start"
PORT="$(cat "$PORT_FILE")"
BASE_URL="http://127.0.0.1:$PORT/v1"
log "local OpenAI-compatible test server: $BASE_URL"

export CRUSH_GLOBAL_CONFIG="$CFG_DIR"
cat > "$CFG_DIR/crush.yaml" <<EOF
agents:
  explore:
    allowed_mcp: null
  auditor:
    allowed_mcp: null
models:
  brain:
    model: async-e2e
    provider: local-openai
  explore:
    model: async-e2e
    provider: local-openai
  worker:
    model: async-e2e
    provider: local-openai
  plan:
    model: async-e2e
    provider: local-openai
  auditor:
    model: async-e2e
    provider: local-openai
providers:
  local-openai:
    id: local-openai
    name: Local OpenAI E2E
    type: openai
    api_key: test-e2e
    base_url: $BASE_URL
    models:
      - id: async-e2e
        name: Async E2E
        context_window: 200000
        default_max_tokens: 4096
        can_reason: false
EOF

log "starting crush in tmux"
"$TUI" start "$SESS" 160 45 -- \
  "cd $REPO && CRUSH_GLOBAL_CONFIG=$CFG_DIR CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 $CRUSH_BIN --data-dir $DATA_DIR --trace-file $TRACE" \
  | tee -a "$LOG"

"$TUI" expect "$SESS" 'Ready' 15 || fail "TUI not ready"

log "submitting async monitor prompt"
"$TUI" send "$SESS" 'Use tools to run the async monitor e2e. Start the background command, monitor it, and finish only after the monitor wakes you.'
"$TUI" key "$SESS" Enter

"$TUI" expect "$SESS" 'ASYNC_E2E_DONE' 45 || {
  log "TUI Screen content on failure:"
  "$TUI" text "$SESS" >> "$LOG" 2>&1
  fail "agent did not complete after monitor wakeup"
}

log "graceful quit"
"$TUI" quit "$SESS"
sleep 1

assert_file_nonempty "$TRACE"
assert_file_nonempty "$REQ_LOG"

log "asserting trace contains real bash and monitor tool calls"
jq -e 'select(.kind == "tool_started" and .tool_name == "bash")' "$TRACE" >/dev/null \
  || fail "trace missing bash tool_started"
jq -e 'select(.kind == "tool_finished" and .tool_name == "bash")' "$TRACE" >/dev/null \
  || fail "trace missing bash tool_finished"
jq -e 'select(.kind == "tool_started" and .tool_name == "monitor")' "$TRACE" >/dev/null \
  || fail "trace missing monitor tool_started"
jq -e 'select(.kind == "tool_finished" and .tool_name == "monitor")' "$TRACE" >/dev/null \
  || fail "trace missing monitor tool_finished"
monitor_started_count="$(jq -s '[.[] | select(.kind == "tool_started" and .tool_name == "monitor")] | length' "$TRACE")"
[[ "$monitor_started_count" == "1" ]] \
  || fail "expected exactly one monitor tool_started event, got $monitor_started_count"

log "asserting provider saw streamed tool-call conversation"
jq -e 'select(.stream == true)' "$REQ_LOG" >/dev/null \
  || fail "local provider did not receive stream=true requests"
grep -q 'Background shell started with ID:' "$REQ_LOG" \
  || fail "local provider never saw the background shell id"
grep -q 'Monitoring job' "$REQ_LOG" \
  || fail "local provider never saw monitor tool output"
! grep -q 'A background job you previously started has now completed' "$REQ_LOG" \
  || fail "background completion wake raced with monitor wake"

log "asserting trace entries are not duplicated after dedupe"
dupes="$(
  jq -c 'del(.sequence, .trace_key)' "$TRACE" | sort | uniq -d
)"
[[ -z "$dupes" ]] || fail "duplicate trace entries found: $dupes"

pass
