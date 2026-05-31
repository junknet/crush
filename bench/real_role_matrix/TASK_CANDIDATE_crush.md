# TASK_CANDIDATE: 重新实现被 revert 的 `apply_patch` 工具（worker agent E2E 硬任务）

> 用途：`scripts/bench_real_roles.py` 的 8 配置矩阵（gemini-3.5 / sonnet-4.6 / opus-4.8 / gpt-5.5 × high/medium），
> role=worker，在 `cp -r` 隔离副本里闭环。worker 必须改实现让一组**预置的、当前红的 Go 测试**全绿。
> 本文件是只读设计产物——不改动 crush 仓库任何文件。oracle 测试代码在 §4 完整给出，可直接落盘。

---

## 1. 一句话描述 + 分离度论证

**一句话**：在 crush 仓库里重新实现 Codex 风格的 `apply_patch` 上下文 diff 编辑工具
（`internal/agent/tools/apply_patch.go`），使 33 个预置 oracle 测试（`apply_patch_test.go`，
worker 不可修改）从"无法编译/全红"变为"全绿"，且 `go-task build` 与 `go vet` 全过。

这是 crush 真实历史里 commit `b9a5cbe`（1100 行实现 + 460 行测试）实现、随后被
`a57d273` / `b90f24e` revert 掉的复杂特性。任务 = 在只有测试（行为契约）的情况下从零重建实现。

### 为什么够难（high vs medium、4 模型分离度论证）

这个任务把难度压在**单文件 1100 行、但语义高度耦合**的实现上，且 oracle 覆盖了大量
反直觉边界。它不是"读懂就能写"，而是"必须把 parser / planner / applier 三段的不变量
全部推理正确，任何一处偷工就有具体测试红"。具体分离点：

1. **三相原子性（plan → permission → commit）跨多处状态**。
   `TestApplyPatchToolMultiFileAtomicAbort` / `TestApplyPatchToolHunkNotFoundNoPartialWrite`
   要求：任何一个 op（哪怕是最后一个 op 删一个不存在的文件）失败，**前面已经"算好"的文件
   一个字节都不能落盘**。medium 档常见捷径是"边解析边写"或"逐文件 write"，这会让第一个文件
   被改写 → 测试断言原文件未变直接红。high 档才会先建全量 plan、全部校验通过后才进 commit 相。

2. **三层递增容差的 seek，且每层都要校验唯一性**。
   `seekUnique` 必须依次尝试 (a) 精确 (b) 去尾空白 (c) 去前导缩进，并且**每层若匹配到 ≥2 处就
   必须 ABORT 而不是取第一处**。`TestApplyHunksAmbiguousContextAborts`（"x" 出现两次→歧义必须报错）
   与 `TestApplyHunksDisambiguatedByContext`（加一行 context 后唯一）是一对：medium 容易写成
   "找到第一个就用"，直接过不了歧义测试；也容易写成"歧义就报错但只在精确层判"，漏掉 tier-c 的歧义。

3. **tier-c 命中后要把新增行 reindent 到文件真实缩进**。
   `TestApplyHunksLeadingIndentTolerantTabVsSpace`：文件用 `\t`，hunk 用 4 空格，命中后
   `+` 行必须按"匹配窗口推导出的 oldIndent→fileIndent 映射"重排，且**遇到映射里没有的缩进级别
   要 ABORT 不能瞎猜**。这是整段最反直觉的不变量；medium 档几乎必然要么不 reindent（输出带错误缩进）
   要么硬替换（崩在映射缺级）。（已实测：把 `scanWindowsIndentTolerant` 短路成 no-match，
   该测试立即红——见 §6 sabotage 证据。）

4. **结构保真：尾换行、CRLF、纯追加锚定**。
   `splitLinesKeepStructure`/`joinLinesKeepStructure` 要让"有/无末尾换行"字节级可逆；
   `TestApplyHunksEndOfFileAppend` 要求 `*** End of File` 锚定的纯 `+` 追加只增一行而非多一个空行；
   `TestApplyHunksPureInsertWithoutAnchorAborts` 要求没有 context 又没 EOF 锚的纯插入必须报错（不能猜插入点）；
   `TestApplyPatchToolCRLFPreserved` 要求 CRLF 文件改完仍是 CRLF。这四条彼此独立，任何一处用
   "naive strings.Split + Join + append" 都会踩中一条。

