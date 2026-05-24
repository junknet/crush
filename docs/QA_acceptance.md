# Crush 验收测试文档

> 适用范围：第一、二、三波 + 后续修补全部上线后的端到端验收
> 目标读者：测试同学
> 最后更新：2026-05-23

---

## 0. 测试前置环境

### 0.1 软件版本与 binary

| 组件 | 路径 / 版本 | 说明 |
|:---|:---|:---|
| Crush TUI binary | `~/.cache/crush-prod/crush` (md5 `62dabe57…` 或更新) | 由 `task build` 生成，被 `crush` / `crush-dev` launcher 调用 |
| launcher 脚本 | `~/.local/bin/crush` (生产)，`~/.local/bin/crush-dev` (带 debug + trace) | 两者最终指向同一 binary，仅 env 不同 |
| Mobile APK | `com.junknet.crushmobile` debug build (170 MB 包含 embedded bundle) | adb 安装到 Android 设备 |
| 远程 NATS | `nats://47.110.255.240:4222` token `ymm_rpc_2026`, ws `8443`, monitor `8222` | 公网 supervisord 守护，三家测试都连这一套 |
| LLM Provider | Claude Sonnet 4.6 (Max OAuth)、WeCode GPT 5.4 Mini、Gemini 2.5 Pro | 至少跑通 Claude + WeCode 两家 |

### 0.2 启动方式

**TUI（必须挂 relay）**：

```bash
export CRUSH_RELAY_NATS_URL=nats://47.110.255.240:4222
export CRUSH_RELAY_TOKEN=ymm_rpc_2026

cd <要测试的工程目录>
crush-dev          # debug 日志 + trace JSONL
# 或
crush              # 生产 launcher
```

dev 模式日志：`~/.local/state/crush-dev/trace-*.jsonl`、HTTP body dump `~/.local/state/crush-dev/http-*/`

**Mobile**：

```bash
adb install -r app-debug.apk      # 首次/更新时
adb shell am force-stop com.junknet.crushmobile
adb shell am start -n com.junknet.crushmobile/.MainActivity
```

Metro bundler 可选（dev menu/JS 调试时启）：
```bash
cd mobile/crush_mobile && bun run start --dev-client
adb reverse tcp:8081 tcp:8081
```

### 0.3 测试同学需要会的辅助命令

```bash
# 看远程 NATS 上的活动连接（TUI sessions 数）
curl -s 'http://47.110.255.240:8222/connz?subs=1' | jq '.connections | map(select(.name | startswith("crush-tui-"))) | length'

# 看 NATS 上的 session 元数据（presence + model）
go run /tmp/crush-e2e-test/natsclient/kvdump.go

# 看 mobile logcat 关键事件
adb logcat -d | grep -E "CrushApi|nats|reconcile"

# 手机截图
adb exec-out screencap -p > /tmp/shot.png
```

---

## 1. 测试矩阵总览

| 模块 | 用例编号 | 类型 |
|:---|:---|:---|
| Prompt cache + Dynamic prefix | TC-01..04 | 性能 + 可观测 |
| Brain → Explore 子代理 | TC-05 | 行为验证 |
| Bash 长输出落盘 + 拦截 | TC-06..07 | I/O |
| microCompact 工具结果清理 | TC-08 | 上下文管理 |
| memdir 注入 | TC-09 | 自动建目录 |
| Edit 三层归一化 | TC-10..12 | 鲁棒性 |
| Ghost text 自动补全 | TC-13 | 交互 |
| ToolSearch + deferred MCP | TC-14 | 渐进装载 |
| 持久 Cron + 事件队列 | TC-15 | 后台 |
| Mobile UI 基础 | TC-16..18 | UI |
| Mobile↔TUI 双向同步 | TC-19..23 | E2E |
| Mobile model picker → state.yaml | TC-24 | 配置同步 |
| Session lifecycle (上线/掉线/重连) | TC-25..28 | 健壮性 |
| 抽屉二级折叠 + 自动追踪 | TC-29..30 | UI |

### 通过标准

- 每个用例 **Result** 列出的"必须满足"的判定全部通过 → PASS
- 任何一条 ❌ → FAIL，记录截图/日志路径
- 性能类（cache 命中率 / 落盘速度）有数值阈值，未达 → FAIL

---

## 2. 详细用例

### TC-01 — LLM cache 实战命中（Anthropic）

**目的**：验证 M1 DYNAMIC BOUNDARY + M2 ephemeral cache 在 Anthropic 上真命中。

