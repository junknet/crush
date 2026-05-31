# Crush Patcher 溯源测试与回归验证套件

本目录用于记录 Crush Patcher (`apply_patch`) 工具的迭代演进历史、典型测试用例（Cases）以及回归验证指南，以确保后续开发在优化性能或加入新功能时不会发生退化。

## 目录结构说明

* **[CHANGELOG.md](file:///home/junknet/Desktop/_cli_bases/crush/test/CHANGELOG.md)**：记录 Patcher 自第一代起的设计缺陷、Revert 历史，以及本次自愈重构版本的迭代变更日志。
* **[cases/](file:///home/junknet/Desktop/_cli_bases/crush/test/cases/)**：收集历史评测中暴露的经典失败用例，供后续回归验证。

## 如何运行回归验证？

### 1. 单元测试回归
在修改 Patcher 算法后，必须首先确保 `tools` 包下的 patcher 测试全部通过：
```bash
go test ./internal/agent/tools/ -v -run "TestApplyPatch|TestParsePatch|TestApplyHunk"
```

### 2. 对抗性 A/B 测试 (A/B Test Suite)
要验证在真实的 LLM 对话重构中是否发生退化，运行以下脚本：
```bash
# 1. 编译最新的二进制到 /tmp/crush-new
go build -o /tmp/crush-new .

# 2. 编译对比版本（或基准版本）到 /tmp/crush-old
git checkout <base-commit>
go build -o /tmp/crush-old .
git checkout main

# 3. 运行 A/B 测试
python3 acceptance/adversarial/edit_ab.py
```
验证标准为：**正确率必须达到 4/4，且在 `twin_blocks` 和 `deep_nest` 用例中均能一次成功 (`first-try-clean`)。**
