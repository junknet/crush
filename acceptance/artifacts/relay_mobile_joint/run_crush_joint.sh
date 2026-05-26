#!/usr/bin/env bash
set -euo pipefail
export CRUSH_DISABLE_METRICS=1
export CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1
export CRUSH_DISABLE_DEFAULT_PROVIDERS=1
export WAITAI_CRUSH_BASE=http://127.0.0.1:43917
export WAITAI_API_KEY=sk-YOeczqKtrs5f5iddc60GsvlmevfW0A8NRUl03b7Xf7FSvhsZ
export NCODER_WAITAI_KEY=sk-YOeczqKtrs5f5iddc60GsvlmevfW0A8NRUl03b7Xf7FSvhsZ
export WECODE_API_KEY=sk-b8ddf80d988561620925a38cc50bc256dd33240efd2932e5e9a97ae24b99da15
export CRUSH_RELAY_NATS_URL=ws://127.0.0.1:9091
export CRUSH_RELAY_TOKEN=ymm_rpc_2026
cd /home/junknet/Desktop/_cli_bases/crush
exec /home/junknet/Desktop/_cli_bases/crush/acceptance/artifacts/relay_mobile_joint/crush-test.bin