**步骤**：
1. `crush-dev` 启动，brain 用 Claude Sonnet 4.6
2. 连续发 5 轮无关 prompt（如「你好」「介绍 Go 语言」「写个 hello world」「列出 5 个城市」「再见」），每轮等 assistant 回复完
3. 查 trace：`tail -100 ~/.local/state/crush-dev/trace-*.jsonl | jq 'select(.cache_read_input_tokens or .cache_creation_input_tokens) | {turn:.turn_idx, read:.cache_read_input_tokens, create:.cache_creation_input_tokens, ratio:.cache_hit_ratio, provider:.cache_provider}'`
4. 或查 HTTP dump：`grep -oE '"cache_read_input_tokens":[0-9]+' ~/.local/state/crush-dev/http-*/anthropic*.jsonl | sort | uniq -c`

**Result**：
- ✅ 第 1 轮：`cache_read_input_tokens = 0`，`cache_creation_input_tokens > 5000`
- ✅ 第 3 轮起：`cache_read_input_tokens > 0` 且 `cache_hit_ratio ≥ 0.5`
- ✅ 第 5 轮：`cache_read_input_tokens > 20000`，命中率 `≥ 0.6`
- ❌ 全程命中率为 0 / cache_read 永远 0 → DYNAMIC BOUNDARY 破了

---

### TC-02 — LLM cache 命中（OpenAI / Codex）

**目的**：M2 三家统一抽象的 OpenAI 路径。

**步骤**：
1. 在 mobile 用 picker 把 brain 切到 `wecode / gpt-5.4-mini`（见 TC-24）
2. 同 session 跑 5 轮 prompt
3. 查 HTTP dump 中 OpenAI 响应：
   ```bash
   grep -oE '"cached_tokens":[0-9]+' ~/.local/state/crush-dev/http-*/openai*.jsonl
   ```
4. 验证请求体含 `"prompt_cache_key": "<sessionID>"`

**Result**：
- ✅ 请求 body 有 `prompt_cache_key`
- ✅ 第 2 轮起 `cached_tokens > 0`，且 `cached_tokens / prompt_tokens > 0.3`
- ❌ `prompt_cache_key` 缺失 / `cached_tokens` 永远 0 → fantasy 注入未生效

---

### TC-03 — DYNAMIC env_dynamic 注入位置

**目的**：确认 date/git status 没污染 cache prefix。

**步骤**：
1. `crush-dev` 启动后发任意 prompt
2. 查 HTTP dump 第一条 anthropic 请求 body：
   ```bash
   jq '.request.body' ~/.local/state/crush-dev/http-*/anthropic*.jsonl | head -100
   ```

**Result**：
- ✅ 第一个 user content text part：含 `<env_dynamic>\nToday's date: ...\n</env_dynamic>\n\n---\n<用户 prompt>` 结构
- ✅ system prompt 体内**不**含 `Today's date` 字样
- ❌ system 头部含日期/git 状态 → cache 永远 miss

---

### TC-04 — Brain 决策表生效

**目的**：M3 验证 brain.md.tpl 的 if-then 表实际让 Claude 派 explore。

**步骤**：
1. 在一个有较多文件的工程下（如 crush 本身）启动 `crush-dev`
2. 发 prompt：`这个 codebase 里有哪些 nim_* tool？它们在哪定义？`（典型跨多文件查询）
3. 看 trace JSONL 第一轮工具调用

**Result**：
- ✅ trace 中首轮 tool_call 是 `agent` (role=explore) 而非 brain 自己一连串 `view`/`grep`
- ❌ brain 自己串 5+ 次 view/grep → 决策表没生效

---

### TC-05 — Bash 大输出 spill 落盘

**目的**：M4 验证保头丢尾 + spill。

**步骤**：
1. TUI 内发 prompt：`用 bash 跑 seq 1 20000`
2. 看 mobile 上 bash 卡片提示「完整输出在 spill 文件」
3. 在 TUI 工作目录下：
   ```bash
   ls .crush/tool-results/<sessionID>/bash-*.log
   wc -l .crush/tool-results/<sessionID>/bash-*.log
   ```

**Result**：
- ✅ `bash-*.log` 文件存在
- ✅ `wc -l` 输出 **20000**（全量原始内容）
- ✅ TUI / mobile 屏幕显示的内容含 `output_spill bytes=N path=...`
- ❌ 文件 size 远小于 20000 行 → spill 不全
- ❌ 没生成 spill 文件 → M4 未生效

