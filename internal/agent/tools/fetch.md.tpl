将 URL 内容读入当前 context（文本 / markdown / html，最大 {{ .MaxFetchSizeKB }}KB）；无 AI 处理，原始内容直接返回。

**四种"拿 URL"工具选择规则**：
- 需要保存为本地文件（二进制/大文件）→ 用 `Download`
- 需要 AI 分析/提取/回答问题 → 用 `websearch-agent`
- 在 sub-agent 内抓网页 → 用 `web_fetch`
- 读原始 API 响应 / 小文本内容进 context → 用 `Fetch`（本工具）
{{- if .GhAvailable }}
- GitHub 仓库/Issue/PR 精确链接 → 用 bash 里的 `gh` CLI{{- end }}

