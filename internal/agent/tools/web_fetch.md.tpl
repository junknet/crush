在 sub-agent 内抓取网页并转换为 markdown；超过 50KB 的大页面保存到临时文件供 `rg`/`view` 后续读取。

**选择规则**：
- 在 sub-agent / explore agent 内读网页 → 用 `web_fetch`（本工具）
- 主 agent 读原始 API 内容 → 用 `fetch`
- 需要 AI 分析提取 → 用 `agentic_fetch`
- 需要保存到本地文件 → 用 `download`
{{- if .GhAvailable }}
- GitHub 仓库/Issue/PR 精确链接 → 用 bash 里的 `gh` CLI{{- end }}

