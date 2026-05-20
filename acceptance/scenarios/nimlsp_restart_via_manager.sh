#!/usr/bin/env bash
# nimlsp_restart_via_manager.sh —— lsp_restart 工具的 Go 层验证。
#
# lsp_restart 不走 LSP 自定义 method;它对每个已注册的 client 调 client.Restart()。
# 已有的 internal/lsp 单测验证了 Restart() 本身;这里只跑这条测试链路,
# 证明 restart 路径在当前 fork 上仍 green。

source "$(dirname "$0")/../common.sh"
need_tui   # 主要是 need_go,但 tui-test 已经检查 crush bin 存在

cd "$REPO"

log "running internal/lsp restart-related tests"
go test ./internal/lsp/... -run 'Restart|Manager|Client' -count=1 -timeout 60s 2>&1 \
  | tee -a "$LOG"
rc=${PIPESTATUS[0]}
(( rc == 0 )) || fail "lsp restart-related tests failed (exit=$rc)"

log "running command_dag DAG tool tests(用作 LSP tool 工具链冒烟)"
go test ./internal/agent/tools/ -run 'TestCommandDAGTool' -count=1 -timeout 60s 2>&1 \
  | tee -a "$LOG"
rc=${PIPESTATUS[0]}
(( rc == 0 )) || fail "command_dag tests failed (exit=$rc)"

pass