---

### TC-06 — 拦截 `tail -f` / `watch`

**步骤**：
1. TUI 发：`用 bash 跑 tail -f /etc/hosts`
2. TUI 发：`用 bash 跑 watch -n 1 date`

**Result**：
- ✅ 两次都被拦：assistant 收到错误「command is not allowed」之类
- ✅ TUI 不卡死

---

### TC-07 — bash 后台 + monitor

**步骤**：
1. 发：`background bash 跑 "for i in {1..20}; do echo line$i; sleep 1; done"`
2. 假设它返回 background job id `001`
3. 发：`用 monitor 工具监听 shell 001，pattern: "line5"`

**Result**：
- ✅ Bash 工具 metadata 含 `Background: true` + `ShellID`
- ✅ monitor 在大约 5 秒后命中 pattern 唤醒，返回 `matched: line5`
- ✅ 后续可用 job_output 看完整后续输出

---

### TC-08 — microCompact 长任务工具结果清理

**目的**：M5 验证 long-running 任务工具结果按时清理。

**步骤**：
1. TUI 内连发 4 次：`用 view 工具读 /usr/share/dict/words`（大文件，每次工具结果 >50KB）
2. 等约 6 分钟（超过 microCompactIdleDuration 5 分钟）
3. 再发任意 prompt（触发 OnStepFinish）
4. 查 dev log：
   ```bash
   grep "MicroCompact: rewrote tool results" ~/.local/state/crush-dev/*.log
   ```

**Result**：
- ✅ 日志中出现至少 1 条 `MicroCompact: rewrote ... cleared_bytes=...`
- ✅ session 的旧 view 结果被替换为 `[Tool result cleared by microCompact — original N bytes, spill at <path>]`
- ✅ 对应路径下 `.crush/tool-results/<sess>/micro-*-*.log` 存在
- ❌ 6+ 分钟仍无 MicroCompact 日志 → 触发逻辑没生效

---

### TC-09 — memdir 自动初始化

**目的**：M7 首次进 cwd 自动建 memdir 目录。

**步骤**：
1. 在一个**新目录** `/tmp/qa-fresh-$(date +%s)` 启动 `crush-dev`
2. 查 mounting：
   ```bash
   ls .crush/projects/*/memory/MEMORY.md
   cat .crush/projects/*/memory/MEMORY.md | head -3
   ```

**Result**：
- ✅ `MEMORY.md` 自动创建
- ✅ 文件以 `<!-- MEMORY.md — cross-session index ... -->` 开头
- ✅ 子目录路径形如 `.crush/projects/qa-fresh-<时间戳>-<8 字符 hash>/memory/MEMORY.md`

---

### TC-10 — Edit old_string 不匹配时友好诊断

**目的**：F1 验证错误信息含 fuzzy hit 提示。

**步骤**：
1. TUI 读一个 .go 文件，记住某一行（如有 4 空格缩进的某行）
2. 发 prompt 让 LLM edit 一个**故意带错误缩进的 old_string**（如要求把 4 空格改 2 空格）

**Result**：
- ✅ assistant 收到的工具错误含「`Diagnostic: a similar line exists ... near line N`」+ 文件 excerpt（用 `·` 标空格、`→` 标 tab、`¶` 标行尾）
- ❌ 错误信息只有 "old_string not found" → F1 没生效

---

### TC-11 — Edit 引号归一化

**目的**：F3 验证直引号 ↔ 弯引号自动匹配。

**步骤**：
1. 准备一个文件含 `say "hello"`（直引号）
2. 让 LLM edit：`把 "say "hello"" 改成 "say "hi""`（直引号 oldString）
3. 再准备一个文件含 `say "hello"`（弯引号 “”）
4. 让 LLM 用直引号 oldString edit 它

**Result**：
- ✅ 两种情况都能成功 edit
- ✅ 弯引号文件 edit 后**仍保留弯引号风格**（不被改成直引号）

---

### TC-12 — Edit 尾随空格自动 strip

**目的**：F3 stripTrailingWhitespace 验证。

**步骤**：
1. 让 LLM 用 edit 给 `.go` 文件加一行末尾带 trailing spaces 的代码
2. 让 LLM 给 `.md` 文件同样操作

**Result**：
- ✅ `.go` 文件：末尾空格被 strip
- ✅ `.md` 文件：末尾空格保留（markdown 双空格硬换行语义需保留）

