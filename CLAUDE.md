# Crush Facade (L0 Entrypoint)

## Build / Test / Format Commands
- **构建 / 运行**: `go build .` 或 `go run .`
- **测试**: `task test` 或 `go test ./...`
- **更新 Golden 文件**: `go test ./... -update`
- **代码格式化**: `task fmt` (底层使用 `gofumpt -w .` 或 `goimports`)
- **代码静态检查**: `task lint:fix`
- **现代化重构**: `task modernize`
- **性能开发分析**: `task dev`

## 架构边界与下钻导航
本仓作为 Go 语言编写的 CLI/TUI 集成层。在阅读或修改本仓代码时，如涉及底层语言服务、元编程以及 Agent 调度规则，请**直接下钻**读取以下权威 SSOT 事实源，**禁止在本地复制规范**：

1.  **LSP/MCP 接口与交互契约**
    -   权威事实源: [AGENT_GUIDE.md](file:///home/junknet/linege/nim-src/langserver/AGENT_GUIDE.md)
    -   用途: 规范 LSP 表面 API、非标方法（如 `nimCheckFile`）、`Diagnostic.data` 格式和 Agent 专用帮助契约。
2.  **nim-core 元能力消费规约**
    -   权威事实源: [docs/CLAUDE.md](file:///home/junknet/linege/nim-core/docs/CLAUDE.md)
    -   用途: 业务仓调用 `nim-core` 的 Result 错误流规范、环境接入、CCache 共享配置等。
3.  **ncoder 调度设计 (DAG 扇区)**
    -   权威事实源: [recursive_dag_design.md](file:///home/junknet/linege/nim-core/docs/ncoder/recursive_dag_design.md)
    -   用途: Task/EvidenceRef/Ownership 三元组设计，以及调度队列逻辑。

## 本地开发要求
- **全局命名与编写哲学**: 严格遵循 [全局 CLAUDE.md](file:///home/junknet/.claude/CLAUDE.md)。
- **可观测性约束**: 涉及 UI/状态变更必须输出结构化 debug log。
- **不兼容与独狼原则**: 单人开发，无下游消费者依赖。对陈旧接口或双跑逻辑采取双删机制，不进行多轨道 fallback 或兼容过渡。
- **CGO与构建实验**: 编译时强制 `CGO_ENABLED=0` 并启用 `GOEXPERIMENT=greenteagc`。
