获取 agent 自身运行时状态（模型、provider、LSP/MCP、技能、hooks、权限、禁用工具）。**仅在诊断 agent 自身配置或连接问题时调用**，不用于用户任务执行。无需参数。

<usage>
- Shows active model and provider, LSP/MCP server status, skills,
  hooks, permissions mode, disabled tools, and key options
- Use when diagnosing why something isn't working (missing diagnostics,
  provider errors, MCP disconnections)
- No parameters needed — always returns the full current state
</usage>

<tips>
- Check [lsp] and [mcp] sections for service health
- Check [providers] to see which providers are enabled and available
- Check [skills] to see which skills are available and whether they have been
  loaded this session
- Check [hooks] to see which hook events are configured and whether the
  hook runner is active
- Pair with the crush-config skill to fix configuration issues
</tips>