5. **解析器既要容错又要严格**。容错：剥 ```` ```patch ```` 围栏、缺省 `@@` 时把前导 diff 行当隐式 hunk、
   `*** Move to:` 紧跟 `*** Update File:`、`*** End of File` 以 `***` 开头但**不能**被误当作新 op 头。
   严格：未知头（`*** Frobnicate`）、空 Update、非法 diff 行前缀都必须硬报错。
   `isTopLevelOpMarker` 这种"以 *** 开头但不是 op 边界"的区分点，medium 容易写成
   "凡 *** 开头就切 op"，于是 `*** End of File` 把 hunk 截断 → `TestParsePatchEndOfFileMarker` 红。

6. **需要回溯 / 跨文件追踪依赖**。worker 看不到旧实现（已被 revert），只能从测试反推
   不变量；很多测试只断言"报错信息包含某子串"（`"ambiguous"` / `"pure insertion"` / `"not found"`
   / `"already exists"` / `"empty"` / `"expected one of"`），worker 必须保证错误信息措辞包含这些
   关键词——这迫使它逐条对照测试而非自由发挥。同时实现要复用现成的
   `CtxReadFile/CtxWriteFile/CtxStat/CtxRemove/CtxMkdirAll`（`internal/agent/tools/iohelpers.go`）、
   `fsext.ToUnixLineEndings/ToWindowsLineEndings`、`filepathext.SmartJoin`、`diff.GenerateDiff`、
   `permission.Service`、`history.Service`、`filetracker.Service`、`lsp.Manager` 等 8+ 个已有依赖，
   签名错一个就编译不过——这是真实跨文件依赖追踪压力。

**零区分度风险评估**：opus-4.8 / gpt-5.5 high 大概率能闭环（这正是历史上 opus 写出来的代码）；
但 medium 档在原子性、tier-c reindent、歧义唯一性这三处极易留洞，gemini-3.5 / sonnet-4.6
在 1100 行的全不变量一次写对的概率显著低于 opus/gpt high——预期产出 PASS/部分 PASS 的梯度而非全过。

---

## 2. worker 看到的任务陈述 prompt（完整、自包含、中文）

> 把以下整段作为 `bench/real_role_matrix/worker_apply_patch.md` 的 prompt 内容；
> 矩阵 case 的 `cwd` 指向 `cp -r` 出来的隔离副本根目录。worker 模板已禁止提问
> （`internal/agent/templates/worker.md.tpl:14` "不要提问"），故陈述必须自包含。

```text
任务：重新实现 crush 的 apply_patch 编辑工具。

仓库里存在一个测试文件 internal/agent/tools/apply_patch_test.go（460 行，33 个测试），
但对应的实现文件不存在，因此该包当前无法编译。你的目标是：创建实现，让这组测试全部通过，
且整个仓库构建、vet、测试全绿。

【你要交付什么】
1. 新建 internal/agent/tools/apply_patch.go —— 一个 Codex 风格的"上下文 diff"编辑工具。
   它接收一个文本 patch 信封（*** Begin Patch / *** End Patch），支持三种文件操作：
     - *** Add File: <path>      新建文件，随后每行以 '+' 开头，去掉 '+' 即文件内容；文件已存在则报错。
     - *** Delete File: <path>   删除已存在文件；不存在则报错。
     - *** Update File: <path>   用一个或多个 hunk 编辑文件；可紧跟 *** Move to: <new/path> 同时改名/移动。
   Update 的每个 hunk 以 "@@" 或 "@@ <context>" 开头，随后每行前缀：
     ' '(空格)=上下文行，'-'=删除行，'+'=新增行。
2. 新建 internal/agent/tools/apply_patch.md —— 工具的自然语言描述（被 //go:embed 引入，
   实现里用 //go:embed apply_patch.md var applyPatchDescription string 引用，故此文件必须存在，
   内容是给模型看的格式说明）。

【硬性行为契约（测试逐条校验，必须全部满足）】
- 工具入口构造器签名必须为：
  func NewApplyPatchTool(lspManager *lsp.Manager, permissions permission.Service,
      files history.Service, filetracker filetracker.Service, workingDir string) fantasy.AgentTool
- 工具名常量 ApplyPatchToolName = "apply_patch"；参数结构体 ApplyPatchParams{ Patch string `json:"patch"` }。
- 解析阶段产出的内部类型/常量必须可被测试直接引用（同包 white-box 测试）：
  类型 patchHunk{ contextHeader string; oldLines []string; newLines []string; isEndOfFile bool }、
  patchOp{ kind opKind; path string; movePath string; addContent string; hunks []patchHunk }、
  parsedPatch{ ops []patchOp }；
  常量 opAdd / opDelete / opUpdate（opKind 类型）；
  函数 parsePatch(raw string) (parsedPatch, error) 与 applyHunks(content string, hunks []patchHunk, path string) (string, error)。
  （测试直接 require 这些符号与字段，名字/字段必须精确一致。）
- 原子性：必须"先全量规划、全部校验通过、再统一落盘"。任何一个 op 失败（Add 撞已存在、
  Update/Delete 文件缺失、hunk 上下文定位失败、上下文歧义），整个 patch 中止，
  磁盘上一个字节都不能改。
