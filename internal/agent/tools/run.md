运行一次性本地脚本（Python / Node / shell inline 代码块）。**不是通用命令执行工具**——单行命令、管道、后台进程统一用 `bash`。

**适合场景**：需要循环、map/filter、排序、结构化解析、JSONL 聚合等用多个 bash 调用会很繁琐的一次性分析。

参数：
- `language`：`shell`（默认）、`python`、`node`
- `script`：要执行的源代码字符串（inline，非文件路径）
- `timeout_seconds`：可选超时，默认 60，最大 300

**选择规则**（bash vs run）：
- 执行命令行 / 管道 / 调用系统工具 → 用 `bash`
- 写一段 Python/Node 脚本做数据处理 → 用 `run`
- 需要后台运行 / 长跑进程 → 用 `bash` + `run_in_background=true`

约束：
- 优先用原生工具（`rg`、`view`、`ast_grep`）做代码库搜索，不要在 run 里再调 shell 命令
- 脚本保持小而确定性，只打印最终结论
- 禁止：安装包、守护进程、交互式提示、foreground sleep 轮询
