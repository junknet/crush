# 任务候选评估 — Nim 数值计算 bug 修复(transcript 5e87bd7e)

> 评估目标:这条 claude transcript 描述的"Nim 数值计算 bug 修复"能否作为 crush
> worker agent 端到端闭环评测的硬任务。
>
> 结论先行:**不可行 —— 因为 transcript 里根本不存在"数值计算 bug 被定位并修复"
> 这件事**。原始任务实质是一次"nim-core 手搓 parquet 读写计算 vs Rust Arrow 性能
> 对比"的 benchmark 工程,正确性从一开始就是 byte-equal 通过的(16/16 列 0 偏差)。
> 没有 bug → 没有 fix diff → 没有"修好/没修好"的红绿 oracle。
>
> 详见第 5 节裁决。第 2 节给出一个**替代候选**(把同一素材改造成"性能优化任务"),
> 但那是另一类任务,且仍受重环境耦合制约。

来源 transcript:`~/.claude/projects/-home-junknet-linege-nim-core/5e87bd7e-283c-4d51-b9ca-5beed8398ae9.jsonl`(2757 行,6.4 MB)。

---

## 1. 任务实质(逐 turn 还原)

### 1.1 首个用户消息的真实意图

> "当前这个 和 /home/junknet/Desktop/nimQuant-sdk 计算貌似有问题 我手搓的 parent
> 的读写计算什么的。/home/junknet/linege/a_stock tirck 可以当作计算的测试数据,
> feature/ 什么的你看看整体情况"(row 2)

这句话信号模糊。assistant(row 22-27)主动用 `AskUserQuestion` 追问"聚焦哪一块 /
具体 wrong number 在哪一列"。**用户的回答(row 26 上下文 + row 34/47/49)把意图明确
改写成了性能问题**:

> "现在的问题是 我们 nim 手搓的 parquet 的读写太差 不知道问题,rust arrow 读写且
> 计算作为对比的 benchmark 很合理,我就想知道到底慢了多少"(row 34/44)
>
> "用一个 time bar 计算 1 天完整的 tick 的 1min bar 特征作为任务场景 …… 时间、性能、
> 内存占用、cpu 消耗、计算写入写出、正确性全面对比,最强的性能对比,找问题"(row 47/49)

即:**"计算貌似有问题"中的"问题"指的是性能差(慢),不是数值错(算错)**。"正确性"
只是性能对比里附带的一个 sanity 校验维度。

### 1.2 实际执行的工作(row 89-299)

assistant 自主完成了一套完整 benchmark 工程,产物落在
`/home/junknet/linege/nim-core/bench/parquet_vs_arrow/`(**该目录现在仍完整存在**):

| 文件 | 内容 |
|---|---|
| `TASK.md` | benchmark 任务契约(18 个聚合列语义) |
| `python_oracle/preprocess.py` | 把 a_stock raw polars parquet 预处理成"基准规范输入"(REQUIRED 列、int32 asset_id、ZSTD/无压缩两版) |
| `rust_arrow/src/main.rs` | Rust arrow-rs 单线程标量基线 |
| `rust_arrow_peak/` | DataFusion 多线程极限实现 |
| `nim_core/trade_bar_bench.nim` | nim-core 手搓 parquet read→compute→write |
| `nim_core_peak/` | nim 无压缩对照版 |
| `run.sh` / `run_uncompressed.sh` | 3 次取中位数、抓 wall/cpu/RSS/IO 三段 |
| `python_oracle/diff.py` | rust 输出 vs nim 输出逐列字节级 diff |
| `RESULTS.md` | 最终报告 |

计算场景:`a_stock/tick/trade/date=20250221/data.parquet`(1.3 GB raw / 预处理后
108.99M 行)→ per-(asset, session) 1min 分桶 → 18 个聚合(OHLC / VWAP / 主动买卖
分解 / ratio)。

### 1.3 涉及的 nim-core 文件