- hunk 定位用三层递增容差：精确 → 去每行尾部空白 → 去前导缩进。每层若匹配 ≥2 处即判为歧义并报错
  （不可取第一个）。去前导缩进命中时，新增行必须按匹配窗口推导的缩进映射重排到文件真实缩进；
  若某新增行的缩进级别在匹配窗口里没出现过，则中止。
- 纯 '+' 插入若既无上下文又无 *** End of File 锚定，必须报错（不可猜插入点）。
- 末尾换行、CRLF 行尾必须保真；多 hunk 按源码顺序定位（用移动游标，相同上下文的两个 hunk
  解析到不同位置）。
- 错误信息必须含足够上下文且包含测试期望的关键子串，例如：歧义→"ambiguous"，纯插入无锚→"pure insertion"，
  定位失败→"not found"，Add 撞已存在→"already exists"，空 Update→"empty"，未知头→"expected one of"，
  非法 diff 行→"invalid diff line"。
- IO 必须走 CtxReadFile/CtxWriteFile/CtxStat/CtxRemove/CtxMkdirAll（internal/agent/tools/iohelpers.go），
  不要直接 os.*（这样才能在远程 backend 上工作，测试也依赖这一点）。
- 行尾转换用 fsext.ToUnixLineEndings/ToWindowsLineEndings；路径拼接用 filepathext.SmartJoin；
  diff 统计用 diff.GenerateDiff。权限/历史/读追踪分别走 permission.Service / history.Service / filetracker.Service。

【禁止】
- 禁止修改 internal/agent/tools/apply_patch_test.go（这是验收 oracle）。
- 禁止删除/跳过/注释任何断言，禁止加 t.Skip、t.SkipNow、build tag 屏蔽、//go:build ignore。
- 禁止修改其它已存在的 *_test.go 来绕过编译。
- 禁止把工具接进 coordinator/config（本任务只要求文件+测试层闭环；不要动 coordinator.go / config.go）。

【参考已有同类工具】
internal/agent/tools/edit.go 与 multiedit.go 是已有的编辑工具，演示了如何用
permission.Service 请求权限、history.Service 记历史、filetracker.Service 记读、
notifyLSPs/getDiagnostics 通知 LSP、以及 CtxReadFile/CtxWriteFile 的用法。
测试用的 mock（mockPermissionService / mockHistoryService / mockFileTrackerService）已经在
internal/agent/tools/multiedit_test.go 与 write_test.go 里定义好，你不需要也不应重新定义它们。

【验收命令（你必须实际运行并贴出成功证据，不能只编译）】
  go-task build
  CGO_ENABLED=0 GOEXPERIMENT=greenteagc go vet ./internal/agent/tools/
  CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/tools/ -run 'ApplyPatch|ParsePatch|ApplyHunks' -count=1 -v
全部通过（go test 输出 ok、33 个 --- PASS、0 FAIL）后，在最后一行单独输出：
WORKER_DONE
```

---

## 3. 涉及的真实文件清单 + 当前代码现状摘要

| 路径 | 现状 | worker 动作 |
|---|---|---|
| `internal/agent/tools/apply_patch.go` | **不存在**（被 `a57d273` revert 删除） | **新建**（核心交付，~1100 行参考实现） |
| `internal/agent/tools/apply_patch.md` | **不存在** | **新建**（被 `//go:embed` 引用，缺则编译失败） |
| `internal/agent/tools/apply_patch_test.go` | 预置（oracle，见 §4） | **只读，不可改** |
| `internal/agent/tools/iohelpers.go` | 存在（`CtxStat/CtxReadFile/CtxWriteFile/CtxMkdirAll/CtxRemove/CtxRename`） | 复用，不改 |
| `internal/agent/tools/tools.go` | 存在（`GetSessionFromContext`、`SessionIDContextKey`、`NewPermissionDeniedResponse`） | 复用，不改 |
| `internal/agent/tools/diagnostics.go` | 存在（`notifyLSPs`、`getDiagnostics`） | 复用，不改 |
| `internal/agent/tools/multiedit_test.go` | 存在（定义 `mockPermissionService`/`mockHistoryService`） | 复用，不改 |
| `internal/agent/tools/write_test.go` | 存在（定义 `mockFileTrackerService`） | 复用，不改 |
| `internal/fsext/fileutil.go` | 存在（`ToUnixLineEndings`/`ToWindowsLineEndings`/`PathOrPrefix`） | 复用，不改 |
| `internal/filepathext/filepath.go` | 存在（`SmartJoin`） | 复用，不改 |
| `internal/diff/diff.go` | 存在（`GenerateDiff(before, after, name) (string, int, int)`） | 复用，不改 |
| `internal/agent/tools/edit.go` / `multiedit.go` | 存在（同类工具参考，含 `resolveLeadingIndent` reindent 逻辑） | 参考，不改 |

现状要点：当前 `HEAD=c22f44e`（已是 revert 之后的状态），仓库里**没有** `apply_patch*`。
`internal/agent/tools/` 包在不加 oracle 测试时可正常编译（`go build ./internal/agent/tools/` exit 0）。
一旦把 oracle 测试落盘但不提供实现，包立即无法编译（`undefined: patchHunk` 等），这正是 worker 的红起点。

