# 启动安全与内存安全不变量（含 double-subclass 启动崩溃复盘）

> 日期：2026-11-19
> 基线：`1a95a91d`（分支 `sec/high-fixes`，刚合并 `origin/main`）
> 范围：启动路径（`main.go`、`app.go`、`app_*.go` 的平台层、`backend/store` 的 bootstrap 链）+ 所有注册
> native 回调/窗口过程/委托的代码
> 触发问题：`task dev` / `wails3 dev` / 新编出来的二进制**启动即崩**（窗口还没出现就没了），旧二进制正常，
> 极易被误判为"环境问题"
> 性质：本次复盘 + 修复 + 沉淀规则。修复提交与本文一并提交（`fix(win): …`）

---

## 0. 给后续修改者/合并者（你）的执行须知

1. **最高优先级不变量：应用必须能启动。**
   启动失败 = 100% 功能损失，比任何单点 bug 都严重；内存损坏（越界、悬垂指针、无界递归爆栈）比启动失败更糟，
   因为它可能静默写坏用户配置/凭据。任何改动只要可能让程序启动不了或让内存不安全，**一律不能进主干**，
   功能再香也不行。
2. 本文档是硬约束，不是建议。§2 的不变量在 review / 合并时必须逐条对照。
3. 从 `main` 合并进加固分支时按 §3 执行，并**在 PR 描述里写明合并后启动验证结果**。
4. 不要削弱已有加固（见 `docs/dev/2026-10-18-security-audit-findings.md` §0.2）。
5. 平台相关代码（`app_windows.go` / `app_darwin.go` / `app_linux.go` / `app_android.go`、`main_*.go`、
   `win11_corners_windows.go`、`backend/platform/**`）**必须至少在一个真实平台上启动验证过**才算测过；
   `go build ./...` 通过 ≠ 能启动。

---

## 1. 事故复盘：Windows 启动即栈溢出（Exception 0xc00000fd）

### 1.1 现象

- `task dev` / `wails3 dev` 编译完、vite 构建完、WebView2 环境创建成功，**然后在第一条窗口消息处进程消失**。
- 直接运行新编译的 exe 同样秒退；`bin/uniTerm.exe`（旧二进制）可以正常启动 → 容易误判成"环境/构建问题"。
- 日志里只有一段 goroutine dump，关键行：

```
Exception 0xc00000fd 0x1 0xe637403ff0 0x7ff679cca366     ← STATUS_STACK_OVERFLOW
...
main.(*App).subclassMainWindow.func2(0x9079a, 0x7f, 0x1, 0x90)   ← 同一条消息的递归帧，重复上千次
main.(*App).subclassMainWindow.func2(0x9079a, 0x7f, 0x1, 0x90)
...
syscall.(*LazyProc).Call(0x2a1539cd6ea0, {0x2a1539ad83f0, 0x5, 0x5})   ← CallWindowProcW
```

### 1.2 根因：两个分支各自正确的改动，合并后互相踩死

两处改动单独看都没问题，**同时存在就是致命的**：

| 来源 | 提交 | 行为 |
| --- | --- | --- |
| 本分支（本地 IME 修复，**只存在于 `sec/high-fixes`**） | `8573ff56` fix(ime): stop Wails' move-start focus bounce detaching the Windows IME | 新增 `attachMainWindow`，在 `main.go` 的 `WindowShow` 里子类化窗口并装 IME/拖拽修复钩子 |
| `main`（#1503） | `e83e0d88` fix(win): stop leaking syscall callbacks in main-window HWND resolution | 把 HWND 从启动期主动查找改成 `ensureMainWindowHwnd` 懒解析 + 缓存，并在解析成功处 `subclassMainWindow()` |

`main` 自己只有懒解析那一条安装路径，所以 `origin/main` **单独跑是正常的**；本分支只有 `WindowShow`
那一条，也正常。合并进 `sec/high-fixes` 之后两条路径同时存在，正常启动就会跑两次：

