# Windows IME 一次提交打两遍（"双打"回归）复盘

> 日期：2026-11-21
> 基线：`3b28b021`（分支 `sec/high-fixes`，已包含 `1a95a91d` 合并的 `origin/main`）
> 范围：`frontend/src/utils/xtermImeCompatibility.ts`、`frontend/src/main.ts`、
> `frontend/src/components/BaseTerminal.vue`、`app_windows.go` 的 IME/focus 修复
> 触发问题：Windows + 中文输入法（微软拼音 / 微信输入法 / 搜狗等）确认候选词后，
> 一个词进终端两遍（例如输入"世界"显示"世界世界"）；拖拽窗口、Alt+Tab 回来后更容易复现
> 性质：本次复盘 + 修复 + 沉淀合并规则。修复提交与本文一并提交（`fix(ime): …`）

---

## 0. 给后续修改者/合并者（你）的执行须知

1. Windows 的 IME 去重有**两层**，必须同时存在：
   - **结构化层** `installWindowsImePatch`：修正 xterm `CompositionHelper` 的
     `_isComposing` / `_isSendingComposition` / `_compositionPosition` / `_dataAlreadySent`
     状态，取消/接管 deferred read，记录 `_lastCommittedText`；
   - **兜底层** `installCompositionCommitGuard`：挂在唯一的数据出口
     `coreService.triggerDataEvent` 上，用 `compositionend` 后的短窗口把**同一 payload 的第二次
     发送**丢掉，不依赖事件顺序。
2. 任何平台（Windows / Linux / macOS）都**不许**用"二选一"处理这两个修复：它们的触发路径不同，
   互相不能替代。`053b4ce7` 的合并正是把 main 的 guard 从 Windows 改成了 Linux-only，
   导致双打回归。
3. 平台判断分支必须补对应 UA 的测试；测试里的 `navigator.userAgent` 改了，就说明该平台的
   行为契约变了，review 时必须问为什么。
4. 平台相关改动**必须真机验证**：至少微软拼音，覆盖"Enter 确认候选词 / 鼠标点选候选词 /
   拖拽窗口后输入 / Alt+Tab 回来后输入"四条路径。

---

## 1. 事故复盘

### 1.1 现象

- Windows + WebView2，中文输入法确认候选词后，PTY 收到两份相同文本：
  `世` + `界世界`、`你好你好`，或整段语音转写重复。
- 纯 ASCII 快速输入、方向键、回车不受影响（去重逻辑只针对多字符/非 ASCII 的 IME 提交）。
- 拖拽窗口、窗口激活（Alt+Tab）后概率明显升高——这正是 TSF client 被 detach、
  `WM_CHAR` 与 xterm textarea diff 同时投递的典型场景。
- 单元测试全绿：因为回归点不是逻辑写错，而是**某条平台路径没有安装**。

### 1.2 根因：合并冲突把"Windows 修复"改成了"Linux 修复"

时间线：

| 提交 | 分支 | 行为 |
| --- | --- | --- |
| `4a56e25b` fix(terminal): dedupe IME commits on Windows | `main` | 新增 `installCompositionCommitGuard`，在 `compositionend` 后 100ms 内丢弃重复的多字符/非 ASCII payload；测试用 **Windows UA**，注释与提交信息都写明目标是 Windows/WebView2（微信输入法 + opencode 的复现）。 |
| `788e9fa6` fix(ime): re-anchor the WebView2 IME after a window drag… | `sec/high-fixes` | 本分支自己的 Windows 结构化补丁 `installWindowsImePatch`：接管 deferred read、修 composition 状态、记录 `_lastCommittedText`、拖拽/激活后 `resetXtermInputState`。 |
| `053b4ce7` Merge origin/main into sec/high-fixes | `sec/high-fixes` | 冲突取舍写成"IME keeps the Windows patch and main's Chromium commit guard for Linux"：`installImeCompatibilityPatch` 在 Windows 分支 `return installWindowsImePatch(terminal)`，guard 只留给非 Windows 的 Chromium；同时把 main 的 guard 测试 UA 从 `Windows NT 10.0` 改成 `X11; Linux x86_64`。 |

