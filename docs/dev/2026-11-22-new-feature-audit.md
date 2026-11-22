# 新功能安全与性能审查（合入 `main` 后）

> 日期：2026-11-22
> 审查基线：`b300655d`（`sec/high-fixes`，已合入 `origin/main` 至 `ea540975`）
> 范围：本次合并引入的 69 个上游提交（`207b695a..origin/main`）所涉功能：
> local-file / WSL-file 连接类型、全类型代理、mosh 端口/语言、会话日志自动开始、
> AI 发送快捷键、托盘最小化与清屏策略、TOTP 自动填充、技能跨设备同步、
> PostgreSQL pgx 驱动、SQL Server 分页、RDP 引擎 0.5.5、构建版本注入。
> 性质：审查阶段**未修改任何业务代码**（临时复现测试已删除）。所有行号以 `b300655d` 为准，
> 定位优先用函数名。
>
> **修复：2026-11-22 已按本清单完成全部 12 项**（A-10 为文档化决策，A-05 的 echo 残留以代码注释记录），
> 每项独立提交，见 §4。验证：`go build ./...`、`go vet ./...`、`go test ./...`（2137 passed）、
> `govulncheck ./...`（0 可达 / 0 模块级）、`npm run typecheck`、`npm run build`、`npm audit --omit=dev`（0）。

---

## 0. 方法与验证

- 逐提交阅读 `207b695a..origin/main` 的功能性 diff，再回到合并后的最终实现确认不变量；
- 对两个关键结论做了可执行复现（见 A-01、A-02）；
- 复跑基础校验：`go build ./...`、`go vet ./...`、`go test ./...`（2129 passed）、
  `npm run typecheck`、`npm run build` 全绿；
- 依赖扫描：`govulncheck ./...`、`npm audit --omit=dev`（0 vulnerabilities）。

审查中确认**未回退**的既有加固（不应在修复时破坏）：

- FTP `dialFTPGuarded` 的 AUTH TLS 降级策略（只在 500/502/504 + FEAT 复核后降级）；
- 会话日志目录 `0700`、文件 `0600`、文件名 `sanitizeLogName` 清洗、目录需 `requireDirGrant`；
- RDP 服务端证书 TOFU 固定（`rdp_pins.go`），CredSSP 逐消息 hex dump 已移除；
- `local-file` 在 server 模式下被禁用（`app_terminal.go:35`）；
- VNC/SPICE 本地代理的 per-listen token 与 Stop 语义；
- AI 侧 `applySessionHeader` 走 Go 的 `http.Header` 校验，无 CRLF 注入面。

---

## 1. 问题总表

| ID | 等级 | 类别 | 一句话 | 主位置 |
|---|---|---|---|---|
| A-01 | **严重** | 数据丢失 | 本地文件会话的递归删除根路径校验在 Windows 上失效，删除盘符行会触发 `os.RemoveAll("C:\\")` | `backend/session/local_file_session.go:239-246` |
| A-02 | **高** | 数据一致性/性能 | 窄范围设备推送后记录的锚点包含“带过来的”域外条目，与下次比较口径不一致，空闲也会永久报冲突并反复全量下载 | `backend/sync/snapshot_sync.go:58,186-193,284` |
| A-03 | 中 | 内存/性能 | `GetContent` 无大小上限，本地大文件经 base64 IPC 读取可打爆内存 | `backend/session/local_file_session.go:275`、`app_file_transfer.go:447` |
| A-04 | 中 | 性能/体验 | 传输开始前在绑定调用内同步遍历整棵目录树计算总量，大目录下 `Get/Put` 迟迟不返回，且与随后的复制重复遍历 | `backend/session/local_file_session.go:322-331`、`wsl_file_session.go:679` |
| A-05 | 低 | 凭据卫生 | TOTP 自动填充不再要求 `echo=false`，验证码会进入回滚缓冲与会话日志；MFA 选项识别是黑名单 | `backend/session/totp.go:98-110` |
| A-06 | 低 | 资源泄漏 | MySQL 代理拨号器每次连接都向 driver 全局注册表新增一项且从不注销 | `backend/database/engine.go:149-156` |
| A-07 | 低 | 可靠性 | WebDAV 代理 transport 未设置任何超时，代理端口“accept 后不转发”会挂住文件面板调用 | `backend/session/webdav_session.go:79` |
| A-08 | 低 | 性能 | Windows 列出目录时每个条目额外一次 `GetFileAttributes`；WSL UNC 上是每条目一次网络往返 | `local_file_session.go:143-160`、`local_hidden_windows.go` |
| A-09 | 低 | 正确性 | `dirSize` 不跟随符号链接目录（按链接大小计数），与 `copyTree` 的 Lstat 语义不一致，进度总量偏小 | `local_file_session.go:478-508` |
| A-10 | 信息 | 信任边界 | `local-file` 把整个本地文件系统暴露给渲染层且不走 `fileGrants`（与 F-04 的本地面板授权模型不一致，但与本机 shell 能力等价） | `backend/session/local_file_session.go`、`app_terminal.go:27-37` |
| A-11 | 信息 | 一致性 | server 模式禁用名单只含 `local`/`local-file`，未含 `wsl-file` | `app_terminal.go:35` |
| A-12 | 低 | 依赖 | `govulncheck` 报 2 条可达漏洞：brotli 1.0.6、klauspost/compress 1.18.7（合并前即存在） | `go.mod:62,99` |