| 时间线 | 调用链 | 结果 |
| --- | --- | --- |
| 早（store bootstrap 期间） | `bootstrapStores` → `bringMainWindowToFront` → `ensureMainWindowHwnd` → `subclassMainWindow` | `SetWindowLongPtrW` 返回真正的旧过程，`originalWndProc` = 系统的 |
| 晚（`WindowShow`） | `main.go` → `attachMainWindow`（`sync.Once` 只管自己这条路径）→ `subclassMainWindow` | `SetWindowLongPtrW` 返回的是**我们自己刚装进去的 trampoline**，`originalWndProc` = 自己 |

第二次安装后，窗口过程里最后那行 `procCallWindowProcW.Call(a.originalWndProc, …)` 变成**调用自己**：
任何一条窗口消息（实测第一条是 `WM_GETICON` 0x7F，另一次是 `WM_WINDOWPOSCHANGING` 0x46）都会无限递归，
把栈用满后抛 `0xc00000fd`，进程在显示 UI 之前就死了。

**这才是本次事故最该记住的一点**：没有哪个提交是"错"的，是两个分支的启动期副作用叠加。所以
§3 的合并规则不是形式主义——它就是这次事故的直接成因。

调试栈证词（修复前打点所得）：

```
DEBUG subclassMainWindow hwnd=0x50ad8 orig=0x0        ← ensureMainWindowHwnd（bringMainWindowToFront）
DEBUG subclassMainWindow hwnd=0x50ad8 orig=0x7ff787c003bb cb=0x7ff787c003c5  ← attachMainWindow，orig 就是自己的 cb
```

### 1.3 为什么没有被测试挡住

- `app_windows_test.go` 只验证"解析成功后 subclass 装上了"（`TestEnsureMainWindowHwndResolvesAndSubclasses`），
  从没验证"**只装一次**"，也没有任何测试真的给窗口发一条消息。
- 单测跑的是 `&App{}` + 测试窗口，`attachMainWindow` 这条路径没被触发；而真实启动两条路径都会走。
- `go build ./...` / `go vet` 全绿 —— 编译期完全看不出问题。

### 1.4 修复

- `app.go`：新增 `wndProcOnce stdsync.Once`；两条路径统一走 `installWndProcSubclass()`；
  `attachMainWindow` 不再覆盖 `ensureMainWindowHwnd` 已解析的 `mainHwnd`（加 `hwndMu` 保护）。
- `app_windows.go`：新增 `installWndProcSubclass()`（幂等）；`subclassMainWindow` 里加保险——若
  `SetWindowLongPtrW` 返回的"上一个过程"等于我们自己的 callback，直接返回，绝不再把自己当成 previous proc。
- `app_windows_test.go`：新增 `TestSubclassMainWindowInstallsOnlyOnce`，两条路径都走一遍，
  断言只安装一次且 `originalWndProc != wndProcCb`。
- `origin/main` 上目前只有一条安装路径，所以 main 单独不崩；但这次修复把"只能装一次"变成了代码层面的
  保证，`main` 以后再出现第二条路径也不会踩到同一个坑（建议顺手把同样的守卫回合 main）。

### 1.5 验证

```bash
go build ./... && go vet .
go test -count=1 ./                     # 337 passed
wails3 dev -config ./build/config.yml -port 9245   # 启动后窗口必须在，日志无 Exception
# 关键：不能用旧二进制/复用 bin/uniTerm.exe 验证
```

---

## 2. 硬性不变量（必须遵守，不可协商）

### I-1 任何平台、任何启动方式都必须能启动

- 覆盖：桌面 dev（`task dev` / `wails3 dev`）、桌面生产构建、server 模式（`task run:server`）、
  android 构建、首次运行（无配置）与老配置迁移两条路径。
- 启动路径上**不允许** `panic` / `log.Fatal` / `os.Exit(非 0)` / `runtime.throw`。任何失败必须降级
  （禁用该特性、写日志、返回错误给前端），而不是让进程消失。
- 启动路径的依赖（目录、权限、剪贴板、DLL 导出、WebView2、可选系统服务）**必须允许缺失**：
  用 `LazyProc.Find()` / `error` 检查，缺了就跳过，不要把"可选"写成"必须"。