结果：Windows 上只跑结构化层，guard 不再安装。结构化层覆盖的是"同一份状态下的两条已知发送路径"，
一旦 TSF 被 detach（WndProc hook 未装、拖拽边界、激活竞态）或提交文本经由 xterm 的
keypress / textarea-diff 路径再次出现，它没有统一的出口兜底，词就落两遍。main 上被 `4a56e25b`
修掉的复现于是在本分支复活。

这不是"哪边代码更对"的问题：两层修的是不同层次，正确做法是**叠加**，而不是在冲突里挑一个。

### 1.3 为什么没有被挡住

- guard 的测试仍在，但 UA 被改成 Linux 后，Windows 分支测试只验证了结构化 patch，
  guard 在 Windows 上是否安装没有任何断言。
- 结构化 patch 的单测用 fake core 直接调 helper，覆盖不到"真实 Chromium 事件顺序 + TSF detach"
  这种跨层场景。
- `git diff` 里只是一行平台分支和一个 UA 字符串，review 时极易被当成"等价重构"。

---

## 2. 修复

`frontend/src/utils/xtermImeCompatibility.ts`：

- `installImeCompatibilityPatch` 不再在 Windows 提前返回。先构造 `installCompositionCommitGuard`，
  Windows 再叠加 `installWindowsImePatch`，用 `combineDisposables` 统一释放：

```ts
const core = (terminal as TerminalWithCore)._core
const helper = core?._compositionHelper
const commitGuard =
  core && helper ? installCompositionCommitGuard(core, helper) : null

if (/Windows/i.test(navigator.userAgent)) {
  const windowsPatch = installWindowsImePatch(terminal)
  return commitGuard ? combineDisposables(windowsPatch, commitGuard) : windowsPatch
}
return commitGuard ?? noopDisposable
```

- 安装顺序：先结构化 patch，后 guard。两者写的是不同对象（helper 方法 / `_inputEvent` 与
  `coreService.triggerDataEvent`），顺序不影响语义；`dispose()` 会逐层还原。
- Linux/Chromium 仍走 guard；macOS 仍走 `installMacImePatch`，互不影响。
- guard 的窗口只对"多字符或非 ASCII 的纯文本"生效，控制序列、换行、单个 ASCII 字符永远不折叠；
  每次 `compositionstart` 清空窗口，所以"连续两次提交同一个词"不会被误吞。

测试（`frontend/src/utils/xtermImeCompatibility.test.ts`）：

- guard 的 describe 恢复 **Windows UA**，把 `4a56e25b` 的 Windows 用例重新变成有效断言；
- 新增"Windows 结构化 patch 与 guard 同时安装"的用例（`_inputEvent` 被包装、
  `_resetWindowsIme` 存在、重复 payload 只发一次）；
- 新增 Linux UA 用例，确认另一条平台路径没有回退；
- `dispose` 用例同时断言 `triggerDataEvent` 与 `_inputEvent` 都恢复原值。

---

## 3. 硬性不变量

- **I-1 一次 IME 提交只能产生一次 PTY 写入。** 入口可以有多条，出口必须收敛；
  `coreService.triggerDataEvent` 是唯一可靠的收敛点。
- **I-2 平台分支是"叠加"，不是"替换"。** 同一问题的结构化修复与出口兜底可以共存；
  合并冲突时不得以"本分支已有补丁"为由移除 main 的兜底。
- **I-3 去重不能误伤正常输入。** 只处理 `compositionend` 后短窗口内的多字符/非 ASCII 纯文本；
  控制序列、`\r`、单个 ASCII 永不折叠；`compositionstart` 必须重置窗口。
- **I-4 平台 UA 的测试是契约。** 改动 `navigator.userAgent` stub 等同于改变平台行为，
  必须同时补该平台的安装断言与真实场景说明。
- **I-5 拖拽/激活路径必须留下复位入口。** `resetXtermInputState` 取消 deferred read、
  清 `_lastCommittedText`，否则下一次提交会读到上一次的残留文本。

---

## 4. 合并 `main` 时的检查清单（IME 相关）

合并前：

```bash
git log --oneline HEAD..origin/main -- frontend/src/utils/xtermImeCompatibility.ts \
  frontend/src/components/BaseTerminal.vue frontend/src/main.ts app_windows.go
git diff HEAD...origin/main -- frontend/src/utils/xtermImeCompatibility.ts
```

