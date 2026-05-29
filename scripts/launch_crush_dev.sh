#!/usr/bin/env zsh
# crush-dev: ALWAYS the latest checkout, with full diagnostic surfaces ON.
# POSIX-sh-safe — see launch_crush.sh for why (mvdan/sh in crush's embedded
# bash tool does not handle zsh-style `-c` heredocs).
#
# Build path:
#   Rebuilds main.go from $CRUSH_DEV_REPO on every invocation into its OWN
#   cache ($XDG_CACHE_HOME/crush-dev), completely isolated from prod's
#   crush-prod cache. Go's build cache makes a no-op relink ~300ms.
#
# Diagnostic surfaces:
#   - --debug                  → slog level = Debug + HTTPRoundTripLogger
#   - --trace-file ...         → runtime task DAG written as JSONL on exit
#   - CRUSH_HTTP_DUMP_DIR      → raw HTTP req/resp body dump per provider
#   - Logs land under $XDG_STATE_HOME/crush-dev/

set -eu

restore_terminal() {
  printf '\033[?1000l\033[?1002l\033[?1003l\033[?1006l\033[?1015l\033[?2004l\033[?1049l\033[?25h'
  stty sane 2>/dev/null || true
}
trap restore_terminal EXIT

repo_root="${CRUSH_DEV_REPO:-$HOME/Desktop/_cli_bases/crush}"
cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-dev"
binary_path="$cache_dir/crush"
go_tmp_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-go-tmp"
go_cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-go-build"

state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/crush-dev"
mkdir -p "$state_dir" "$cache_dir" "$go_tmp_dir" "$go_cache_dir"
ts=$(date +%Y%m%d-%H%M%S-%N)-$$
trace_file="$state_dir/trace-$ts.jsonl"
http_dump_dir="$state_dir/http-$ts"

if [ ! -d "$repo_root" ]; then
  echo "Missing Crush repo: $repo_root. Set CRUSH_DEV_REPO." >&2
  exit 1
fi

# Rebuild every launch — dev guarantees latest tree. Go build is
# content-addressed; an unchanged tree is a fast relink (~300ms).
# Note: NO -ldflags='-s -w' so panics keep symbols.
printf "[crush-dev] building %s -> %s\n" "$repo_root" "$binary_path" >&2
(
  cd "$repo_root"
  TMPDIR="$go_tmp_dir" GOTMPDIR="$go_tmp_dir" GOCACHE="$go_cache_dir" \
    CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -o "$binary_path" .
)

wecode_key="${WECODE_API_KEY:-}"
if [ -z "$wecode_key" ] && [ -f "$HOME/.codex/auth.json" ]; then
  wecode_key=$(python3 -c "import json,sys; print(json.load(open('$HOME/.codex/auth.json')).get('OPENAI_API_KEY',''))" 2>/dev/null || true)
fi

printf "[crush-dev] trace=%s\n[crush-dev] http_dump_dir=%s\n" "$trace_file" "$http_dump_dir" >&2

relay_nats_url="${CRUSH_RELAY_NATS_URL:-nats://47.110.255.240:4222}"
relay_token="${CRUSH_RELAY_TOKEN:-ymm_rpc_2026}"

exec env \
  CRUSH_DISABLE_METRICS=1 \
  CRUSH_LAUNCHER_NAME=crush-dev \
  CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 \
  CRUSH_HTTP_DUMP_DIR="$http_dump_dir" \
  WECODE_API_KEY="$wecode_key" \
  CRUSH_RELAY_NATS_URL="$relay_nats_url" \
  CRUSH_RELAY_TOKEN="$relay_token" \
  "$binary_path" --debug --trace-file "$trace_file" "$@"
