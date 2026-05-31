将 URL 直接流式下载保存为本地文件（二进制安全，最大 {{ .MaxDownloadTimeout }}s 超时）；已存在文件直接覆盖，无警告。

**选择规则**：
- 需要保存文件到磁盘（图片、压缩包、大二进制、需要 rg/view 后续处理）→ 用 `Download`（本工具）
- 需要读取内容进 context → 用 `Fetch`