调研/使用(非修改)了 nim-core parquet facade:
`src/infra/encoding/parquet/api.nim`、`writer.nim`、`encoding/byte_array.nim`、
`metadata.nim`、`codec/`、`encoding/`。assistant 发现高层 `api.nim` 不支持
BYTE_ARRAY/string 和 NULL(`readColumnRaw` 仅支持全 present 列),据此把基准设计成
"REQUIRED 列预处理"以绕开 facade 缺口 —— 这是**功能缺口的记录**,不是 bug 修复。

### 1.4 关键结论:没有任何"数值 bug"被定位或修复

- row 206:首次对比即报 **`rows_kept 与 groups 完全一致(107,990,743 / 747,586)
  → 计算逻辑等价`**。
- row 267:**"Bit-exact 一致:16 列 × 747,586 行,零误差。Nim 和 Rust 的输出完全
  字节级等价"**。
- 最终 `RESULTS.md`:nim-core 正确性 = byte-equal,**16/16 列 0 偏差**;唯一差距是
  性能(Nim wall 7.74s vs Rust 5.11s,慢 1.51×;Peak RSS 7.15GB vs 3.76GB,1.90×)。

整条 session 在 row 918 之后**彻底转题**到 nim3/Nimony 私有编译器开发、formal spec、
C/Rust FFI 库接入,与 a_stock 数值计算再无关系。所谓"transcript 末尾的最终修复
diff / commit"不存在 —— 因为根本没有 bug fix 这条主线。

---

## 2. oracle 可机判性

### 2.1 如果当作"数值 bug 修复"任务:无法构造 oracle

机判 oracle 的前提是"存在一个会失败的初始状态 + 一个修好后会通过的终态"。本素材里
**初始状态就是通过的**(byte-equal),无法构造"红→绿"判定。除非人为往 nim-core
parquet 计算里注入一个 bug 作为起点 —— 但那就不是"真实历史任务"了,违反评测设计初衷。

### 2.2 客观 oracle 素材本身是存在的(为替代任务铺垫)

环境里确有可机判的数值对拍设施,质量很高:

1. **独立 Python oracle**:`/home/junknet/linege/a_stock/tools/audit_bar_python_oracle.py`
   —— 用 polars 从 raw tick 复刻 build.nim 的 SQL 逻辑,**不调用 DuckDB**,三层对拍:
   - L1 严格 byte-equal:1 日 × 3 抽检 asset,逐行逐列,浮点容差 `eps=1e-9`
   - L2 分布对齐:全市场每列 mean/p1/p50/p99 偏差 `< 1e-6`
   - L3 跨日 sanity:抽样 10 日验时序
   - 用法:`python3 audit_bar_python_oracle.py --date 20250221 --level all`
2. **历史正确产物**:`a_stock/feature/bar/time_1min_trade/date=20250221/data.parquet`
   (48 MB)—— 现成的"已知正确基准",可做字段级 diff 终点。
3. **bench 内自对拍**:`bench/parquet_vs_arrow/python_oracle/diff.py`,按
   (asset_id, session, bar_id) join 后逐列 diff,容差判红绿,已可重复运行。

也就是说:**oracle 设施是绿的,但没有对应的 bug**。这正是它不能当 bug-fix 任务的原因。

### 2.3 可机判但属于"性能优化"类的替代 oracle

若把任务改写为"把 nim-core 手搓 parquet 的 1min bar pipeline 性能提到接近 Rust
单线程基线",可以构造可重复命令式 oracle:

```bash
# 正确性 gate(必须保持绿):
python3 bench/parquet_vs_arrow/python_oracle/diff.py \
    data/out_rust_scalar.parquet data/out_nim.parquet   # 退出码 0 = byte-equal
# 性能 gate(优化目标):
./bench/parquet_vs_arrow/run.sh                          # 取 nim wall 中位数
#   红/绿阈值例:nim_wall / rust_wall < 1.2  (当前 1.51)
```

但这是"性能任务",不是"数值 bug 任务",评测语义完全不同(见第 4 节)。

---

## 3. 隔离可行性(逐项)