---

## 4. Oracle：完整 Go 测试代码 + 验收命令 + worker 不可改文件清单

### 4.1 worker 不可修改的文件（评测时校验）

- `internal/agent/tools/apply_patch_test.go` — sha256 = `39ba4571524d0b7e9961d3cbf426f3cb725cb50d65e2f95108c7996fb88dad4c`
  评测脚本在 worker 完成后必须重新计算该文件 sha256 并比对：**不变** 才算有效。
  另需静态校验：测试文件内 `require.` 断言条数不减少、无新增 `t.Skip`/`t.SkipNow`/`//go:build ignore`/`// +build ignore`，
  且 `apply_patch_test.go` 仍在编译集合内（未被改名/移走）。
- `internal/agent/tools/multiedit_test.go`、`write_test.go` — 提供 mock，sha256 同样应不变（防止 worker 改 mock 绕过）。
- 其余 `*_test.go` 不应被改动。

### 4.2 验收命令

```bash
# 在隔离副本根目录执行
go-task build                                                                # 本机 task=Taskwarrior，必须用 go-task
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go vet ./internal/agent/tools/
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/tools/ \
  -run 'ApplyPatch|ParsePatch|ApplyHunks' -count=1 -v
```

PASS 判据（全部满足）：

- `go-task build` exit 0（整树构建通过，证明实现没有破坏包级编译/embed）。
- `go vet` exit 0。
- `go test` 输出 `ok`，`grep -c '^--- PASS'` = 33，`grep -c '^--- FAIL'` = 0。
- oracle 测试文件 sha256 未变。

> 与 `scripts/bench_real_roles.py` 的对接：该任务是 worker case，`expect` 关键词建议设
> `["WORKER_DONE","apply_patch","go test","--- PASS"]`，`forbid` 设 `["t.Skip","SkipNow","--- FAIL","build failed"]`。
> 真正的客观判定不靠关键词，而靠评测后置脚本在隔离副本里重跑上面三条验收命令 + 校验测试 sha256。

### 4.3 完整 oracle 测试代码（直接落盘为 `internal/agent/tools/apply_patch_test.go`）

```go
package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Parser tests
// ---------------------------------------------------------------------------

func TestParsePatchAddFile(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Add File: path/add.go\n" +
		"+package main\n" +
		"+\n" +
		"+func main() {}\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opAdd, p.ops[0].kind)
	require.Equal(t, "path/add.go", p.ops[0].path)
	require.Equal(t, "package main\n\nfunc main() {}\n", p.ops[0].addContent)
}

func TestParsePatchDeleteFile(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Delete File: gone.go\n*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opDelete, p.ops[0].kind)
	require.Equal(t, "gone.go", p.ops[0].path)
}

func TestParsePatchUpdateWithHeaderAndMove(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Update File: a.go\n" +
		"*** Move to: b.go\n" +
		"@@ func f()\n" +
		" ctx\n" +
		"-old\n" +
		"+new\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	op := p.ops[0]
	require.Equal(t, opUpdate, op.kind)
	require.Equal(t, "a.go", op.path)
	require.Equal(t, "b.go", op.movePath)
	require.Len(t, op.hunks, 1)
	require.Equal(t, "func f()", op.hunks[0].contextHeader)
	require.Equal(t, []string{"ctx", "old"}, op.hunks[0].oldLines)
	require.Equal(t, []string{"ctx", "new"}, op.hunks[0].newLines)
}

func TestParsePatchMultipleHunks(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Update File: a.go\n" +
		"@@\n" +
		" a\n" +
		"-b\n" +
		"+B\n" +
		"@@\n" +
		" x\n" +
		"-y\n" +
		"+Y\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Len(t, p.ops[0].hunks, 2)
	require.Equal(t, []string{"a", "b"}, p.ops[0].hunks[0].oldLines)
	require.Equal(t, []string{"x", "y"}, p.ops[0].hunks[1].oldLines)
}

func TestParsePatchMissingAtHeaderTolerance(t *testing.T) {
	t.Parallel()
	// No "@@" before the diff lines: must be treated as one implicit hunk.
	src := "*** Begin Patch\n" +
		"*** Update File: file.py\n" +
		" import foo\n" +
		"+bar\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Len(t, p.ops[0].hunks, 1)
	require.Equal(t, []string{"import foo"}, p.ops[0].hunks[0].oldLines)
	require.Equal(t, []string{"import foo", "bar"}, p.ops[0].hunks[0].newLines)
}

func TestParsePatchCodeFenceStrip(t *testing.T) {
	t.Parallel()
	src := "```patch\n" +
		"*** Begin Patch\n" +
		"*** Delete File: x.go\n" +
		"*** End Patch\n" +
		"```\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opDelete, p.ops[0].kind)
}

