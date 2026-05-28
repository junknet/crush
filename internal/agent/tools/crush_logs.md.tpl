读取 agent 内部应用日志（默认 {{ .DefaultLines }} 条，最多 {{ .MaxLines }} 条）。**仅在诊断 agent 自身问题时调用**（provider 错误、tool 失败、LSP/MCP 连接问题），不用于用户任务执行。

<usage>
- Returns recent log entries from Crush's internal log file
- Use to diagnose issues with Crush itself (provider errors, tool failures,
  LSP problems, MCP connection issues)
- Entries shown in compact format: TIME LEVEL SOURCE MESSAGE key=value...
</usage>

<tips>
- Default returns last {{ .DefaultLines }} entries; use lines parameter for more (max {{ .MaxLines }})
- Look for ERROR and WARN entries first when diagnosing problems
</tips>