| 维度 | 事实 | 隔离副本可行性 |
|---|---|---|
| **nim-core 副本大小** | `du -sh` = **5.5 GB**(含 `.git` 92M、`nimcache` 2.7M;主体是 vendor `.a`/二进制) | `cp -r` 一份 5.5G,单机 62G RAM/磁盘可承受但**单次成本偏重**;多 worker 并行会放大 |
| **a_stock 输入数据** | 单日 `tick/trade/date=20250221/data.parquet` = **1.3 GB**;整个 `tick/trade` = **392 GB** | 只读引用即可,**不进副本**;但 worker 需要这条绝对路径可达 |
| **历史参考产物** | `feature/bar/time_1min_trade/date=20250221/data.parquet` = 48 MB | 只读引用,不进副本 |
| **Nim 编译器** | **私有 fork**:`~/.nim-profile.env` → `/home/junknet/linege/nim-src/Nim/bin/nim`,经 `~/.local/bin/nim` wrapper 注入 | **副本外强耦合**:wrapper 读全局 `~/.nim-profile.env`,编译器在 `nim-src`(另一个 5G+ 仓),**不会随 nim-core 副本复制**;副本能否编译完全依赖宿主全局环境完好 |
| **env.sh** | 自发现仓根(`BASH_SOURCE` 解析),source 副本自己的 `env.sh` 会正确重定位 `NIM_CORE_PATH` 到副本 | ✅ 可重定位 |
| **NIMCACHE / ccache** | env.sh 把 `NIMCACHE_ROOT` / `CCACHE_DIR` 指向 `~/.cache/*` 共享桶,wrapper 按 project/profile 分桶并加锁 | ⚠️ **共享状态**:多个隔离副本会争用同一 ccache/nimcache 锁,可能串扰;需要 per-worker 覆写 `NIMCACHE_ROOT` |
| **Rust / DataFusion 工具链** | benchmark 需要 `cargo build --release`(arrow-rs + parquet + DataFusion crate) | crate 下载/编译依赖宿主 cargo + 网络/cargo cache;副本里首次 build 会拉 crate,**非密闭** |
| **Python oracle 依赖** | `polars` / `pyarrow` | 依赖宿主 Python 环境;a_stock 路径在 oracle 里**硬编码** `/home/junknet/linege/a_stock` |
| **bench 源码绝对路径** | nim/py bench 源码本身**不硬编码** a_stock 路径(走命令行参数);仅 `run.sh`/`RESULTS.md`/oracle 把路径写在调用处 | ✅ 计算代码可参数化;⚠️ 调用脚本和 oracle 含绝对路径 |
| **内存** | 峰值 RSS Nim 7.15 GB / Rust 3.76 GB;宿主 62G(可用 38G) | ✅ 单 worker 够;多 worker 并行跑 108M 行计算会吃紧 |

**核心耦合风险**:worker 在 `cp -r` 副本里要跑通"编译 + 对拍",硬依赖三个副本外的
全局资源 —— ① 私有 Nim 编译器(`nim-src`,5G+,不随副本走)、② `~/.nim-profile.env`
全局 profile、③ a_stock 真数据(392G 主仓,只读引用)。其中 ① ② 是**绝对路径/全局
环境耦合**:只要宿主这套 toolchain 在,副本能编译;一旦想把副本搬到干净沙箱/CI/另一台
机器,会因为缺私有编译器和 profile 直接编译失败。这不是"纯净可移植任务",是"宿主
环境内的隔离副本"。

---

## 4. 难度与分离度评估

### 4.1 作为"数值 bug 修复"任务(原命题)

不成立 —— 没有 bug,无法评估难度。

### 4.2 假设改写为"性能优化"任务

- **难度**:中高。要把 nim-core load 段(parquet 读 + zstd 解压 + PLAIN decode)从
  5.1s 拉到接近 Rust 2.8s,需要懂:列式解码零拷贝、`ptr UncheckedArray` 借用、ARC
  move 语义消拷贝、哈希聚合优化、RSS 控制(当前 nim 7.15G vs rust 3.76G 几乎 2×)。
  这是真硬骨头,弱模型大概率只能跑通编译、复现数字,做不出实质提速。