---

### TC-13 — Ghost text 自动补全

**目的**：F4 完整链路（生成 → 渲染 → Tab 接受）。

**步骤**：
1. TUI 内发个简单 prompt（如 `你好`）
2. 等 assistant 回复完毕（出现 `Ready?` 提示符）
3. **保持光标在输入区**，等 1-3 秒
4. 观察光标后是否出现**灰色 dim 字**（ghost text）
5. 按 `Tab` 或 `Right Arrow`

**Result**：
- ✅ 回复后 1-3s 出现灰色补全字（2-12 字）
- ✅ 按 Tab → 灰字入 textarea 变正常颜色
- ✅ 看 log：`grep "Suggestion shown\|Suggestion accepted" ~/.local/state/crush-dev/*.log`
- ❌ 无灰字出现 → F4 未触发（看是不是 thin history / non-interactive）

---

### TC-14 — ToolSearch + deferred MCP

**目的**：F5 验证 MCP 工具默认 deferred + tool_search 按需展开。

**前置**：crush.yaml 配至少 1 个 MCP server（如 ptc-foreman）。

**步骤**：
1. `crush-dev` 启动
2. 查 HTTP dump 第一条 Anthropic 请求体：
   ```bash
   jq '.request.body.system' ~/.local/state/crush-dev/http-*/anthropic*.jsonl | head -50
   ```
3. 应能看到 `<system-reminder>The following deferred tools are now available via tool_search...`
4. 发 prompt：`用 tool_search 工具搜 ptc 相关的工具，然后用其中一个`

**Result**：
- ✅ system 含 deferred 列表（MCP 工具名 + 简短描述，无 schema）
- ✅ assistant 第一步用 `tool_search query: select:<name>` 或关键词搜
- ✅ 之后能成功调用实际 MCP 工具

---

### TC-15 — 持久 Cron 表达式

**目的**：F6 验证 cron + 落盘 + 触发。

**步骤**：
1. TUI 发：`用 schedule_wakeup 工具，cron_expression 每分钟，payload "qa cron test"`
2. 查文件：`cat .crush/scheduled_tasks.json`
3. 等 1-2 分钟看是否触发

**Result**：
- ✅ `scheduled_tasks.json` 含新任务（含 cron / next_fire_at / id）
- ✅ 整点附近 ±90s jitter 内被触发
- ✅ 触发后 mobile / TUI 都收到 `<task-notification>` system-reminder
- ✅ recurring task 7 天后自动从 json 中删除

---

### TC-16 — Mobile 启动 + 自动连 NATS

**步骤**：
1. force-stop 后启动 mobile app
2. 等 5 秒

**Result**：
- ✅ 顶部状态：绿点 + "在线"
- ✅ logcat：`[CrushApi] connecting to ws://47.110.255.240:8443` + `[CrushApi] connected`
- ✅ session 列表至少 1 条（如果远程已有跑着的 TUI）

---

### TC-17 — Mobile 抽屉 cwd 二级折叠

**目的**：F8 验证 grouping + 折叠。

**前置**：在 2 个不同 cwd 各启 1 个 `crush-dev`，其中 1 个 cwd 起 2 个 session。

**步骤**：
1. 点击左上汉堡打开抽屉
2. 观察会话列表区

**Result**：
- ✅ 每个 cwd 一个 header 行：含 chevron-down + folder icon + 短路径 + `alive/total` (如 `2/2`、`1/1`)
- ✅ 同 cwd 的 session 缩进显示在 header 下
- ✅ 点击 header → chevron 变 chevron-right + session 隐藏（折叠）
- ✅ active session 所在的 header 高亮蓝色
- ✅ alive 数 < total 时（有 dead 残留），header 数字正确（如 `1/3`）

---

### TC-18 — Mobile model badge 显示当前 model

**目的**：#19 紫色 CPU chip。

**步骤**：
1. 启动 mobile，等连接
2. 选中任一 alive session
3. 看顶部 cwd 行右侧第二个 chip（紫色 CPU 图标）

**Result**：
- ✅ 显示具体 model id（如 `claude-sonnet-4-6` 或 `gpt-5.4-mini`）
- ✅ session 切换后 chip 文案跟着变
- ❌ 持续显示 `未就绪` → presence 没带 provider/model（看 NATS KV）

---

### TC-19 — Mobile → TUI 发送 prompt