---

## 2. 详细说明

### A-01（严重）本地文件递归删除的根路径校验在 Windows 上失效

**位置**：`backend/session/local_file_session.go:239-246`

```go
func (s *LocalFileSession) Remove(p string, recursive bool) error {
	target := s.remoteOSPath(s.resolveRemote(p))
	if target == "" || target == "/" || filepath.VolumeName(target) == target {
		return fmt.Errorf("refusing to delete path: %s", p)
	}
	if recursive {
		return os.RemoveAll(target)
	}
```

**问题**：`remoteOSPath` 做 `filepath.FromSlash`，Windows 下用户态路径的根是 `\` 而不是 `/`，
且 `VolumeName("C:\\")` 返回 `"C:"`（不等于 `"C:\\"`）。因此三个条件全部放行：

| 输入 `p` | `resolveRemote` | `target`（Windows） | 守卫结果 |
|---|---|---|---|
| `/` | `/` | `\` | **放行** |
| `\`、`//` | `/` | `\` | **放行** |
| `/C:`（盘符行拼接结果） | `C:/` | `C:\` | **放行** |
| `C:` | `C:/` | `C:\` | **放行** |

于是 `os.RemoveAll("\")` / `os.RemoveAll("C:\\")` 会以当前用户权限递归删除当前盘根目录或整盘可写内容。

**可达路径（正常 UI）**：`local-file` 连接的“远端”窗格在根 `/` 会渲染盘符行（`listRemoteDrives`，
条目名 `C:`，`IsDir=true`）。`useFilePanel.onDelete` 对目录行调用
`ops.remove(id, joinPath(cwd, item.name), item.isDir)` → `SftpRemove(..., "/C:", true)` →
上述守卫放行。即：右键盘符 `C:` → 删除 → 确认，就会尝试清空整盘（先删到无权限处才停）。
`SftpRemove` 也是可直接调用的绑定，渲染层一次调用即可触发。

WSL 会话的 `Remove` 用 `c == "/"` 判断（其根是 `//wsl.localhost/<distro>`），不受影响；
问题只在新加的 `LocalFileSession`。

**修复建议**：抽一个共用判定，按 `filepath.Clean` 之后的平台形态比较，并让 `Move`/`Rename`
的旧路径也走同一判定（`Move` 的复制回退同样可能对根路径执行 `RemoveAll`）：

```go
func refuseRootPath(target string) error {
	clean := filepath.Clean(target)
	vol := filepath.VolumeName(clean)
	sep := string(filepath.Separator)
	if clean == "" || clean == sep ||
		(vol != "" && (strings.EqualFold(clean, vol) || strings.EqualFold(clean, vol+sep))) {
		return fmt.Errorf("refusing to operate on filesystem root: %s", target)
	}
	return nil
}
```

**回归测试**：不要真的调用 `RemoveAll`。把守卫抽成纯函数（或测试 `refuseRootPath`），
表驱动覆盖 `\`、`/`、`C:`、`C:\`、`\\server\share`、`/C:`、正常子路径；Windows 下断言
`removeAll` 依赖被注入的 seam 拦住。

---

### A-02（高）窄范围推送后同步锚点与比较口径不一致 → 永久冲突 + 反复全量下载

**位置**：`backend/sync/snapshot_sync.go:58`（比较用 `scopedAggregateHash`）、
`:186-193`（携带域外条目后重算 `agg`）、`:284`（把含域外条目的 `agg` 记为锚点）

**问题**：`pushSnapshot` 为了不“驱逐”其他设备的 blob，会把远端 manifest 里**本机范围之外**
的条目拼进新 manifest（正确），但同时把 `agg` 重算为 `aggregateHash(hashes)`（包含这些带过来的条目）。
这个 `agg` 被写成 `LastSyncedHash`。而 `syncSnapshot` 下次比较时：

```go
remoteHash := scopedAggregateHash(manifest.FileHashes, files) // 只算本机 scope
```

于是 `last`（含域外条目）永远不等于 `remoteHash`（只含本机 scope），
即使本地、远端都没有任何变化：

- 快速路径 `localHash == last && remoteHash == last` 永不命中；
- `localHash == last` / `remoteHash == last` 的单边分支也都不命中；
- `scopeWidenedSince` 在范围未变时为 false；
- 落到 `default` → **每次同步都返回冲突**（`sync:conflict` 事件 → 前端冲突弹窗），
  且冲突前已经 `FetchAll` 了整份远端快照（带宽/耗时）。

更糟的是冲突时选“使用本地”会再次走 `pushSnapshot` 并再次把域外条目算进锚点，
**永远无法收敛**；选择“使用远端”才能恢复。

**复现（本次已跑通，临时测试已删除）**：

```go
s, dataDir, key := newSnapshotTestService(t, []string{"connections.json"}) // 窄 scope
writeConfig(t, dataDir, "connections.json", `{"v":1}`)
b := newFakeSnapshotBackend()
s.syncSnapshot(b)                                  // bootstrap push

// 宽 scope 设备推入 skills.json（同 connections 内容）
... EncryptConfigFilesScoped([]string{"connections.json","skills.json"}, srcDir, staging, key)
... b.StoreAll(staging, salt, SnapshotInfo{FileHashes: hashes})

s.syncSnapshot(b)                                  // no-op，正常
writeConfig(t, dataDir, "connections.json", `{"v":2}`)
s.syncSnapshot(b)                                  // push，锚点被写成含 skills 的聚合

res, _ := s.syncSnapshot(b)                        // 什么都没改
// 实际：res.Direction == SyncConflict(3)；期望：SyncNone
```

`changePasswordSnapshot`（`:527`）已经用了正确写法 `scopedAggregateHash(hashes, files)`，
说明口径在本文件内已经不一致。

**修复建议**：不要覆盖锚点变量。manifest 用含域外条目的哈希，锚点始终用本机 scope：

```go
hashes, anchor, err := hashStoresScoped(s.stores, files) // anchor 是 scoped
...
if carried {
	manifestHashes := hashes // 已含带过来的条目
}
// StoreAll(..., FileHashes: hashes)
s.setLastSyncedHash(anchor, files)   // 不传重算后的 aggregateHash
```

或直接 `s.setLastSyncedHash(scopedAggregateHash(hashes, files), files)`。
补一个“窄 scope 推送后空闲再同步 = SyncNone”的回归测试（现有新测试只覆盖推送本身）。

---

### A-03（中）`GetContent` 无上限读取，本地大文件可打爆内存

**位置**：`backend/session/local_file_session.go:275`、`app_file_transfer.go:447-457`

```go
func (s *LocalFileSession) GetContent(remotePath string) ([]byte, error) {
	return os.ReadFile(s.remoteOSPath(s.resolveRemote(remotePath)))
}
...
return base64.StdEncoding.EncodeToString(content), nil
```

`local-file` 让“远端”直接等于本机文件系统，用户/渲染层点开一个几 GB 的镜像或日志即可让
Go 侧同时持有 `原始字节 + base64(+33%) + JSON` 三份数据，桌面端直接 OOM；
Windows 上还会因为一次 IPC 巨块导致前端长时间无响应。

同类无上限读取在 SFTP/S3/WebDAV/WSL 中也存在（属既有系统性问题），但新类型把触发门槛降到
“打开任意本地大文件”。SFTP 侧有 25s 超时，只挡住“慢”不挡“大”。

**修复建议**：在绑定层或 `GetContent` 层加统一上限（例如 8–32 MiB），超限返回明确错误；
更彻底的做法是给编辑器增加 range/stream 接口（`SftpGetRange`），二进制/大文件走下载任务而不是
`GetContent`。前端在打开前用 `size` 预判并提示。

---

### A-04（中）传输前同步全树扫描，阻塞 `Get/Put` 调用

**位置**：`backend/session/local_file_session.go:322-331`、`backend/session/wsl_file_session.go:679`

```go
total, err := dirSize(src)
if err != nil { return "", err }
```

`dirSize` 在返回 task id 之前同步遍历整棵目录（每个目录一次 `ReadDir` + 每个条目一次
`Info`）。大目录（数十万文件、或 WSL UNC 网络往返）会让前端 `SftpGet/SftpPut` 的 await
卡住几十秒到分钟级，传输进度条根本没有出现；随后 `copyPath` 又完整遍历一遍，等于双倍 I/O。
`dirSize` 也没有 `transferCtxErr`，取消/暂停在预扫描阶段完全无效。

**修复建议**：
- 预扫描放进 goroutine，先返回 task id，`Total` 稍后通过 progress 事件补发（`task.loadTotal()`
  已有“未知总量”路径）；或
- 只在“文件”场景直接 `Stat`，目录场景延后到复制过程中累加；
- 至少让 `dirSize` 接受 `ctx/task` 并在每个目录检查取消。

---

### A-05（低）TOTP 自动填充放宽 echo 条件后的凭据卫生问题

**位置**：`backend/session/totp.go:98-110`

`automaticTOTPChallenge` 现在只要问题匹配 `\b(?:totp|otp)\b` 等（且不匹配选项黑名单）就回填验证码，
不再要求 `echos[i] == false`。功能上合理（堡垒机常用 echo=true），但：

- 验证码会被终端回显，进入滚动缓冲，并在开启会话日志/自动记录时**落盘到明文日志**（文件 0600，
  但可能被同步/备份/导出）。TOTP 有效期通常 30s，影响有限，但违反“验证码不回显”的常规预期。
- MFA 选项识别是黑名单（`select/choose/option/…`）。未覆盖的菜单（例如 `"1> "`、`"Enter 1 or 2: "`）
  仍会把验证码当选项发出去，导致认证失败/验证码被消耗。

**建议**：回填前对 echo=true 的 OTP 问题给出一次性提示（或在会话日志中打码 6 位数字）；
把“选项提示”识别改成“期望回答是短数字/单选”的正向判定，或同时匹配 `(1|2|3)\.`、`[1-9]>` 等形态。

---

### A-06（低）MySQL 代理拨号器全局注册只增不减

**位置**：`backend/database/engine.go:149-156`

`mysql.RegisterDialContext(netName, …)` 写入 driver 包级 `dialers` map，驱动没有反注册 API。
每次带代理的 DB 连接都会新增 `uniterm-proxy-N` 一项；长时间运行的实例 + 反复重连会持续增长
（每项很小，但无上限）。同一代理的多个连接可以复用同一个注册名，或改为
`mysql.NewConnector` + `cfg.Dialer`（若驱动版本支持）避免全局表。

---

### A-07（低）WebDAV 代理 transport 无超时

**位置**：`backend/session/webdav_session.go:74-80`

```go
client.SetTransport(&http.Transport{Proxy: ProxyHTTPTransportProxy(config.Proxy)})
```

新建的 `http.Transport` 没有 `DialContext`/`TLSHandshakeTimeout`/`ResponseHeaderTimeout`，
代理若“accept 后不转发”，`ListRemote`/`GetContent` 会无限挂起（面板一直 loading，占用 goroutine）。
S3 侧用的是 `http.DefaultTransport.Clone()`，建议 WebDAV 同样 clone 后只替换 `Proxy`，并补
`ResponseHeaderTimeout`。

---

### A-08（低）隐藏属性检查的逐条目系统调用

**位置**：`backend/session/local_file_session.go:143-160`、`local_hidden_windows.go`

`fileItemsFromDir` 对每个不以 `.` 开头的条目调用 `isPathHidden`，Windows 下即一次
`GetFileAttributes`；WSL 的 `\\wsl.localhost` 共享上每次都是一次 9P/网络往返。
目录条目到万级时，列表明显变慢。可以只在用户开启“显示隐藏文件”时懒加载，或批量读取
`FindFirstFile` 的 `FILE_ATTRIBUTE_HIDDEN`（`os.ReadDir` 在 Windows 已内置该字段但未暴露）。

---

### A-09（低）`dirSize` 与 `copyTree` 的符号链接语义不一致

`dirSize` 用 `e.Info()`（返回链接自身信息）判断 `!IsDir()` 后直接累加 `Size()`，所以
**指向目录的符号链接按链接大小计**；`copyTree` 则用 `Lstat` + `copyLocalSymlink` 物化/重建链接。
结果是传输进度总量偏小（甚至为 0），UI 上进度百分比失真。建议 `dirSize` 与 `copyTree` 共用
同一个遍历器（`Lstat` + 循环检测），或直接采用 A-04 的“边复制边累计”方案，彻底去掉预扫描。

---

### A-10（信息）`local-file` 的信任边界应与本机 shell 一致并写清楚

新类型把本机文件系统的读/写/删除/建链能力直接暴露给渲染层，且与既有 `fileGrants` 模型
（F-04：`SftpListLocal`/`SftpLocalRemove` 等要求目录授权）不一致——通过
`CreateSession('local-file')` + `SftpGetContent/PutContent/Remove` 可在**无用户对话框**的情况下
读写任意本机路径。

由于桌面模式下 `CreateSession('local')` 本来就能起本机 shell（等价能力），这不是新增的
隔离突破；但两点需要明确：

1. 这是有意为之还是遗漏？若是前者，建议在 `docs/security` 与类型注释中写明
   “渲染层 = 本机完全信任”的边界，别让后续审查误以为 `fileGrants` 覆盖了所有本地面板路径。
2. server 模式已禁用（正确）。若未来加入别的“非本机”渲染来源，该类型必须一并禁用。

---

### A-11（信息）`wsl-file` 未纳入 server 模式禁用名单

`app_terminal.go:35` 只拦截 `local` / `local-file`。Linux server 构建里 `wsl.exe` 不存在会失败，
但判定依赖运行环境而不是显式策略。建议把 `wsl-file` 一起列进禁用名单（或在 manager 层拒绝
非网络类型），保持“server 模式不得访问宿主机文件系统”的显式不变量。

---

### A-12（低）依赖可达漏洞（合并前已存在）

`govulncheck ./...`：

| 漏洞 | 模块 | 当前 | 修复版本 | 可达路径 |
|---|---|---|---|---|
| GO-2026-6888 | `github.com/andybalholm/brotli` | v1.0.6 | v1.2.7 | `backend/session/rdp_session.go` → utls → brotli；`monitor_session.go` |
| GO-2026-6865 | `github.com/klauspost/compress` | v1.18.7 | v1.20.2 | `mongodb_session.go` → mongo-driver → fse/huff0 |

二者均为间接依赖，合并前版本相同（非本次引入），但当前代码可达。
`golang.org/x/net v0.60.0` 另有 2 条仅模块级漏洞（`govulncheck` 判定不可达）。
修复时连同 x/net 一并升到 v0.62.0，升级后 `govulncheck ./...` 为
**0 可达 / 0 模块级**；`npm audit --omit=dev` 为 0。

建议单独提交 `fix(deps):` 升级 brotli/compress（MVS 允许提升间接依赖；升级后重跑 `go test ./...`
与 govulncheck 确认 utls fork 与 mongo-driver 兼容）。注意 `go.mod` 里 utls 是 fork replace，
不要随手改回上游。

---

## 3. 已检查且本次未发现问题的点（供后续对照）

- FTP：`dialFTPGuarded` + `ftpProbeAuthTLS` 的降级判定完整保留；新增的 `CWD <dir> + LIST ""`
  行为在 `connMu` 内串行，不会与传输交叉；仅要求控制连接上的服务端 cwd 漂移不影响绝对路径操作。
- 会话日志：目录 `0700`、文件 `0600`、`O_EXCL`、文件名模板 `%S/%H/%Y/...` 全部经 `sanitizeLogName`
  清洗，无法目录穿越；目录覆盖仍需 `requireDirGrant`。
- 代理：`materializeProxy` 在 `CreateSession`/`TestConnection` 均先解析，`system` 哨兵按目标解析；
  代理不可达时直接失败而不是静默直连（与提交说明一致）。MongoDB 的 `SetDialer` 不影响 TLS 层。
- mosh：`buildMoshServerCommand` 的 `safeMoshArg` 只允许 `[A-Za-z0-9_.:@-]`，`-p/-l` 值无法注入 shell。
- AI：Enter 发送前有 `e.isComposing || e.keyCode === 229` 保护；`sendKey` 仅影响发送组合键；
  `SummarizeAIContext` 的 session header 经 `http.Header` 校验，无 CRLF 注入。
- SQL Server 分页：`ROW_NUMBER() OVER (...)` 的排序列来自 `requireColumn` + `Quote`，过滤值走占位符；
  `GetTablePage` 的 WHERE 位于窗口函数之前，语义正确。
- pgx：DSN 显式设置 `sslmode`（默认 `prefer`），与 lib/pq 时代一致；`DriverName()` 为 `pgx` 后
  占位符分支已同时匹配 `postgres`/`pgx`，未发现按 driver 名分支遗漏。
- 托盘最小化：仅改变最小化去向，关闭路径仍退出；会话在托盘期间保持运行（文档已说明）。
- 构建版本注入：`VERSION` 只来自 tag/release，`case` 过滤后传入 Taskfile；可执行性依赖仓库写权限，
  风险可接受（可再加 SemVer 正则做纵深防御）。

---

## 4. 状态跟踪

| ID | 等级 | 状态 | 修复提交 |
|---|---|---|---|
| A-01 | 严重 | 已修复 | `d02fab76` |
| A-02 | 高 | 已修复 | `9fb50c3f` |
| A-03 | 中 | 已修复 | `e5032d69` |
| A-04 | 中 | 已修复 | `9d783e87` |
| A-05 | 低 | 已修复（echo/日志残留已注释） | `de662ee6` |
| A-06 | 低 | 已修复 | `ddfb3630` |
| A-07 | 低 | 已修复 | `adaadcf6` |
| A-08 | 低 | 已修复 | `65cba270` |
| A-09 | 低 | 已修复 | `e1e219fb` |
| A-10 | 信息 | 已文档化（信任边界写入类型注释） | `7007aa34` |
| A-11 | 信息 | 已修复 | `7007aa34` |
| A-12 | 低 | 已修复 | `50e27661` |

> 修复已完成并推送。依赖升级后 `govulncheck` 无任何输出项；新增回归测试覆盖
> A-01（根路径判定，不执行真实删除）、A-02（窄 scope 推送后空闲同步 = SyncNone）、
> A-03（读取上限）、A-04（目录传输）、A-06（注册表上界）、A-09（符号链接与环）、
> A-10/A-11（session 类型判定）。
