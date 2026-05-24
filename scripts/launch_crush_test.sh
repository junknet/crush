#!/usr/bin/env zsh
# crush-test launcher.
#
# Rebuilds the current checkout from main.go on every invocation, then runs the
# freshly built binary. This is the "latest tree" path for local verification.
# Use it when you want the exact current working tree instead of the cached prod
# launcher.

set -euo pipefail

restore_terminal() {
  printf '\e[?1000l\e[?1002l\e[?1003l\e[?1006l\e[?1015l\e[?2004l\e[?1049l\e[?25h'
  stty sane 2>/dev/null || true
}
trap restore_terminal EXIT

zsh -lic '
  set -euo pipefail

  repo_root="${CRUSH_TEST_REPO:-${CRUSH_DEV_REPO:-$HOME/Desktop/_cli_bases/crush}}"
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-test"
  binary_path="$cache_dir/crush"

  waitai_base="${WAITAI_CRUSH_BASE:-${WAITAI_BASE:-http://127.0.0.1:43917}}"
  waitai_key="${WAITAI_API_KEY:-${NCODER_WAITAI_KEY:-}}"

  if [[ -z "$waitai_key" ]]; then
    echo "Missing WAITAI_API_KEY or NCODER_WAITAI_KEY." >&2
    exit 1
  fi
  if [[ ! -d "$repo_root" ]]; then
    echo "Missing Crush repo: $repo_root. Set CRUSH_TEST_REPO." >&2
    exit 1
  fi

  mkdir -p "$cache_dir"
  (
    cd "$repo_root"
    go build -o "$binary_path" main.go
  )

  wecode_key="${WECODE_API_KEY:-}"
  if [[ -z "$wecode_key" && -f "$HOME/.codex/auth.json" ]]; then
    wecode_key="$(python3 -c "import json;print(json.load(open(\"$HOME/.codex/auth.json\"))[\"OPENAI_API_KEY\"])" 2>/dev/null || true)"
  fi

  relay_nats_url="${CRUSH_RELAY_NATS_URL:-nats://47.110.255.240:4222}"
  relay_token="${CRUSH_RELAY_TOKEN:-ymm_rpc_2026}"

  exec env \
    CRUSH_DISABLE_METRICS=1 \
    CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 \
    CRUSH_DISABLE_DEFAULT_PROVIDERS=1 \
    WAITAI_CRUSH_BASE="$waitai_base" \
    WAITAI_API_KEY="$waitai_key" \
    NCODER_WAITAI_KEY="$waitai_key" \
    WECODE_API_KEY="$wecode_key" \
    CRUSH_RELAY_NATS_URL="$relay_nats_url" \
    CRUSH_RELAY_TOKEN="$relay_token" \
    "$binary_path" "$@"
' zsh "$@"