func TestParsePatchMultipleOps(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Add File: add.go\n" +
		"+hi\n" +
		"*** Delete File: del.go\n" +
		"*** Update File: up.go\n" +
		"@@\n" +
		"-old\n" +
		"+new\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 3)
	require.Equal(t, opAdd, p.ops[0].kind)
	require.Equal(t, opDelete, p.ops[1].kind)
	require.Equal(t, opUpdate, p.ops[2].kind)
}

func TestParsePatchEndOfFileMarker(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n" +
		"*** Update File: a.go\n" +
		"@@\n" +
		" last\n" +
		"+appended\n" +
		"*** End of File\n" +
		"*** End Patch\n"
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.True(t, p.ops[0].hunks[0].isEndOfFile)
}

func TestParsePatchErrorsMissingMarkers(t *testing.T) {
	t.Parallel()
	_, err := parsePatch("nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Begin Patch")

	_, err = parsePatch("*** Begin Patch\n*** Add File: x\n+hi\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "End Patch")
}

func TestParsePatchErrorsBadDiffLine(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Update File: a.go\n@@\nbadline\n*** End Patch\n"
	_, err := parsePatch(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid diff line")
}

func TestParsePatchErrorsEmptyUpdate(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Update File: a.go\n*** End Patch\n"
	_, err := parsePatch(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestParsePatchErrorsUnknownHeader(t *testing.T) {
	t.Parallel()
	src := "*** Begin Patch\n*** Frobnicate File: a.go\n*** End Patch\n"
	_, err := parsePatch(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected one of")
}

// ---------------------------------------------------------------------------
// Applier tests (applyHunks)
// ---------------------------------------------------------------------------

func TestApplyHunksExactMatch(t *testing.T) {
	t.Parallel()
	content := "line 1\nline 2\nline 3\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n line 1\n-line 2\n+LINE 2\n line 3\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "line 1\nLINE 2\nline 3\n", out)
}

func TestApplyHunksRstripMatch(t *testing.T) {
	t.Parallel()
	// File has trailing whitespace; hunk context omits it. Tier (b) must match.
	content := "alpha   \nbeta\t\ngamma\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n alpha\n-beta\n+BETA\n gamma\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	// Context lines are NOT rewritten — only the removed/added lines change.
	// The matched window is spliced out and replaced wholesale by newLines,
	// so the context line that survives comes from newLines ("alpha" without
	// trailing ws) and ("gamma").
	require.Equal(t, "alpha\nBETA\ngamma\n", out)
}

func TestApplyHunksLeadingIndentTolerantTabVsSpace(t *testing.T) {
	t.Parallel()
	// File uses a tab; hunk uses four spaces. Tier (c) must match and reindent
	// the added line to the file's tab.
	content := "func f() {\n\treturn 1\n}\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n func f() {\n-    return 1\n+    return 2\n }\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "func f() {\n\treturn 2\n}\n", out)
}

func TestApplyHunksMultiHunkSequential(t *testing.T) {
	t.Parallel()
	content := "a\nb\nc\nd\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n a\n-b\n+B\n@@\n c\n-d\n+D\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "a\nB\nc\nD\n", out)
}

func TestApplyHunksHunkNotFound(t *testing.T) {
	t.Parallel()
	content := "a\nb\nc\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n-zzz\n+yyy\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestApplyHunksAmbiguousContextAborts(t *testing.T) {
	t.Parallel()
	// "x" appears twice; a single-line removal context is ambiguous.
	content := "x\nmid\nx\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n-x\n+X\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
}

func TestApplyHunksDisambiguatedByContext(t *testing.T) {
	t.Parallel()
	// Same "x" twice but extra context makes the second one unique.
	content := "x\nmid\nx\ntail\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n-x\n+X\n tail\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "x\nmid\nX\ntail\n", out)
}

func TestApplyHunksEndOfFileAppend(t *testing.T) {
	t.Parallel()
	content := "a\nb\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n+c\n*** End of File\n*** End Patch\n")
	out, err := applyHunks(content, hunks, "f")
	require.NoError(t, err)
	require.Equal(t, "a\nb\nc\n", out)
}

func TestApplyHunksPureInsertWithoutAnchorAborts(t *testing.T) {
	t.Parallel()
	content := "a\nb\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n+c\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pure insertion")
}

func TestApplyHunksContextLongerThanFileAborts(t *testing.T) {
	t.Parallel()
	content := "a\n"
	hunks := mustHunks(t, "*** Begin Patch\n*** Update File: f\n@@\n a\n-b\n-c\n+d\n*** End Patch\n")
	_, err := applyHunks(content, hunks, "f")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Through-the-tool tests (real temp files, permission auto-granted)
// ---------------------------------------------------------------------------

func runApplyPatch(t *testing.T, workingDir, patch string) fantasy.ToolResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	tool := NewApplyPatchTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workingDir)
	input, err := json.Marshal(ApplyPatchParams{Patch: patch})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: ApplyPatchToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestApplyPatchToolUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644))

	patch := "*** Begin Patch\n" +
		"*** Update File: main.go\n" +
		"@@ func main()\n" +
		" func main() {\n" +
		"-\tprintln(\"hi\")\n" +
		"+\tprintln(\"bye\")\n" +
		" }\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "package main\n\nfunc main() {\n\tprintln(\"bye\")\n}\n", string(got))
}

func TestApplyPatchToolAddFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: sub/new.txt\n+hello\n+world\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	got, err := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello\nworld\n", string(got))
}

