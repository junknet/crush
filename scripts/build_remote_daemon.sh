#!/usr/bin/env bash
# Cross-compiles the minimal crush-remote daemon for every supported remote
# target into internal/iodriver/embedbin/, where it is //go:embed-ed into crush.
# The daemon imports only internal/iodriver (stdlib-only), so each binary is a
# few MB rather than the ~100MB full crush binary the daemon path would
# otherwise ship. -trimpath keeps the output reproducible (no spurious diffs).
set -euo pipefail
cd "$(dirname "$0")/.."
out=internal/iodriver/embedbin
mkdir -p "$out"
for target in linux/amd64 linux/arm64; do
  goos=${target%/*}; goarch=${target#*/}
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w" \
    -o "$out/crush-remote_${goos}_${goarch}" ./cmd/crush-remote
done
echo "built crush-remote daemons: $(cd "$out" && ls crush-remote_* | tr '\n' ' ')"
