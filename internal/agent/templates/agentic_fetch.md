使用 AI 子智能体抓取 URL 或搜索网页，能够提取、总结和回答问题；比 `fetch` 慢且成本高。

**选择规则**：
- 需要理解/提取/回答网页内容 → 用 `agentic_fetch`（本工具）
- 只需要原始文本或 API 响应 → 用 `fetch`（更快、更省）
- 需要保存文件 → 用 `download`
- sub-agent 内读网页 → 用 `web_fetch`

