# Crush 真实 Role/Model 能力测试方法

> 目标：用真实 `crush-dev`、真实 provider/key、真实仓库任务，评估各
> role agent 的速度、工具稳定性、推理质量、上下文压力和 provider 参数映射。
> 不用 mock，不把单元测试当能力证明。

## 核心原则

- 测试入口必须是 `crush-dev` 或实际构建出的 CLI/TUI。
- provider 必须走用户真实配置和真实 key。
- prompt 必须来自真实业务仓库或真实历史问题，不能用玩具题。
- 每个结果必须留下 trace JSONL、HTTP dump、stdout/stderr 或 TUI 截图。
- 评估不只看 PASS/FAIL，还要看首 token、工具失败、schema 体积、缓存、
  reasoning tokens、自动压缩和模型输出的证据密度。

## 指定某个 role 使用某个模型

Crush 的 role 模型来自 `state.yaml` 的 `models:`：

```yaml
models:
  brain:
    provider: antigravity
    model: gemini-3-flash-agent
    max_tokens: 12000
  explore:
    provider: antigravity
    model: gemini-3.5-flash-low
    max_tokens: 8192
  worker:
    provider: antigravity
    model: gemini-3-flash-agent
    max_tokens: 8192
  plan:
    provider: official-openai
    model: gpt-5.5
    max_tokens: 16384
    reasoning_effort: xhigh
  auditor:
    provider: official-openai
    model: gpt-5.5
    max_tokens: 16384
    reasoning_effort: xhigh
```

不要直接改全局生产配置做矩阵测试。标准做法是复制真实配置到临时目录，
只覆盖 `state.yaml`：

```bash
cfg="$(mktemp -d)"
cp ~/.config/crush/crush.yaml "$cfg/crush.yaml"
cat > "$cfg/state.yaml" <<'YAML'
models:
  brain:
    provider: antigravity
    model: gemini-3-flash-agent
    max_tokens: 12000
  explore:
    provider: antigravity
    model: gemini-3.5-flash-low
    max_tokens: 8192
  worker:
    provider: antigravity
    model: gemini-3-flash-agent
    max_tokens: 8192
  plan:
    provider: official-openai
    model: gpt-5.5
    max_tokens: 16384
    reasoning_effort: xhigh
  auditor:
    provider: official-openai
    model: gpt-5.5
    max_tokens: 16384
    reasoning_effort: xhigh
YAML

CRUSH_GLOBAL_CONFIG="$cfg" \
CRUSH_GLOBAL_DATA="$cfg/data" \
CRUSH_DISABLE_METRICS=1 \
CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 \
crush-dev run --quiet "调用 crush_info，只输出 model、agents、providers、mcp 四段。"
```

`crush-dev run -m provider/model` 只能快速覆盖顶层 brain 的主模型；
`--explore-model` 是 explore 快捷覆盖。要稳定测试 `plan`、`auditor`、
`worker`、`explore` 的真实 role 行为，使用上面的临时 `state.yaml`。

## 让指定 role 真正干活

顶层 `crush-dev run` 默认由 `brain` 接任务。要验证子 role，有两种方式。

方式一：让 brain 调用 `agent` 工具。

```bash
crush-dev run --quiet \
  "请调用 agent(role=explore)，让它调查当前仓库中 provider 参数映射在哪里实现；最后只汇总子 agent 的证据路径。"
```

可用 role：

- `explore`：只读代码库探索、证据收集、路径定位。
- `plan`：只读实现方案设计，沉默错误成本高，应该用强智力。
- `worker`：执行修改、重构、验证，可写文件。
- `auditor`：安全、数学、逻辑、协议映射对抗审查，应该用强智力。

方式二：用真实矩阵脚本批量跑 role/model case。

```bash
scripts/bench_real_roles.py --only explore --timeout 300
scripts/bench_real_roles.py --only worker_gemini_agent --timeout 300
scripts/bench_real_roles.py --cases bench/real_role_matrix/cases.jsonl --timeout 300
```

脚本做的事情：

- 读取 `bench/real_role_matrix/cases.jsonl`。
- 为每个 case 生成临时 `CRUSH_GLOBAL_CONFIG/state.yaml`。
- 复用真实 `~/.config/crush/crush.yaml` provider/key。
- 清掉 `CRUSH_MOCK_*`。
- 调用真实 `crush-dev run --quiet <prompt>`。
- 解析 trace，生成 `REPORT.md` 和 `results.jsonl`。

输出位置：

```text
~/.local/state/crush-real-bench/<run-id>/
  REPORT.md
  results.jsonl
  <case-id>/stdout.txt
  <case-id>/stderr.txt
  <case-id>/raw.log
```

## 真实运行时信息怎么抓

每次 `crush-dev` 启动都会打印：

```text
[crush-dev] trace=/home/junknet/.local/state/crush-dev/trace-....jsonl
[crush-dev] http_dump_dir=/home/junknet/.local/state/crush-dev/http-...
```

trace 是主证据。它包含：

