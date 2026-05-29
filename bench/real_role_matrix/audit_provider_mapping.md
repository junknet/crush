你是 Crush provider/agent 架构审计员。只读，不要修改文件。

真实任务来源：当前 Crush 正在迁移官方 OpenAI `/v1/responses`、Anthropic
`/v1/messages`、Gemini/Antigravity，并且需要确认各家 reasoning/cache/tool
映射是否真的打通。

必须先用工具检查源码，至少覆盖：
- `internal/agent/coordinator.go`
- `internal/agentsdk/provider.go`
- `/home/junknet/Desktop/agent-all-sdk-go/provider/codex/transform.go`
- `/home/junknet/Desktop/agent-all-sdk-go/provider/claude/transform.go`

输出要求：
- 用中文。
- 给出 3-6 条审计结论，每条必须带文件路径。
- 明确说明 `official-openai/gpt-5.5` 的 reasoning effort 最终如何进入请求。
- 明确说明 `anthropic-oauth` 的 thinking budget 是否会进入官方 Anthropic
  Messages 路径。
- 明确说明 prompt cache / cache_control 当前是哪家显式控制、哪家隐式或缺失。