只要 diff 里出现以下任一形态，按"IME 回归风险"处理：

- `installImeCompatibilityPatch` 的 Windows 分支出现新的提前 `return`；
- guard / 结构化 patch 的平台归属被改写；
- 测试里的 `navigator.userAgent` 被改；
- `resetXtermInputState`、`_lastCommittedText`、`compositionend` 的去重窗口被删除或提前返回。

合并后必须跑：

```bash
cd frontend
npx vue-tsc --noEmit
npx vitest run src/utils/xtermImeCompatibility.test.ts \
  src/components/BaseTerminal.imeActivation.test.ts
npx vitest run
```

Windows 真机冒烟（缺一不算过）：

1. 微软拼音输入"世界"：Enter 确认 → 只出现一次；
2. 同一词用鼠标点选候选词 → 只出现一次；
3. 拖拽窗口后立刻输入 → 只出现一次，候选框不跑到屏幕角落；
4. Alt+Tab 切走再回来输入 → 只出现一次；
5. 快速输入 `abc`、连按方向键、连按回车 → 不丢、不合并；
6. 连续两次提交同一个词（中间重新开始组合）→ 两次都要出现。

---

## 5. 与其它文档的关系

- 拖拽/激活的 native 侧修复见 `app_windows.go` 的 `subclassMainWindow`
  （`WM_ENTERSIZEMOVE` / `WM_EXITSIZEMOVE` / `wmAppReanchorIme`）与
  `2026-11-19-startup-safety-invariants.md` §1（同一轮合并引入的 double-subclass 启动崩溃）。
- `resetXtermInputState` 的调用点见 `BaseTerminal.vue` 的 `reanchorImeAfterFocusChange`；
  修改激活/拖拽流程时不要删掉它。
- 历史 IME 提交（`8573ff56`、`92094453`、`714fdf40`、`a6e8d140`、`0bae4a59`、`788e9fa6`）
  代表了不同层次的尝试；`8573ff56` 曾删除 JS 侧 Windows 去重、完全依赖 WndProc，
  后续因 hook 未安装/事件顺序问题又补回结构化层。**结论：native 修复与 JS 出口兜底必须同时保留。**

---

## 6. 类型检查作为提交门禁

这次合并（`1a95a91d`）除了 IME 回归，还带进来 23 个前端 type error（图标 `size` 传字符串、
模板里把 `ref` 当普通值用 `platform.value`、`TabItem` 里已经不可能成立的类型比较、
未显式标注的 `withSshAuthRetry` 泛型）。`npm run build` 走的是 Vite，**不做类型检查**，
所以这些错误能一路进主干。现在改为三道门：

1. **本地 pre-commit**：`.githooks/pre-commit`，只对涉及 `frontend/` 的提交运行
   `npm run typecheck`；`sh scripts/install-git-hooks.sh` 在每个 clone 里安装一次。
2. **CI required check**：`.github/workflows/frontend-typecheck.yml`，push/PR 都会
   `generate bindings` + `npm ci` + `npm run typecheck`。
3. **文档约束**：`CONTRIBUTING.md` 与 `AGENTS.md` 明确“前端类型错误必须为零”，
   不允许通过放宽 `strict` / `noUnusedLocals` / 绑定类型声明来消错。

手动命令：

```bash
sh scripts/install-git-hooks.sh     # 一次性
cd frontend && npm run typecheck    # 等价于 wails3 task check:frontend:types
```

注意：`frontend/bindings/` 是 gitignore 的，PR 机器上必须先 `wails3 generate bindings`；
`src/types/bindings.d.ts` 只把生成层声明为 `any`，它不能替代对真实绑定的检查。

## 7. 回滚策略

若发现 guard 误吞正常输入：

1. 先确认 `compositionstart` 是否真的到达（TSF detach 时可能丢）——`onCompositionStart`
   未执行会让窗口在上一次提交后一直打开；
2. 临时只保留结构化 patch（`installWindowsImePatch`），但必须在本文档和 PR 里记录
   "Windows 双打风险回归"，并附复现步骤；
3. 不要通过调大/调小 `COMMIT_DEDUPE_WINDOW_MS` 来解决事件顺序问题——那只是把竞态窗口挪个位置。