- `task_*`：任务规划、开始、输出、失败。
- `llm_request_*`：provider/model、首事件、首文本、token、cache、schema。
- `tool_*`：工具名、输入、输出、失败、耗时。
- `command_*`：shell 命令、cwd、exit code、stdout/stderr 字节。
- `conversation_compaction_*`：自动压缩是否触发。
- `memory_*`：memory recall/save 是否干扰。

快速看本次请求的模型、schema、上下文压力：

```bash
trace=/path/to/trace.jsonl
jq -r '
  select(.kind=="llm_request_started") |
  {
    profile, provider_id, provider_type, model_id,
    context_message_count, context_bytes,
    preflight_estimated_input_tokens,
    context_window_tokens,
    auto_summarize_threshold_tokens,
    attachment_count, file_count,
    tool_count, tool_schema_bytes,
    max_output_tokens
  }
' "$trace"
```

快速看速度、缓存、reasoning：

```bash
jq -r '
  select(.kind=="llm_first_event" or .kind=="llm_first_text_delta" or .kind=="llm_request_finished") |
  {
    kind, profile, provider_id, model_id,
    first_event_latency_ms, first_text_latency_ms,
    duration_ms, finish_reason,
    input_tokens, output_tokens, total_tokens,
    reasoning_tokens, cache_creation_tokens, cache_read_tokens,
    auto_summarize_used_tokens,
    auto_summarize_triggered
  }
' "$trace"
```

快速看工具稳定性：

```bash
jq -r '
  select(.kind=="tool_started" or .kind=="tool_finished" or .kind=="tool_failed") |
  {
    kind, profile, tool_name, tool_call_id,
    duration_ms, success, tool_is_error,
    tool_input_bytes, tool_output_bytes,
    error
  }
' "$trace"
```

快速统计工具失败：

```bash
jq -r 'select(.kind|startswith("tool_")) | .kind + " " + (.tool_name // "")' "$trace" |
  sort | uniq -c
```

## MCP、tool schema、当前配置怎么确认

在真实 run 里让模型调用 `crush_info`：

```bash
crush-dev run --quiet \
  "请调用 crush_info，然后只输出 config_files、model、agents、providers、mcp、skills、user_constitution。"
```

`crush_info` 能直接看到：

- 当前 config 文件路径和 dirty 状态。
- role 到 provider/model 的映射。
- agents 是否启用、每个 agent 暴露多少工具。
- providers 是否启用、模型数量。
- MCP server 是否 connected，以及 tool/resource 数。
- skills 是否加载。
- `user_constitution` 是否只来自个人宪法文件。

schema 体积不需要肉眼数 HTTP body，trace 已有 `tool_count` 和
`tool_schema_bytes`。需要查具体 request body 时再看 `http_dump_dir`：

```bash
ls ~/.local/state/crush-dev/http-*
rg -n '"tools"|"tool_choice"|"responses"|"messages"|"thinking"|"reasoning_effort"' \
  ~/.local/state/crush-dev/http-<run-id>
```

## provider 参数映射重点

不同 provider 的“思考/推理”参数不是同一个东西，评估时要确认 trace 和
HTTP dump 是否符合预期。

- OpenAI 官方：优先 Responses 路径；`reasoning_effort` 映射到请求选项；
  reasoning 模型会请求 reasoning summary / encrypted reasoning include。
- Anthropic 官方 / OAuth：Messages 路径；没有 `reasoning_effort` 字段；
  `think=true` 时映射为 `thinking.budget_tokens`。
- Gemini 官方：`thinking_config`，Gemini 2 系用 `thinking_budget`，
  Gemini 3.5 类用 `thinking_level`。
- Antigravity：默认保留服务端配置；boost 时提升 thinking level。
- OpenAI-compatible：只用于兼容 provider；不要把它当官方 OpenAI 证明。

## 如何设计有区分度的 case

没有区分度的 case 会让 `extra-low` 和 `low` 都 PASS，结论无效。case 需要
同时压模型能力和工具策略。

`explore` case 应该测：

- 是否能判断要读哪些文件，而不是只看 README。
- 是否能并行使用 `Batch`。
- 是否能找到真实源文件、守门代码、撤回记录、配置入口。
- 是否能诚实列出不确定项，不越权给上线结论。
- 是否满足证据路径数量、命中关键词、禁止词。

`worker` case 应该测：

- 是否能在真实仓库完成小改动。
- 是否会读上下文后再改。
- 是否能用真实 CLI 路径验证。
- 是否留下可审计 diff 和 trace。

`plan` / `auditor` case 应该测：

- 是否能发现隐蔽协议/缓存/权限/并发风险。
- 是否能给可执行闭环方案，而不是泛泛建议。
- 是否能指出“不改会怎样”和验证路径。
- 这两个 role 沉默错误成本高，应优先最强智力模型。

case 文件格式：

