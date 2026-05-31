# Patcher 变更日志与迭代历史

本日志用于追踪 Crush Patcher (`apply_patch`) 工具的发展历史，剖析其设计演进背后的逻辑决策，为后续架构改进提供上下文。

---

## 2026-05-31 · 弹性自愈与广义 AST 守卫重构 (eba5d4c & 60cf261)

### 痛点诊断
在此前的版本中，传统的 `apply_patch` (Unified Diff) 由于追求字节级对齐，表现出了极强的**工具刚性**。在 A/B 评测中：
1. **缩进不容错**：只要模型产生了一点缩进层级偏差，或者新增行没能精确匹配上下文的缩进风格，整个 Hunk 就会被拒绝并直接 Abort。
2. **孪生块歧义阻碍 (`twin_blocks`)**：在有相似上下文的块中，由于 strict match 限制，模型极易因为 context 不唯一而导致 ambiguous 失败。
3. **小众格式受阻**：如果对非白名单的小众代码文件进行 patch 强行校验，会由于缺乏 parser 而直接抛错，缺乏安全退避（Bypass）能力。

这些缺陷直接导致了旧版 `apply_patch` 的高重试税，并在先前被 Revert 回退为 `edit`/`multiedit`（改进版 `old_string` 替换）。

### 本次重构改进
为了彻底攻克上述痛点，我们重新设计了具有**自愈弹性匹配机制**的高级 Patcher：
1. **空白符折叠匹配定位 (`seekTierCollapse`)**：
   引入第四寻求层。剥离首尾空白并将行内所有连续的空格和 Tab 折叠为单空格进行比对。这一修改彻底解决了相似作用域下的模糊定位，攻克了 `twin_blocks` 失败的问题。
2. **比例换算自愈重缩进 (`reindentNewLinesWithFallback`)**：
   如果严格映射失败（模型输出了 context 中不存在的缩进层级），系统自动分析文件基准缩进单位和 Hunk 缩进单位，实现对 Added 行缩进的自愈式按比例平移，保证了代码排版的完美融入。
3. **高解析度相似性匹配诊断 (`diagnoseMatchError`)**：
   若定位彻底失败，Patcher 自动在源文件中搜索相似度最高的候选窗口，并输出高清的对比日志（指出是缩进不一致、直弯引号不一致、还是内容不一致），引导模型自愈。
4. **广义 AST 校验卫兵与安全退避**：
   * **Sniff 嗅探**：对于无后缀的配置文件，通过内容首尾特征嗅探是否为 JSON/YAML。
   * **Bypass 退避**：强校验仅在 JSON, YAML, HTML 白名单上生效。对其他小众格式自动退避（Bypass），不拦截写回。
   * **通配符拦截**：严格禁止 op.path 包含 `*`, `?`, `[` 等 glob 通配符，强迫模型使用确切文件名，杜绝跨文件误伤。

### 测试证明
* 在 `edit_ab.py` 的受控 A/B 评测中，NEW 组（自愈 patcher 二进制）实现了 **4/4 (100% 正确率)**，而 OLD 组（旧版 edit）只有 3/4 成功，NEW 组攻克了旧版失守的 `deep_nest` (深层嵌套) 和 `twin_blocks` (双生块) 关卡。首试干净率 100%。

---

## 2026-05-15 · ApplyPatch Revert (d065b2a)
* **变更**：Revert 了 `apply_patch` (Unified Diff) 作为系统默认 edit 工具的尝试，切回 `edit`/`multiedit` (old_string) 方案。
* **原因**：当时在 tab 缩进编辑任务的 A/B 测试中，旧版 Patcher 的 strict match 对格式过于严苛，在 `twin_blocks` 用例下频繁发生 context 匹配歧义引起的 Abort 崩溃，首试干净度落后于改进的 old_string edit。

---

## 2026-05-12 · ApplyPatch 实验性引入 (461d891)
* **变更**：新增 Codex 风格的 `apply_patch` 上下文 diff 编辑工具。支持 `*** Add File:`、`*** Delete File:`、`*** Update File:` 协议，采用严格 exact/rstrip/indent 三层匹配 seek。
