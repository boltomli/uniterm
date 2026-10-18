# Go 依赖 Fork 基线记录（F-12.8）

> 日期：2026-10-19
> 背景：`go.mod` 中有三个 `replace` 指向个人 fork。**govulncheck 按模块名匹配
> advisory，被 replace 的模块在扫描中按 fork 名出现，导致上游 CVE 在扫描结果里
> "消失"**（2026-10-18 安全审查中 `golang.org/x/crypto` 完全未出现在报告里）。
> 本文档记录每个 fork 的上游基线，升级时必须同步跟随；CI 用
> `scripts/check-fork-advisories.py` 按**上游模块名 + 版本**查询 OSV 补漏。

## Fork 清单

| 上游模块 | 上游基线版本 | fork | fork 版本 / 提交 | fork 原因 |
|---|---|---|---|---|
| `golang.org/x/crypto` | **v0.57.1**（`go.mod` require 行为 v0.57.0，fork 伪版本基于 v0.57.1） | `github.com/ys-ll/crypto` | `v0.57.1-0.20260922023551-18e0be45963e` | 维护 X11/mosh 相关补丁（详见提交历史）；**不要随手改回上游**，除非同时评估 X11/mosh 功能 |
| `github.com/rhnvrm/simples3` | **v0.11.1** | `github.com/ys-ll/simples3` | `v0.0.0-20260930081831-3357c14e91bd` | 上游不再维护的修复/适配 |
| `github.com/unixshells/mosh-go` | **v0.5.2** | `github.com/ys-ll/mosh-go` | `v0.0.0-20260702130124-e17b92cd9dab` | mosh 序列 Buffer/连接行为补丁 |

## 升级流程（每次 bump 依赖时执行）

1. 升级上游 require 版本（`go get golang.org/x/crypto@<新版>` 等）。
2. 同步 rebase fork 到新的上游基线，并更新本文档的"上游基线版本"与
   "fork 版本"两列（fork 仓库：`github.com/ys-ll/<name>`）。
3. 本地跑扫描（CI 同款）：
   ```bash
   go run golang.org/x/vuln/cmd/govulncheck@latest ./...
   python3 scripts/check-fork-advisories.py     # 按上游名查 OSV，覆盖 fork 盲区
   ```
4. 验证功能回归：`go test ./backend/...`（X11/mosh 相关用例）+ 全量 `go test ./...`。

## CI 对比扫描

`.github/workflows/security.yml` 每次 push/PR/周调度运行
`scripts/check-fork-advisories.py`：脚本解析 `go.mod` 的 require/replace，
把每个 fork 还原成"上游模块名 @ 上游基线版本"，查询 OSV API
（`https://api.osv.dev/v1/querybatch`，Go ecosystem），命中即非零退出。
该步骤独立于 govulncheck，专补 fork 造成的 advisory 盲区。
