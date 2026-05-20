#!/usr/bin/env bash
# acceptance/run.sh —— 跑所有 scenarios,汇总 PASS/FAIL/SKIP
#
# 用法:
#   ./acceptance/run.sh                         # 跑全部
#   ./acceptance/run.sh smoke_landing           # 跑指定一个
#   ./acceptance/run.sh '*_nimlsp_*'            # glob 过滤
#
# 退出码:0 全 PASS(SKIP 不算 fail),非 0 = 至少一个 FAIL

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCEN_DIR="$ROOT/scenarios"

filter="${1:-*}"

pass=0; fail=0; skip=0; failed=()
echo "=== crush acceptance run ==="
echo "scenario dir: $SCEN_DIR"
echo

# 收集脚本(.sh + .py),按文件名 glob 过滤
mapfile -t scripts < <(
  find "$SCEN_DIR" -maxdepth 1 -type f \( -name "*.sh" -o -name "*.py" \) \
    | grep -E "/$filter\.(sh|py)$" | sort
)

if (( ${#scripts[@]} == 0 )); then
  echo "no scenarios match '$filter'" >&2
  exit 2
fi

for s in "${scripts[@]}"; do
  name="$(basename "${s%.*}")"
  printf "── %-40s ── " "$name"
  if [[ "$s" == *.py ]]; then
    python3 "$s" >/dev/null 2>&1
  else
    bash "$s" >/dev/null 2>&1
  fi
  rc=$?
  case $rc in
    0)  echo "PASS"; pass=$((pass+1)) ;;
    77) echo "SKIP"; skip=$((skip+1)) ;;
    *)  echo "FAIL (exit=$rc)"; fail=$((fail+1)); failed+=("$name") ;;
  esac
done

echo
echo "── summary ──"
echo "  PASS: $pass"
echo "  FAIL: $fail"
echo "  SKIP: $skip"
if (( fail > 0 )); then
  echo
  echo "failed scenarios(看 acceptance/artifacts/<name>/run.log 排查):"
  printf '  - %s\n' "${failed[@]}"
fi

exit $(( fail > 0 ? 1 : 0 ))
