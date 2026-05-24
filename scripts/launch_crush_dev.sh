#!/usr/bin/env zsh
# crush-dev: ALWAYS the latest checkout, with full diagnostic surfaces ON.
#
# Build path:
#   Rebuilds main.go from $CRUSH_DEV_REPO on every invocation into its OWN
#   cache ($XDG_CACHE_HOME/crush-dev), completely isolated from prod's
#   crush-prod cache. Pay the second-or-two relink cost so the dev launcher
#   never silently runs a stale tree. Same model as crush-test, just with
#   the dev surface enabled.
#
# Diagnostic surfaces:
#   - --debug                  → slog level = Debug + HTTPRoundTripLogger
#   - --trace-file ...         → runtime task DAG written as JSONL on exit
#   - CRUSH_HTTP_DUMP_DIR      → raw HTTP req/resp body dump per provider,
#                                one "<provider>.jsonl" file each
#   - Logs land under $XDG_STATE_HOME/crush-dev/, so prod logs in the project's
#     .crush/logs/ aren't shadowed.
#
# Use `crush` for normal work; `crush-dev` only when you need the latest
# tree or the diagnostic surfaces (or both).
#
# Same architecture as the prod launcher: foreground exec into the binary plus
# a trap on EXIT for terminal cleanup. See launch_crush.sh for the rationale.

set -euo pipefail

restore_terminal() {
  printf '\e[?1000l\e[?1002l\e[?1003l\e[?1006l\e[?1015l\e[?2004l\e[?1049l\e[?25h'
  stty sane 2>/dev/null || true
}
trap restore_terminal EXIT

zsh -lic '
  set -euo pipefail

  repo_root="${CRUSH_DEV_REPO:-$HOME/Desktop/_cli_bases/crush}"
  # Dev binary lives in its OWN cache, never shared with prod. crush-prod
  # is updated only by `task build`; crush-dev is updated on every launch.
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-dev"
  binary_path="$cache_dir/crush"

  state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/crush-dev"
  mkdir -p "$state_dir" "$cache_dir"
  ts="$(date +%Y%m%d-%H%M%S)"
  trace_file="$state_dir/trace-$ts.jsonl"
  http_dump_dir="$state_dir/http-$ts"

  waitai_base="${WAITAI_CRUSH_BASE:-${WAITAI_BASE:-http://127.0.0.1:43917}}"
  waitai_key="${WAITAI_API_KEY:-${NCODER_WAITAI_KEY:-}}"

  if [[ -z "$waitai_key" ]]; then
    echo "Missing WAITAI_API_KEY or NCODER_WAITAI_KEY." >&2
    exit 1
  fi
  if [[ ! -d "$repo_root" ]]; then
    echo "Missing Crush repo: $repo_root. Set CRUSH_DEV_REPO." >&2
    exit 1
  fi

  # Rebuild before exec. Go build is content-addressed; an unchanged tree
  # is a fast relink (~300ms), a changed tree picks up the diff. No
  # mtime gymnastics — let the Go toolchain decide what is stale.
  printf "[crush-dev] building %s → %s\n" "$repo_root" "$binary_path" >&2
  (
    cd "$repo_root"
    CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -o "$binary_path" .
  )

  wecode_key="${WECODE_API_KEY:-}"
  if [[ -z "$wecode_key" && -f "$HOME/.codex/auth.json" ]]; then
    wecode_key="$(python3 -c "import json;print(json.load(open(\"$HOME/.codex/auth.json\"))[\"OPENAI_API_KEY\"])" 2>/dev/null || true)"
  fi

  # Local-authoritative: the agent and every subprocess/job it spawns live IN
  # this foreground TUI process. ESC pauses it, Ctrl+C kills the whole subtree.
  # The TUI owns its work — the server does NOT run it remotely.
  printf "[crush-dev] trace=%s\n[crush-dev] http_dump_dir=%s\n" "$trace_file" "$http_dump_dir" >&2

  relay_nats_url="${CRUSH_RELAY_NATS_URL:-nats://47.110.255.240:4222}"
  relay_token="${CRUSH_RELAY_TOKEN:-ymm_rpc_2026}"

  exec env \
    CRUSH_DISABLE_METRICS=1 \
    CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 \
    CRUSH_DISABLE_DEFAULT_PROVIDERS=1 \
    CRUSH_HTTP_DUMP_DIR="$http_dump_dir" \
    WAITAI_CRUSH_BASE="$waitai_base" \
    WAITAI_API_KEY="$waitai_key" \
    NCODER_WAITAI_KEY="$waitai_key" \
    WECODE_API_KEY="$wecode_key" \
    CRUSH_RELAY_NATS_URL="$relay_nats_url" \
    CRUSH_RELAY_TOKEN="$relay_token" \
    "$binary_path" --debug --trace-file "$trace_file" "$@"
' zsh "$@"
