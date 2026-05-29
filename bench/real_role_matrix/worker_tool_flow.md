你是 worker agent。只读，不要修改文件。

真实任务：验证模型在真实 Crush 工具协议下能不能稳定、快速、按指令调用工具。

必须用工具执行这些检查：
1. 输出当前工作目录。
2. 找到 `internal/agent/coordinator.go` 中 `func getProviderOptions` 的行号。
3. 找到 `internal/agentsdk/provider.go` 中 `sdkReasoningEffort` 的行号。

输出要求：
- 用中文。
- 给出三项检查结果，必须包含精确文件路径和行号。
- 最后一行输出 `WORKER_DONE`。