### I-2 native hook 的安装必须幂等，且只能有一条安装路径

窗口过程子类化（`SetWindowLongPtrW(GWLP_WNDPROC)`）、macOS delegate、Linux/GTK 信号、Android JNI 回调
都属于此类：

- 用 `sync.Once`（或等价的原子 CAS）把安装收口到**一个** helper，禁止在多个调用点各自"顺手装一下"。
- **保存的 previous proc/handler 必须不是你自己的 trampoline**。装完立刻校验：
  `if prev == self { 回滚/直接返回 }`，绝不允许把自身当作 previous 而形成自递归。
- 卸载（`unsubclassMainWindow`）必须与安装成对，且可重复调用而不破坏状态。

### I-3 回调注册 / 递归 / 队列必须有界

- Go 在 Windows 上的 syscall callback 表是固定大小（`cb_max = 2000`，条目永不回收）：注册一次后重复使用，
  禁止在热路径里 `windows.NewCallback`（历史 issue #1426）。
- 禁止无界递归（尤其是 native → Go → native → Go 的回环）。任何 hook 都必须假设"消息可能来自我们自己"。
- 事件/消息转发到前端不得阻塞 UI 线程；禁止在 WndProc/模态循环里同步调用前端或 `EventsEmit`。

### I-4 内存安全：unsafe / uintptr / 跨 C 边界的生命周期

- `unsafe.Pointer` ↔ `uintptr` 转换只在同一个表达式内完成；不要把 `uintptr` 存字段"以后再转回来"
  （GC 可能已经移动/回收）。
- 传给 Windows API 的缓冲区（`unsafe.Pointer(&buf[0])`）必须保证调用期间活着；`GlobalLock` 拿到的指针
  只在锁住期间有效，用完立刻 `GlobalUnlock`。
- 保留 `wndProcCb`、`enumWindowsCb` 这类字段防止回调被 GC —— 新增 native 回调时同理，别只存在局部变量里。
- 共享字段跨 goroutine 读写必须加锁（`hwndMu` / `enumMu` 的既有模式），跨线程操作窗口必须回到 UI 线程
  （`InvokeSync`），不要在后台 goroutine 里直接碰 HWND。

### I-5 每次碰启动路径都要留测试

- 幂等类修复：必须有"调用两次仍是同一个状态"的测试（例：`TestSubclassMainWindowInstallsOnlyOnce`）。
- 触发过 `fatal` 的用例要放进 `UNITERM_*` 环境变量开关下（fatal 会带走整个测试二进制，见
  `TestFindMainWindowReusesCallback` 的既有做法），并在 CI 的冒烟任务里单独跑。
- 冒烟测试的最低标准：进程能起来、窗口在、连续发一条窗口消息不崩、能正常退出且不留残余进程。

---

## 3. 从 `main` 合并进加固分支（`sec/high-fixes` 等）的规则

本分支长期接受 `main` 的合并。合并本身就是高风险时刻：**`main` 的功能推进常常与加固分支的
"少做一次泄漏的查找""fail-closed""别在启动路径上多做一步"直接冲突**。按下面的顺序取舍。

### 3.1 合并前：先看差异落在哪一层

```bash
git fetch origin
git log --oneline HEAD..origin/main
git diff --stat HEAD...origin/main -- main.go app.go app_*.go backend/platform/** backend/update/**
```

只要差异触及以下任一位置，这次合并必须按"启动路径变更"对待（走 §3.3 全量验证）：

- `main.go`（窗口创建、`WindowShow`、单实例、shutdown）
- `app.go` 的 `ServiceStartup` / `bootstrapStores` / `attachMainWindow` / `ensureMainWindowHwnd` /
  `shutdown`
- 任何 `subclassMainWindow` / `NewCallback` / `SetWindowLongPtr` / delegate 注册点
- `backend/store/**` 的启动期初始化、`backend/update/**`（签名校验链）

### 3.2 冲突取舍顺序（功能 / 性能 / 安全的平衡）