```json
{"id":"explore_gemini_low","role":"explore","cwd":"/home/junknet/Desktop/research_toolkit","provider":"antigravity","model":"gemini-3.5-flash-low","max_tokens":8192,"prompt":"explore_pit_production_triage.md","min_expect_hits":12,"min_tools":2,"min_evidence_nodes":8,"max_tool_failed":0,"expect":["EXPLORE_EVIDENCE_DONE","真源文件"],"forbid":["只看 README"]}
```

## 之前那轮测试怎么做的

入口：

```bash
scripts/bench_real_roles.py --cases bench/real_role_matrix/cases.jsonl --timeout 300
```

典型输出：

```text
[bench] explore_gemini_low PASS trace=/home/junknet/.local/state/crush-dev/trace-...
[bench] report=/home/junknet/.local/state/crush-real-bench/<run-id>/REPORT.md
```

当时用的关键 case：

- `explore_pit_production_triage.md`：真实 `research_toolkit` 生产三岔口调研。
- `audit_provider_mapping.md`：审计 provider/model 参数映射。
- `worker_tool_flow.md`：检查 worker 是否能稳定走 evidence / provider 代码路径。

已有分析记录在：

- `bench/real_role_matrix/agent_dag_tool_design_20260529.md`
- `bench/real_role_matrix/explore_model_behavior_20260529.md`
- `bench/real_role_matrix/explore_research_toolkit_data.md`

结论样式不是“模型感觉不错”，而是：

```text
case=explore_gemini_low
provider/model=antigravity/gemini-3.5-flash-low
trace success=true
tools=2/2 failed=0 nodes=11
first_event_latency_ms=...
duration_ms=...
expected_hits=...
forbidden_hits=0
trace=/home/junknet/.local/state/crush-dev/trace-...
```

## 日志打点分析方法

最小判断表：

| 指标 | 看哪里 | 说明 |
|---|---|---|
| 首 token 慢 | `llm_first_event.first_event_latency_ms` | provider 排队、网络或模型慢 |
| 有事件但无文本 | `llm_first_text_delta` 缺失或很晚 | 模型长思考、tool schema 太大、provider 卡流 |
| schema 压力 | `tool_count`, `tool_schema_bytes` | MCP/tool 太多会拖慢首包 |
| 上下文压力 | `context_bytes`, `preflight_estimated_input_tokens` | 大上下文会降低速度并触发压缩 |
| 自动压缩 | `auto_summarize_triggered` | 压缩会改变后续行为，应单独标记 |
| 工具稳定性 | `tool_failed`, `tool_is_error` | 区分模型乱调和工具实现错误 |
| DAG 有效性 | `evidence_nodes` 或多 `tool_started` | 看是否真的并发收集证据 |
| 缓存有效性 | `cache_read_tokens`, `cache_creation_tokens` | 第二轮后应看到 cache read |
| 推理预算 | `reasoning_tokens` / HTTP dump | 看 reasoning effort 是否真的生效 |
| 回退/重试 | `llm_request_retry`, `llm_request_failed` | provider 错误、SSE 断流、首 token timeout |

常用一屏汇总：

```bash
jq -r '
  select(.kind=="task_finished" or .kind=="task_failed" or
         .kind=="llm_first_event" or .kind=="llm_request_finished" or
         .kind=="llm_request_retry" or .kind=="tool_failed") |
  {
    kind, profile, provider_id, model_id,
    duration_ms, first_event_latency_ms,
    input_tokens, output_tokens, reasoning_tokens,
    cache_read_tokens, tool_name, error, success
  }
' "$trace"
```

## 判定规则

PASS 必须同时满足：

- exit code 为 0。
- trace `task_finished.success=true`。
- 没有 provider 首 token timeout / SSE 中断 / API key 错误。
- `tool_failed` 不超过 case 设定阈值。
- 命中足够多真实证据关键词。
- 没有命中禁止词。
- 输出能被真实业务使用，且能从 trace 还原它怎么得到结论。

FAIL 要标明类型：

- provider failure：网络、key、quota、首 token timeout、SSE broken。
- protocol failure：官方协议/参数映射不对。
- tool failure：工具 schema、参数、执行、渲染、MCP 状态错误。
- strategy failure：工具能跑，但模型没读对、总结错、漏关键证据。
- UX failure：TUI 信息太丑、不可读、不知道 agent 在干嘛。

## TUI 交互测试补充

非交互矩阵跑完后，还要用 TUI 验证用户视角：

```bash
cd /home/junknet/Desktop/_cli_bases/crush
crush-dev
```

在 TUI 里验证：

- 顶栏 provider/model 是否是预期 role。
- 工具卡片是否能看懂，不暴露隐藏 CoT。
- sub-agent / DAG 活动窗口是否能看到 role、状态、耗时、工具数。
- tool error、provider error、retry 是否清晰显示。
- `crush_info` 输出是否包含当前 mcp、providers、agents。
- trace 与 TUI 看到的工具事件能对上。

需要截图或录屏时，把 trace 路径和截图路径写进结论。没有 trace 的截图只能算
UX 现象，不能算能力证明。
