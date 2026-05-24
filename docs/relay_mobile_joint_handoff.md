# Relay Mobile Joint Handoff

这份文档用于交接 `crush-test` TUI、远端 NATS relay、以及 Android mobile client 的联调验证。

## 当前已验证状态

- `crush-test` 已安装到 `~/.local/bin/crush-test`。
- 远端 NATS 可用：
  - `ws://47.110.255.240:8443`
  - token: `ymm_rpc_2026`
- mobile 客户端包名：`com.junknet.crushmobile`
- session 列表使用 NATS KV `CRUSH_SESSIONS`。
- mobile 侧已经把 `agent_finished` 视为结束态，避免 busy spinner 一直挂住。

## 关键文件

- 联调脚本：[`acceptance/scenarios/relay_mobile_joint.sh`](../acceptance/scenarios/relay_mobile_joint.sh)
- 共享 bootstrap：[`acceptance/common.sh`](../acceptance/common.sh)
- NATS relay：[`internal/relay/relay.go`](../internal/relay/relay.go)
- mobile API：[`mobile/crush_mobile/lib/crush/api.ts`](../mobile/crush_mobile/lib/crush/api.ts)
- mobile UI：[`mobile/crush_mobile/app/index.tsx`](../mobile/crush_mobile/app/index.tsx)

## 复现目标

验证一条完整链路：

1. 新开一个 `crush-test` TUI 会话。
2. 通过 relay 把 session / message / agent_event 送到 NATS。
3. mobile 端通过 websocket 订阅并展示会话列表。
4. mobile 端发任务后，TUI 真正执行。
5. 执行结束后，busy 状态能收回，UI 不应持续转圈。

## 推荐测试流程

### 1. 启动服务端

确认远端 NATS 在线，至少要能看到：

```bash
curl -s http://47.110.255.240:8222/connz?subs=1 | jq '.connections | length'
```

### 2. 启动新 TUI

```bash
crush-test
```

如果命令不存在，先安装 launcher：

```bash
task build
```

如果本机没有 `task`，就直接执行仓库里的 `scripts/launch_crush_test.sh` 或手工 `go build` 后安装到 `~/.local/bin/crush-test`。

如果要自动化，直接使用：

```bash
./acceptance/scenarios/relay_mobile_joint.sh
```

### 3. 连接 mobile

用 deep link 指向 NATS websocket：

```text
crushmobile://connect?serverUrl=ws%3A%2F%2F47.110.255.240%3A8443
```

### 4. 发一个最短任务

建议先用 ASCII 指令：

```text
run date
```

或者：

```text
echo hello
```

## 通过判据

- mobile 顶栏状态变成 `在线`。
- 会话列表里出现 `_cli_bases/crush` 一类的人类可读标签，而不是内部 session id。
- TUI 侧出现真实输出。
- mobile 侧 busy / loading 状态在 agent 完成后清掉，不长期转圈。
- `acceptance/artifacts/relay_mobile_joint/connz.json` 里能看到 `crush-mobile` 和至少一个 `crush-tui-*` 连接。

## 常见失败点

### 1. mobile 还停在旧地址

症状：显示 `ws://192.168.0.104:9092` 之类的旧值。

处理：

- 用 deep link 重新冷启动。
- 不要依赖手工输入 URL，Android 输入法会污染 `://`。

### 2. 一直转圈

症状：TUI 已经返回了结果，但 mobile 仍显示 `Agent 正在处理中...`。

处理：

- 检查 relay 是否发出了 `agent_finished`。
- 检查 mobile 是否把 `agent_finished` 视为 idle。
- 看 `agent_event` 是否只发了开始事件，没有发结束事件。

### 3. 会话列表为空

处理：

- 先看 `CRUSH_SESSIONS` KV 是否有记录。
- 再看 `connz` 里是否存在 `crush-tui-*`。
- 如果 NATS 有连接但没有 session 记录，问题在 relay 的 presence 写入。

## 交接时必须留的证据

- `acceptance/artifacts/relay_mobile_joint/run.log`
- `acceptance/artifacts/relay_mobile_joint/connz.json`
- `~/.local/state/crush-dev/trace-*.jsonl`
- 最新的 mobile 截图

## 结论

这条联调链路的优先级不是“再做一个 mock”，而是“真实 TUI + 真实 NATS + 真实 mobile”。
只要 busy 收尾事件和 session presence 正常，mobile 上的会话和状态就应该能闭环。