1. **不变量 > 功能 > 安全加固细节 > 性能。** 顺序不可颠倒：先保证能启动、内存安全、fail-closed
   不被绕过；再保 `main` 的新功能完整可用；再谈"少一次枚举/少一次系统调用"的性能优化。
2. 当 `main` 的新代码**重新引入了**已被本分支修掉的问题（典型：又把 `findMainWindow` 改成每调用一次就
   `NewCallback`；又在启动路径上加了一次 `subclassMainWindow`），**保留 `main` 的功能意图，但用本分支的
   加固实现落地**：把"懒解析 + 缓存 + 单次安装"接进 `main` 的新结构里，而不是简单回退 `main`。
   反向同理：本分支的加固也不许假设"`main` 不会有第二条路径"——本次事故（§1.2）就是本分支的
   `attachMainWindow` 与 `main` #1503 的懒解析各自都装了一次 hook，两边单跑都正常、合并后启动即崩。
3. 当 `main` 的功能与不变量**真的不可兼得**（例如某功能必须在启动时同步阻塞等网络）：
   - 默认选择保留功能，但必须改成"启动后异步/可重试/超时可降级"，绝不允许启动期无限阻塞或 fatal；
   - 在 PR 描述里写明取舍与补偿措施，并在本文档 §5 记录。
4. **禁止**的合并手法：
   - `git checkout --ours/--theirs` 整片覆盖加固改动；
   - 为了"减少冲突"把 `SecureJoin` / 签名校验 / redaction / `sync.Once` 收口删掉；
   - 在冲突处留下两条并行的安装/校验路径（本次事故的同类形态）；
   - 只跑 `go build` 就宣布合并成功。
5. 合并后**必须**为被冲突破坏的不变量补测试；加固分支的既有测试不许为了"跟上 main"而放宽或跳过
   （`t.Skip` 需要在本文件记录理由）。

### 3.3 合并后：必须执行的验证清单

```bash
# 1) 编译与静态检查
go build ./...
go vet ./...

# 2) 单测（全量；至少覆盖 app 层 + backend/...）
go test -count=1 ./
go test -count=1 ./backend/...

# 3) 前端
cd frontend && npx vue-tsc --noEmit && npx vitest run && npm run build && cd ..

# 4) 真实启动冒烟（关键，缺一步都算没测）
task dev                 # 或 wails3 dev -config ./build/config.yml -port 9245
#   窗口必须出现；日志里不能有 Exception / fatal error / panic；
#   连续操作 30s（拖拽窗口、切标签、打开连接面板）不崩；
#   退出后 tasklist 里不能残留进程（残留会挡住下次启动）

# 5) server 模式冒烟（headless 路径与桌面路径不同）
task run:server
```

### 3.4 合并提交形式

- 合并提交信息写清楚：`Merge origin/main into <branch>` + 冲突点的取舍说明（哪个不变量、为什么这样解）。
- 合并提交里**不要**夹带与本合并无关的重构；发现的问题单独提交。
- 若合并引入了回归，修复合并在同一个 PR 里（像本次 `fix(win)` 一样），并回填本文档 §5。

---

## 4. 建议的 CI 冒烟任务（未实现，供排期）

- Windows/macOS/Linux 各跑一次"构建 + 启动 30s + 发一条窗口消息 + 优雅退出"的冒烟脚本，
  失败即红（`Exception 0xc00000fd` 这类问题只会在真实启动时暴露）。
- 冒烟日志里保留 goroutine dump（已有 `panic-stderr.log` 捕获机制，见 CHANGELOG）。

---

## 5. 状态跟踪 / 事故记录

| 日期 | 问题 | 分支/来源 | 处理 |
| --- | --- | --- | --- |
| 2026-11-19 | Windows 启动即 `0xc00000fd`（double subclass → 自递归 → 爆栈） | `8573ff56`（本分支的 IME 钩子）+ `e83e0d88`（`main` 的懒解析钩子）在 `sec/high-fixes` 里叠加 | 已修复：`installWndProcSubclass` 单次安装 + 自递归保险 + `TestSubclassMainWindowInstallsOnlyOnce`；`main` 单独不受影响，但建议同步守卫 |