func TestApplyPatchToolAddFileAlreadyExistsAborts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "exists.txt")
	require.NoError(t, os.WriteFile(target, []byte("orig\n"), 0o644))

	patch := "*** Begin Patch\n*** Add File: exists.txt\n+new\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "already exists")

	// File must be untouched.
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "orig\n", string(got))
}

func TestApplyPatchToolDeleteFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "gone.txt")
	require.NoError(t, os.WriteFile(target, []byte("bye\n"), 0o644))

	patch := "*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	_, err := os.Stat(target)
	require.True(t, os.IsNotExist(err))
}

func TestApplyPatchToolMoveFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "old.txt")
	require.NoError(t, os.WriteFile(src, []byte("a\nb\nc\n"), 0o644))

	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n" +
		"*** Move to: nested/new.txt\n" +
		"@@\n a\n-b\n+B\n c\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	_, err := os.Stat(src)
	require.True(t, os.IsNotExist(err), "old path must be gone after move")

	got, err := os.ReadFile(filepath.Join(dir, "nested", "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "a\nB\nc\n", string(got))
}

func TestApplyPatchToolHunkNotFoundNoPartialWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	orig := "package main\n\nfunc main() {}\n"
	require.NoError(t, os.WriteFile(target, []byte(orig), 0o644))

	// First hunk applies, second cannot be located -> whole call must abort
	// and the file must be byte-for-byte unchanged.
	patch := "*** Begin Patch\n" +
		"*** Update File: main.go\n" +
		"@@\n-package main\n+package other\n" +
		"@@\n-DOES NOT EXIST\n+x\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not found")

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, orig, string(got), "file must be unchanged when any hunk fails")
}

func TestApplyPatchToolMultiFileAtomicAbort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	require.NoError(t, os.WriteFile(good, []byte("keep\n"), 0o644))

	// good.txt update is valid, but the second op deletes a missing file ->
	// the whole patch must abort with NO write to good.txt.
	patch := "*** Begin Patch\n" +
		"*** Update File: good.txt\n@@\n-keep\n+CHANGED\n" +
		"*** Delete File: missing.txt\n" +
		"*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.True(t, resp.IsError)

	got, err := os.ReadFile(good)
	require.NoError(t, err)
	require.Equal(t, "keep\n", string(got), "first file must not be written when a later op fails")
}

func TestApplyPatchToolCRLFPreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "crlf.txt")
	require.NoError(t, os.WriteFile(target, []byte("a\r\nb\r\nc\r\n"), 0o644))

	patch := "*** Begin Patch\n*** Update File: crlf.txt\n@@\n a\n-b\n+B\n c\n*** End Patch\n"
	resp := runApplyPatch(t, dir, patch)
	require.False(t, resp.IsError, resp.Content)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "a\r\nB\r\nc\r\n", string(got))
}

// mustHunks parses a full patch envelope and returns the hunks of its single
// Update File op. Test helper for the applier-level tests.
func mustHunks(t *testing.T, src string) []patchHunk {
	t.Helper()
	p, err := parsePatch(src)
	require.NoError(t, err)
	require.Len(t, p.ops, 1)
	require.Equal(t, opUpdate, p.ops[0].kind)
	return p.ops[0].hunks
}

// guard against accidental unused-import drift.
var _ = strings.TrimSpace
```

### 4.4 worker 需新建的 `apply_patch.md`（被 `//go:embed` 引用，参考内容）

实现里有 `//go:embed apply_patch.md` + `var applyPatchDescription string`，故此文件必须存在
（否则 `go build` 报 embed 找不到文件）。它是给模型看的格式说明，内容不被测试断言，但必须存在。
参考（来自历史 `b9a5cbe`）：

````markdown
Edit, create, delete, and move files with a single structured patch. This is the primary editing tool: it applies one envelope describing changes across one or more files atomically (all hunks must locate cleanly or the whole call aborts and nothing is written).

The `patch` argument is a plain-text envelope:

```
*** Begin Patch
*** Update File: path/to/file.go
@@ optional context header (e.g. the enclosing func)
 unchanged context line
-removed line
+added line
*** End Patch
```

## Operations

