#!/usr/bin/env zsh
set -euo pipefail

exec zsh -lic '
  set -euo pipefail

  script_path="$1"
  repo_root="${CRUSH_DEV_REPO:-$(cd "$(dirname "$script_path")/.." && pwd)}"
  port="${CRUSH_SERVER_PORT:-28080}"
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}"
  binary_path="${CRUSH_SERVER_BINARY:-$cache_dir/crush-prod/crush}"

  echo "=== [1/2] 编译 Crush server 二进制 ==="
  mkdir -p "$(dirname "$binary_path")"

  needs_build=0
  if [[ ! -x "$binary_path" ]]; then
    needs_build=1
  else
    while IFS= read -r -d "" source_path; do
      if [[ "$source_path" -nt "$binary_path" ]]; then
        needs_build=1
        break
      fi
    done < <(git -C "$repo_root" ls-files -z "*.go" "go.mod" "go.sum" "internal/agent/**/*.md" "internal/agent/**/*.md.tpl" "internal/agent/hyper/provider.json")
  fi

  if [[ "$needs_build" -eq 1 ]]; then
    tmp_path="$binary_path.tmp"
    rm -f "$tmp_path"
    CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -trimpath -o "$tmp_path" "$repo_root"
    mv "$tmp_path" "$binary_path"
  else
    echo "二进制已是最新，跳过编译。"
  fi

  echo "=== [2/2] 启动 Crush API server ==="
  lan_ip="$(hostname -I 2>/dev/null || true)"
  lan_ip="${lan_ip%% *}"
  if [[ -z "$lan_ip" || "$lan_ip" == 127.* ]]; then
    lan_ip="127.0.0.1"
  fi

  echo ""
  echo "--------------------------------------------------------"
  echo "  Crush API Server"
  echo "  本机: http://127.0.0.1:$port"
  echo "  手机: http://$lan_ip:$port"
  echo "  Workspace: $repo_root"
  echo "--------------------------------------------------------"
  echo ""

  exec env \
    CRUSH_DISABLE_METRICS="${CRUSH_DISABLE_METRICS:-1}" \
    "$binary_path" server --cwd "$repo_root" --host "tcp://0.0.0.0:$port" --debug --register-cwd
' zsh "$0"
