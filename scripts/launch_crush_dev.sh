#!/usr/bin/env zsh
# crush-dev: same binary as `crush`, with diagnostic surfaces ON:
#   - --debug                  → slog level = Debug + HTTPRoundTripLogger
#   - --trace-file ...         → runtime task DAG written as JSONL on exit
#   - CRUSH_HTTP_DUMP_DIR      → raw HTTP req/resp body dump per provider,
#                                one "<provider>.jsonl" file each, all providers
#   - Logs land under $XDG_STATE_HOME/crush-dev/, so prod logs in the project's
#     .crush/logs/ aren't shadowed.
#
# Use `crush` for normal work; `crush-dev` only when you need traces.
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
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-prod"
  binary_path="$cache_dir/crush"

  state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/crush-dev"
  mkdir -p "$state_dir"
  ts="$(date +%Y%m%d-%H%M%S)"
  trace_file="$state_dir/trace-$ts.jsonl"
  http_dump_dir="$state_dir/http-$ts"

  waitai_base="${WAITAI_CRUSH_BASE:-${WAITAI_BASE:-http://127.0.0.1:43917}}"
  waitai_key="${WAITAI_API_KEY:-${NCODER_WAITAI_KEY:-}}"

  if [[ -z "$waitai_key" ]]; then
    echo "Missing WAITAI_API_KEY or NCODER_WAITAI_KEY." >&2
    exit 1
  fi
  if [[ ! -x "$binary_path" ]]; then
    echo "Missing Crush binary: $binary_path. Run task build in $repo_root." >&2
    exit 1
  fi

  wecode_key="${WECODE_API_KEY:-}"
  if [[ -z "$wecode_key" && -f "$HOME/.codex/auth.json" ]]; then
    wecode_key="$(python3 -c "import json;print(json.load(open(\"$HOME/.codex/auth.json\"))[\"OPENAI_API_KEY\"])" 2>/dev/null || true)"
  fi

  printf "[crush-dev] trace=%s\n[crush-dev] http_dump_dir=%s\n" "$trace_file" "$http_dump_dir" >&2

  exec env \
    CRUSH_DISABLE_METRICS=1 \
    CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 \
    CRUSH_DISABLE_DEFAULT_PROVIDERS=1 \
    CRUSH_HTTP_DUMP_DIR="$http_dump_dir" \
    WAITAI_CRUSH_BASE="$waitai_base" \
    WAITAI_API_KEY="$waitai_key" \
    NCODER_WAITAI_KEY="$waitai_key" \
    WECODE_API_KEY="$wecode_key" \
    "$binary_path" --debug --trace-file "$trace_file" "$@"
' zsh "$@"
