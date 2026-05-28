执行 Nushell 命令并返回输出。**适合结构化数据处理**（JSON 过滤、表格排序/聚合、多列管道），输出可以是 JSON 格式。

**适合场景**：
- 对 JSON/CSV/表格数据做 filter、select、sort、group-by
- 需要结构化管道输出供后续 view/rg 处理
- `bash` + `jq` 太繁琐的数据转换

**不适合**：执行系统命令、文件读写、网络请求——这些用 `bash`。