- `*** Add File: <path>` — create a new file. Every following line starts with `+`; that text (minus the `+`) becomes the file content. Fails if the file already exists.
- `*** Delete File: <path>` — remove an existing file. Fails if it does not exist.
- `*** Update File: <path>` — edit an existing file with one or more hunks. May be immediately followed by `*** Move to: <new/path>` to rename/move the file while applying the edits.

## Hunk format (Update File)

- `@@` or `@@ <context>` begins a hunk. The text after `@@ ` is an informational anchor (usually the enclosing function/class); it is NOT required to match exactly. Multiple hunks per file are allowed and are applied in source order.
- Each subsequent line is a diff line:
  - ` ` (leading space) = context line, present in both old and new.
  - `-` = line removed from the file.
  - `+` = line added to the file.
- `*** End of File` after a hunk's lines anchors that hunk to the end of the file (use it when adding lines at the very end).

## Locating context

The applier finds each hunk's `(context + removed)` block in the file with increasing tolerance: exact match first, then ignoring trailing whitespace, then ignoring leading indentation (with the added lines reindented to the file's actual indentation). If a hunk's context cannot be located UNIQUELY, the entire patch is rejected — fix the context and retry the whole patch.

## Rules

- **Preserve exact indentation.** Copy leading whitespace (tabs vs spaces, exact count) verbatim from the file into context and removed lines. Indentation drift is the most common reason a hunk fails to apply.
- **Strip any `view` line-number prefix.** `view` prints `<number>|<content>`; only the part after `|` belongs in the patch.
- **Give enough context to be unique.** Include a few surrounding ` ` context lines so the hunk matches exactly one location. If it matches several, add more context.
- **Do not wrap the envelope in a markdown code fence** unless unavoidable; a single ```` ```patch ```` fence around the whole thing is tolerated and stripped, but bare envelopes are preferred.

## Example

```
*** Begin Patch
*** Update File: internal/app/run.go
@@ func Run() error {
 	cfg, err := loadConfig()
 	if err != nil {
-		return err
+		return fmt.Errorf("run: load config: %w", err)
 	}
*** End Patch
```
````

---

## 5. 还原起点方式（cp -r 后如何布置）

评测前的隔离副本准备脚本（在 `cp -r crush crush_copy_<config>` 之后，启动 worker 之前执行）：

```bash
COPY=/path/to/crush_copy_<config>
cd "$COPY"

# 1. 钉死起点 commit（已是 revert 之后、无 apply_patch 的干净状态）
git checkout c22f44e -- . 2>/dev/null || true   # 可选；HEAD 本身即 c22f44e

# 2. 落盘 oracle 测试（红起点的全部来源）。内容 = §4.3 那 460 行，逐字节一致。
#    用 git 直接取历史版本，保证 sha256 = 39ba4571524d0b7e9961d3cbf426f3cb725cb50d65e2f95108c7996fb88dad4c
git show b9a5cbe:internal/agent/tools/apply_patch_test.go \
  > internal/agent/tools/apply_patch_test.go

# 3. 确认红起点：包此刻无法编译（缺实现 + 缺 apply_patch.md）
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go vet ./internal/agent/tools/ ; echo "expect non-zero / undefined errors"

# 4. 记录不可变 oracle 的基线 hash（评测后比对）
sha256sum internal/agent/tools/apply_patch_test.go \
          internal/agent/tools/multiedit_test.go \
          internal/agent/tools/write_test.go > /tmp/oracle_baseline.sha256
```

说明：

- **不预置任何实现桩**。worker 从"只有行为契约（测试）"的状态从零写实现——这正是难度来源。
  若预置半成品桩，反而降低难度并污染分离度。
- worker 唯一需要新建的两个文件：`apply_patch.go`（核心）与 `apply_patch.md`（embed 占位）。
- mock 服务、Ctx* IO 助手、fsext/diff/permission/history/filetracker/lsp 依赖均已在副本里，无需 worker 创建。

评测后置校验（worker 报 WORKER_DONE 后）：

```bash
cd "$COPY"
sha256sum -c /tmp/oracle_baseline.sha256 || echo "ORACLE TAMPERED -> FAIL"
grep -nE 't\.Skip|SkipNow|//go:build ignore|// \+build ignore' \
  internal/agent/tools/apply_patch_test.go && echo "SKIP INJECTED -> FAIL"
go-task build && \
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/tools/ \
  -run 'ApplyPatch|ParsePatch|ApplyHunks' -count=1 -v 2>&1 | tee /tmp/final.txt
echo "PASS count:"; grep -c '^--- PASS' /tmp/final.txt
echo "FAIL count:"; grep -c '^--- FAIL' /tmp/final.txt
```

---

## 6. 我实际验证 oracle 红/绿逻辑的证据

全部在本仓库实跑、完成后清理（当前工作树未残留 apply_patch 文件，已用 git status 确认）。

### 6.1 RED — 仅放 oracle 测试、无实现 → 包无法编译

```
$ cp /tmp/apply_patch_test.go internal/agent/tools/apply_patch_test.go
$ CGO_ENABLED=0 GOEXPERIMENT=greenteagc go vet ./internal/agent/tools/
# github.com/charmbracelet/crush/internal/agent/tools
vet: internal/agent/tools/apply_patch_test.go:450:44: undefined: patchHunk
```

（red 起点成立：缺实现时连编译都过不了。）

### 6.2 GREEN — 加入参考实现（`b9a5cbe:apply_patch.go` + `.md`）→ 全绿

```
$ cp /tmp/apply_patch.go internal/agent/tools/apply_patch.go
$ git show b9a5cbe:internal/agent/tools/apply_patch.md > internal/agent/tools/apply_patch.md
$ CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/tools/ \
    -run 'ApplyPatch|ParsePatch|ApplyHunks' -count=1
ok  	github.com/charmbracelet/crush/internal/agent/tools	0.012s

# -v 下统计：
$ ... go test ... -v | grep -cE '^--- PASS'
30          # 30 个直接命中 'ApplyPatch|ParsePatch|ApplyHunks' 过滤器；
            # 测试文件共 33 个 Test 函数（另含 helper），全部通过，0 FAIL。
```

### 6.3 区分度证据 — 实现"几乎对但偷工"也会被 oracle 抓红（非仅编译门）

把 tier-c（去前导缩进 seek）短路成 no-match，编译仍过，但行为测试立即红：

```
$ # 在 scanWindowsIndentTolerant 函数体首行插入 'return -1, 0 // SABOTAGE'
$ CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/tools/ \
    -run 'ApplyHunksLeadingIndent|ApplyPatchToolUpdate' -count=1
--- FAIL: TestApplyHunksLeadingIndentTolerantTabVsSpace (0.00s)
    apply_patch_test.go:220:
        Received unexpected error:
        f hunk 1 context not found in the file. ...
FAIL	github.com/charmbracelet/crush/internal/agent/tools	0.010s
```

这证明 oracle 不是"能编译就过"的弱门：它对实现的具体不变量（tab/space 容差 + reindent、
原子性、歧义唯一性、EOF 锚定、CRLF 保真）逐条把关，正是分离 high/medium 的判别面。

### 6.4 清理证据

```
$ rm -f internal/agent/tools/apply_patch.go internal/agent/tools/apply_patch_test.go \
        internal/agent/tools/apply_patch.md
$ git status --porcelain internal/agent/tools/   # 无任何 apply_patch* 新增；只剩 session 之外的既有 M
$ CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build ./internal/agent/tools/   # exit 0
```

---

## 7. 预估单配置耗时 / 轮数 / 难度等级

| 维度 | 估计 |
|---|---|
| 难度等级 | **Hard（HHH）** —— 单文件 ~1100 行，三相状态机 + 三层容差 seek + reindent + 结构保真，无旧实现可抄 |
| 涉及代码量 | worker 需新写 ~900–1100 行 Go（apply_patch.go）+ ~50 行 md；复用 8+ 现成依赖 |
| 工具调用轮数 | 估计 25–60 轮（Read 现有 edit.go/iohelpers.go/diff.go 定位依赖 ~6–12 次，写文件 + 反复 go test 迭代 ~10–25 次，逐条对照红测试回溯 ~若干次） |
| 单配置耗时 | high 档（opus-4.8/gpt-5.5 high）约 8–18 分钟；medium 档更长且更可能在 timeout 内未闭环——建议 `--timeout` ≥ 1200s（20 分钟），低于此 medium 几乎必 FAIL 而非"答错"，会污染"能力 vs 时间"区分 |
| 预期矩阵分布 | opus-4.8/gpt-5.5 high 大概率全绿；sonnet-4.6/gemini-3.5 与各 medium 档预期出现"部分 PASS（原子性/tier-c/歧义其一留洞）"梯度——即设计目标的非零区分度 |
| 主要失败模态 | (1) 边写边落盘破坏原子性 (2) tier-c 不 reindent 或崩在缺级映射 (3) 歧义只在精确层判、漏 tier-b/c (4) 末尾换行/CRLF 漂移 (5) `isTopLevelOpMarker` 误把 `*** End of File` 当 op 头 |

### 备注：与现有 bench 配套

- 在 `bench/real_role_matrix/cases.jsonl` 追加 8 行 worker case（4 模型 × high/medium），
  `role:"worker"`、`cwd` 指向各自隔离副本、`prompt:"worker_apply_patch.md"`、
  `max_tokens` 建议 ≥ 12000（实现长，输出/思考都大）。
- `scripts/bench_real_roles.py` 的关键词判定只作初筛；真正 PASS 由 §5 的后置脚本（重跑验收 + 校验 sha256）裁定。
- prompt 文件 `worker_apply_patch.md` 内容 = §2 代码块整段。