- **分离度**:理论上有(强模型可能拿下 1.3× 阈值,弱模型卡在 1.5×)。但**问题在于
  评测信号被环境噪声污染**:wall 受 ccache 命中、磁盘 cache 冷热、并行副本争用
  影响,跨 run 方差大;要可信判红绿必须 warm-cache + 串行 + 锁核,这对"隔离副本并行
  评测"是反约束。
- **正确性 gate 反而稳**:diff.py byte-equal 是确定性的,可以稳定卡"优化不许改坏结果"。

---

## 5. 最终裁决

### 5.1 作为"Nim 数值计算 bug 修复"硬任务:**不可行**

理由(硬事实,非推测):
1. transcript 里**不存在数值计算 bug**。首次对比即 `rows_kept/groups` 一致
   (row 206),终态 **byte-exact 16/16 列 0 偏差**(row 267)。任务实质是性能对比
   benchmark,"计算貌似有问题"经用户澄清后指的是"读写慢",不是"算错"。
2. 没有 bug → 没有 fix diff → 没有 commit → **没有"红→绿"客观 oracle 可挂**。强行
   把它当 bug 任务只能靠人工注入 bug,那就不是真实历史任务了。
3. 即便撇开 (1)(2),环境也是**重耦合非密闭**:5.5G nim-core 副本 + 副本外私有 Nim
   编译器(`nim-src` 5G+)+ 全局 `~/.nim-profile.env` + 392G a_stock 主仓只读引用 +
   cargo/polars 宿主依赖 + 共享 ccache 锁。可在本机宿主环境内跑,但不是可搬运的
   干净评测单元。

### 5.2 有条件可行的替代命题:"nim-core parquet pipeline 性能优化"任务

若评测目标可以从"修数值 bug"放宽到"在保持 byte-equal 正确性前提下把 nim 端 pipeline
提速到接近 Rust 基线",则**有条件可行**,条件如下(全满足才落得了地):

- **C1 编译环境**:隔离副本必须能访问宿主全局 `~/.nim-profile.env` + `nim-src` 私有
  编译器 + `~/.local/bin/nim` wrapper。这些**不能复制进副本**,只能宿主内运行。接受
  "副本隔离的是 nim-core 工作树,不是整条 toolchain"。
- **C2 数据只读引用**:a_stock 单日输入(1.3G)和历史参考产物(48M)以绝对路径只读
  挂载,不进副本。
- **C3 per-worker 隔离 NIMCACHE**:覆写 `NIMCACHE_ROOT` 到 per-worker 目录,避免多副本
  争用同一 nimcache/ccache 锁导致串扰或编译失败。
- **C4 度量纪律**:性能 gate 必须 warm-cache + 串行 + 锁核 + 多次取中位数,否则 wall
  方差会淹没强弱模型的真实分离度。这与"并行跑多个 worker"天然冲突,需排队串行测性能段。
- **C5 双 gate oracle**:
  - 正确性(确定性,稳):`diff.py` byte-equal 退出码 = 0(优化不许改坏结果)
  - 性能(目标):`nim_wall / rust_wall < 阈值`(当前 1.51,可设 1.25/1.3 作 high 线)
- **C6 任务语义转换**:必须接受这是"性能优化"评测,不是"数值 bug 修复"评测。两者考的
  能力不同(前者考底层 Nim 性能工程,后者考逻辑定位)。

### 5.3 建议

- 若评测明确要"数值/逻辑 bug 定位修复"类硬任务:**换素材**。这条 transcript 不含
  这类信号。
- 若评测可接受"底层性能工程"类硬任务:可用第 5.2 命题,且它的正确性 oracle
  (byte-equal diff)质量很高、判定确定;但要正视 C1/C3/C4 的环境/度量约束,且产出的是
  "性能分离度"而非"对错分离度",方差控制是落地的真正难点。
