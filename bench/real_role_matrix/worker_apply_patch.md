任务：重新实现 crush 的 apply_patch 编辑工具。

仓库里存在一个测试文件 internal/agent/tools/apply_patch_test.go（460 行，33 个测试），
但对应的实现文件不存在，因此该包当前无法编译。你的目标是：创建实现，让这组测试全部通过，
且整个仓库构建、vet、测试全绿。

【你要交付什么】
1. 新建 internal/agent/tools/apply_patch.go —— 一个 Codex 风格的“上下文 diff”编辑工具。
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
- 原子性：必须“先全量规划、全部校验通过、再统一落盘”。任何一个 op 失败（Add 撞已存在、
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
- 禁止从 git 历史、网络、或仓库其它位置寻找现成的 apply_patch 实现照抄；必须自己根据测试契约实现。

【参考已有同类工具】
internal/agent/tools/edit.go 与 multiedit.go 是已有的编辑工具，演示了如何用
permission.Service 请求权限、history.Service 记历史、filetracker.Service 记读、
notifyLSPs/getDiagnostics 通知 LSP、以及 CtxReadFile/CtxWriteFile 的用法。
测试用的 mock（mockPermissionService / mockHistoryService / mockFileTrackerService）已经在
internal/agent/tools/multiedit_test.go 与 write_test.go 里定义好，你不需要也不应重新定义它们。

【验收命令（你必须实际运行并贴出成功证据，不能只编译）】
  CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build ./...
  CGO_ENABLED=0 GOEXPERIMENT=greenteagc go vet ./internal/agent/tools/
  CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/tools/ -run 'ApplyPatch|ParsePatch|ApplyHunks' -count=1 -v
全部通过（go test 输出 ok、33 个 --- PASS、0 FAIL）后，在最后一行单独输出：
WORKER_DONE
