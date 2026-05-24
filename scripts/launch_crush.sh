#!/usr/bin/env zsh
# crush launcher (production).
#
# Goals:
#   1. Compose the env crush needs (waitai keys, disable-defaults flags) without
#      polluting the parent shell.
#   2. Drive the cached prod binary at $XDG_CACHE_HOME/crush-prod/crush so the
#      launcher and `task build` stay in sync.
#   3. **Always restore the terminal on exit** — bubbletea's alt-screen +
#      xterm mouse tracking + bracketed paste must be torn down even when crush
#      panics or quits abruptly. We do *not* exec into crush; we keep the
#      wrapper alive so its EXIT trap can fire the reset sequences.
#
# Caveat: a SIGKILL targeted at this wrapper would skip the trap. Use SIGTERM
# (`pkill crush`) when you can — bubbletea handles SIGTERM gracefully on its
# own. If you ever land in a stuck terminal, run `crush-rescue` blindly.
#
# IMPORTANT: crush is a TUI that must own the controlling TTY. Run it in the
# foreground — backgrounding (`... &`) triggers SIGTTOU and zsh suspends the
# job with "suspended (tty output)". A bare foreground call plus a trap on
# EXIT gets us cleanup without the backgrounding hazard.

set -euo pipefail

restore_terminal() {
  printf '\e[?1000l\e[?1002l\e[?1003l\e[?1006l\e[?1015l\e[?2004l\e[?1049l\e[?25h'
  stty sane 2>/dev/null || true
}
trap restore_terminal EXIT

# Run crush in the foreground; the trap fires regardless of how we exit
# (normal, panic, SIGINT, SIGTERM). bubbletea forwards SIGINT/SIGTERM into
# its own graceful shutdown, so the terminal usually self-restores too — the
# trap is the belt-and-suspenders backup.
zsh -lic '
  set -euo pipefail

  repo_root="${CRUSH_DEV_REPO:-$HOME/Desktop/_cli_bases/crush}"
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-prod"
  binary_path="$cache_dir/crush"

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

  # WECODE_API_KEY: auto-derive from ~/.codex/auth.json if not exported.
  wecode_key="${WECODE_API_KEY:-}"
  if [[ -z "$wecode_key" && -f "$HOME/.codex/auth.json" ]]; then
    wecode_key="$(python3 -c "import json;print(json.load(open(\"$HOME/.codex/auth.json\"))[\"OPENAI_API_KEY\"])" 2>/dev/null || true)"
  fi

  # Local-authoritative: the agent and every subprocess it spawns live IN this
  # foreground TUI process. Ctrl+C kills the whole subtree. The TUI owns its work.
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