**步骤**：
1. mobile 输入「list current go files」
2. 点蓝色 send 按钮

**Result**：
- ✅ 用户气泡 `list current go files` 出现在 mobile
- ✅ 状态条 `未就绪` → `运行中`
- ✅ send 按钮变红色方块 stop
- ✅ TUI 同步显示新用户 prompt + agent 开始思考
- ✅ 几秒后 mobile + TUI 都收到 assistant 回复
- ✅ 完成后 stop 按钮还原为 send

---

### TC-20 — Mobile send → stop 按钮转换

**步骤**：
1. mobile 发个长 prompt（如「写 500 字故事」）
2. 等 agent 开始 thinking（约 1s）
3. 点红色 stop 按钮

**Result**：
- ✅ Thinking 中 send 按钮变红色方块
- ✅ 点 stop → TUI agent 立刻取消（TUI 日志: `context canceled`）
- ✅ mobile 状态条回 `未就绪`，stop 还原 send

---

### TC-21 — TUI ESC 暂停 → mobile 同步

**步骤**：
1. mobile 或 TUI 发个长 prompt，让 agent 跑起来
2. TUI 内按 **ESC** 键

**Result**：
- ✅ TUI 顶部 spinner 消失 + 出现 `ERROR context canceled` 行
- ✅ mobile 状态条同步从 `运行中` 回 `未就绪`
- ✅ mobile 输入按钮回 send

---

### TC-22 — TUI 退出 → mobile 同步 offline

**目的**：#16 验证 alive 状态正确反映。

**步骤**：
1. 在 mobile 抽屉确认某个 cwd 有个 alive session
2. 在 TUI 里 Ctrl-C 关掉那个 session（或 `tmux kill-session`）
3. 等 13 秒 → 看 mobile
4. 再等 8 秒（总共 ~21s）→ 看 mobile

**Result**：
- ✅ 13s 内：mobile 该 session 圆点变**红色**（alive=false 但 entry 还在）
- ✅ 21s 后：该 session 从抽屉**完全消失**（NATS KV TTL 15s expire + 8s reconcile）
- ✅ 抽屉 cwd group 的 alive 计数自动减 1（如 `3/3` → `2/2`）

---

### TC-23 — Mobile 自动追最新 alive session

**目的**：#20 验证 mobile 不卡在 dead session。

**步骤**：
1. mobile 选中 session A
2. TUI 关 session A
3. 在同样或不同 cwd 起新 session B（带不同 prompt 历史）
4. 等 reconcile 周期（约 13s）

**Result**：
- ✅ mobile 自动从 session A 跳到 session B（最新 alive，按 updated_at desc）
- ✅ 顶部 cwd badge 变成 B 的 cwd
- ✅ 不需要用户手动开抽屉切换
- ❌ mobile 卡在 dead session A 上 → #20 没生效

---

### TC-24 — Mobile model picker → state.yaml 持久化

**目的**：#17 验证整链路。

**步骤**：
1. mobile 点右上 ⚙ 滑块图标
2. 展开 model picker 区域
3. 选 role: `brain`
4. **重要**：切到英文输入法（长按输入框选 English keyboard）避免中文转换
5. provider 输入：`wecode`
6. model 输入：`gpt-5.4-mini`
7. 点「应用到 brain」按钮
8. 在 PC 上：
   ```bash
   cat ~/.config/crush/state.yaml | grep -A4 'models:'
   ```

**Result**：
- ✅ 按钮文案 `应用到 brain` → `保存中…` → `已写入 state.yaml ✓`
- ✅ state.yaml 显示：
  ```yaml
  models:
      brain:
          model: gpt-5.4-mini
          provider: wecode
  ```
- ✅ TUI 日志: `Relay set_model applied role=brain provider=wecode model=gpt-5.4-mini`
- ✅ 等 5s 后 mobile 顶部 model chip 变 `gpt-5.4-mini`（presence 周期同步）
- ✅ TUI 下次新 prompt 用 wecode/gpt-5.4-mini（看 HTTP dump host 变 `api.wecodemaster.com`）

---

### TC-25 — Mobile 掉线/重连 UI 反馈

**步骤**：
1. 启动 mobile + TUI 正常连通
2. 让手机进飞行模式 5 秒
3. 取消飞行模式

**Result**：
- ✅ 掉线时顶部状态：红点 + `连接中` 文案
- ✅ placeholder 变 `请先连接 Crush`
- ✅ send 按钮 disabled
- ✅ 网络恢复后约 5s 内：绿点 + `在线`，输入恢复
- ✅ 历史消息没丢（自动重新拉 events）

