#!/usr/bin/env zsh
# crush launcher (production). POSIX-sh-safe so it can be invoked from any
# parent shell — including crush's own embedded mvdan/sh, which does not
# accept zsh-specific quoting tricks (the previous heredoc-via-`zsh -lic`
# approach printed its own source on mvdan due to literal `\n` mishandling).
#
# Goals:
#   1. Compose the env crush needs without polluting the parent shell.
#   2. Drive the cached prod binary at $XDG_CACHE_HOME/crush-prod/crush so the
#      launcher and `task build` stay in sync.
#   3. Smart auto-rebuild: if any source under $CRUSH_DEV_REPO is newer than
#      the cached binary, transparently rebuild before exec. Avoids the "I
#      edited and re-ran but it's still the old code" footgun, and skips the
#      rebuild cost when nothing changed.
#   4. **Always restore the terminal on exit** — bubbletea's alt-screen +
#      xterm mouse tracking + bracketed paste must be torn down even when
#      crush panics or quits abruptly. We do *not* exec into crush directly
#      until the very last step; the trap is set on the outer shell so it
#      fires regardless of how we exit.
#
# IMPORTANT: crush is a TUI that must own the controlling TTY. Run it in the
# foreground — backgrounding (`... &`) triggers SIGTTOU and zsh suspends the
# job with "suspended (tty output)".

set -eu

restore_terminal() {
  printf '\033[?1000l\033[?1002l\033[?1003l\033[?1006l\033[?1015l\033[?2004l\033[?1049l\033[?25h'
  stty sane 2>/dev/null || true
}
trap restore_terminal EXIT

repo_root="${CRUSH_DEV_REPO:-$HOME/Desktop/_cli_bases/crush}"
cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-prod"
binary_path="$cache_dir/crush"
go_tmp_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-go-tmp"
go_cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-go-build"

# Smart auto-rebuild: only when a tracked source is newer than the cached
# binary. rg --files honours .gitignore so we do not chase generated
# cassettes/artifacts. The check is done via a single `find -newer` call,
# which is POSIX and far simpler than walking-and-stat-comparing each file.
needs_rebuild=0
if [ ! -x "$binary_path" ]; then
  needs_rebuild=1
elif [ -d "$repo_root" ]; then
  # Any *.go / *.md.tpl / *.md / embed *.json newer than the binary triggers
  # a rebuild. `find -newer X -print -quit` exits as soon as it sees one
  # newer file, so this is O(repo) only in the cold-cache case.
  newer=$(find "$repo_root" \
    -path "$repo_root/.git" -prune -o \
    -path "$repo_root/scratch" -prune -o \
    -path "$repo_root/acceptance/artifacts" -prune -o \
    \( -name '*.go' -o -name '*.md.tpl' -o -name 'provider.json' \) \
    -newer "$binary_path" -print 2>/dev/null | head -n 1)
  if [ -n "$newer" ]; then
    needs_rebuild=1
  fi
fi
if [ "$needs_rebuild" = "1" ]; then
  if [ ! -d "$repo_root" ]; then
    echo "Missing Crush repo: $repo_root. Set CRUSH_DEV_REPO." >&2
    exit 1
  fi
  echo "[crush] source newer than binary — rebuilding $binary_path ..." >&2
  mkdir -p "$cache_dir" "$go_tmp_dir" "$go_cache_dir"
  (
    cd "$repo_root"
    TMPDIR="$go_tmp_dir" GOTMPDIR="$go_tmp_dir" GOCACHE="$go_cache_dir" \
      CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -trimpath -ldflags='-s -w' -o "$binary_path" .
  )
fi

if [ ! -x "$binary_path" ]; then
  echo "Missing Crush binary: $binary_path. Run task build in $repo_root." >&2
  exit 1
fi

# WECODE_API_KEY: auto-derive from ~/.codex/auth.json if not exported.
wecode_key="${WECODE_API_KEY:-}"
if [ -z "$wecode_key" ] && [ -f "$HOME/.codex/auth.json" ]; then
  wecode_key=$(python3 -c "import json,sys; print(json.load(open('$HOME/.codex/auth.json')).get('OPENAI_API_KEY',''))" 2>/dev/null || true)
fi

# Local-authoritative: the agent and every subprocess it spawns live IN
# this foreground TUI process. Ctrl+C kills the whole subtree.
relay_nats_url="${CRUSH_RELAY_NATS_URL:-nats://47.110.255.240:4222}"
relay_token="${CRUSH_RELAY_TOKEN:-ymm_rpc_2026}"

exec env \
  CRUSH_DISABLE_METRICS=1 \
  CRUSH_LAUNCHER_NAME=crush \
  CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 \
  WECODE_API_KEY="$wecode_key" \
  CRUSH_RELAY_NATS_URL="$relay_nats_url" \
  CRUSH_RELAY_TOKEN="$relay_token" \
  "$binary_path" "$@"
