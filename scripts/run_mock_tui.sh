#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

mock_base="${CRUSH_MOCK_LLM_BASE:-${CRUSH_MOCK_BASE:-http://127.0.0.1:43917}}"
mock_key="${CRUSH_MOCK_API_KEY:-${CRUSH_MOCK_KEY:-}}"

if [[ -z "$mock_key" ]]; then
  echo "Missing CRUSH_MOCK_API_KEY or CRUSH_MOCK_KEY." >&2
  exit 1
fi

cd "$repo_root"

cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/crush-mock"
binary_path="$cache_dir/crush"

mkdir -p "$cache_dir"

trace_path="$(mktemp "${cache_dir}/trace.XXXXXX.jsonl")"

needs_build=0
if [[ ! -x "$binary_path" ]]; then
  needs_build=1
else
  while IFS= read -r -d '' source_path; do
    if [[ "$source_path" -nt "$binary_path" ]]; then
      needs_build=1
      break
    fi
  done < <(git -C "$repo_root" ls-files -z '*.go' 'go.mod' 'go.sum')
fi

if [[ "$needs_build" -eq 1 ]]; then
  go build -o "$binary_path" .
fi

exec env \
  CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 \
  CRUSH_DISABLE_DEFAULT_PROVIDERS=1 \
  CRUSH_TRACE_FILE="$trace_path" \
  CRUSH_MOCK_LLM_BASE="$mock_base" \
  CRUSH_MOCK_API_KEY="$mock_key" \
  CRUSH_MOCK_KEY="$mock_key" \
  "$binary_path" \
  --cwd "$repo_root" \
  --trace-file "$trace_path"