---

### TC-26 — 历史加载（切 session 看以前消息）

**步骤**：
1. 抽屉选一个**有历史**的 session（如 8 小时前那个）
2. 主屏滚动看历史

**Result**：
- ✅ 用户气泡 + agent 回复完整加载
- ✅ 含 `<env_dynamic>Today's date: ...</env_dynamic>` 头部（M1 注入）
- ✅ bash 工具卡片完整（如 `crush〉date` + 输出）
- ✅ 加载在 2-3 秒内完成（不卡白屏）

---

### TC-27 — 多设备同时观察一个 session

**步骤**（如果有 2 台手机）：
1. 两台手机连同 NATS、选同一个 session
2. 在 TUI 发 prompt
3. 在某一台手机发 prompt

**Result**：
- ✅ 两台手机和 TUI 屏幕**实时同步**（< 1s 延迟）
- ✅ 任一端发的 prompt 都在所有端显示

---

### TC-28 — 抽屉 alive count 实时更新

**步骤**：
1. 抽屉打开，看某 cwd `2/2`（假设两个 alive）
2. 不关 mobile，去 TUI Ctrl-C 关一个
3. 等 5-15s 看抽屉数

**Result**：
- ✅ 13s 内 alive count 变 `1/2`（红 dot offline）
- ✅ 20s 内变 `1/1`（dead session 移除）

---

### TC-29 — Bash 工具用 sleep（不该被拦）

**步骤**：
1. TUI 发：`用 bash 跑 "sleep 2 && echo done"`

**Result**：
- ✅ 命令正常执行，约 2s 后返回 `done`
- ❌ 被 "command not allowed" 拦了 → 我们前一波拦 sleep 太激进了，已回退，验证回退到位

---

### TC-30 — 子代理（explore role）只读

**目的**：M3 + free-code 一致的子代理工具集。

**步骤**：
1. 让 TUI 派 explore：`用 explore 子代理找出所有 .nim 文件里包含 "macro" 的函数`
2. 让 explore 主动尝试编辑文件：`用 explore 子代理修改 README.md 加一行`

**Result**：
- ✅ 第一种成功（read-only 工具集够用）
- ✅ 第二种 explore 回复说做不了 / 让父 agent 用 worker 处理（explore 没 edit / write 权限）

---

## 3. 已知 Gap（不在本轮范围）

| Gap | 状态 | 描述 |
|:---|:---|:---|
| Notification / PostToolUse / Stop hooks | 推迟 | 仅 PreToolUse 在用 |
| extractMemories 后台 agent | 推迟 | 用户已有 ~/.claude memdir，Crush 端仅做注入读 |
| Task* 工具套件持久化 + DAG | 推迟 | 当前用 todos.go 够 |
| outputStyles 三套预设 | 不做 | 独狼场景 ROI 不足 |

---

## 4. 提交 bug 模板

发现失败用例时按下面格式提：

```
- 用例 ID: TC-XX
- 现象: <一句话描述>
- 复现步骤: <精确到点击/键入序列>
- 实际结果: <粘贴 log / 截图>
- 期望结果: <参考文档对应 Result>
- 环境:
    - TUI binary md5: <`md5sum ~/.cache/crush-prod/crush`>
    - Mobile APK 安装时间: <adb dumpsys package com.junknet.crushmobile | grep firstInstallTime>
    - NATS server reachable: <`curl -m 3 http://47.110.255.240:8222/varz` 返回 200 即可>
- 截图/log 路径: <PR 里附上>
```

---

## 5. 性能基线（参考）

| 指标 | 期望 |
|:---|:---|
| Anthropic cache 命中率（第 3 轮起） | ≥ 0.6 |
| OpenAI cached_tokens 比例（第 2 轮起） | ≥ 0.3 |
| Bash spill 落盘时间 (20K 行) | < 200ms |
| Ghost text 生成延迟 (assistant 回复后) | < 3s |
| Mobile UI 渲染 prompt → 用户气泡 | < 300ms |
| Mobile↔TUI 同步延迟 | < 1s |
| Session offline → 红 dot | 12-13s |
| Dead session 从抽屉移除 | 20-21s |
| Set_model → state.yaml 写入 | < 500ms |

---

## 6. 联系人

测试中遇到环境问题 → 找开发同学；功能层面 bug 用上面模板提交。
