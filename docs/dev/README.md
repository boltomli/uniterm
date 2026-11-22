# 开发文档索引（设计记录 / 审查报告）

本目录保存**开发侧**的设计记录与审查报告。它们不是用户手册（用户手册在 `docs/guide/`），
也不是长期有效的规范——每份文档都冻结在它自己的**审查基线**上。阅读时先看这里的“状态”列，
再回到代码确认当前行为。

## 文档状态

| 文档 | 日期 | 基线 | 主题 | 状态 |
|---|---|---|---|---|
| `2026-10-12-store-persistence-unification.md` | 2026-10-12 | — | Store 持久层统一（信封、SecretFiles manifest、原子写） | **已实施**（遗留项已于后续提交解决，见文内 2026-11-22 注） |
| `2026-10-18-security-audit-findings.md` | 2026-10-18 | `1e358761` | 全仓安全审查（F-01…F-12） | **已完成**（12 项全部修复；行号已冻结） |
| `2026-10-24-terminal-perf-audit-findings.md` | 2026-10-24 | `ab5227e3` | 终端输出性能/正确性（P-xx、S-xx） | **已完成**（后续有合并带来的新优化） |
| `2026-10-25-database-regression-audit.md` | 2026-10-25 | `9f61eb7a` | 数据库功能回归复盘（DB-xx） | **已完成**；部分后续被上游改写（见下） |
| `2026-11-19-startup-safety-invariants.md` | 2026-11-19 | `1a95a91d` | 启动/native 回调安全不变量 | **现行规范**（规则仍生效） |
| `2026-11-21-ime-double-commit.md` | 2026-11-21 | `3b28b021` | Windows IME 双打复盘 | **现行规范**（规则仍生效） |
| `2026-11-22-new-feature-audit.md` | 2026-11-22 | `b300655d` | 合入 main 后的新功能安全/性能审查（A-01…A-12） | **已完成**（12 项全部修复） |
| `go-fork-baselines.md` | 2026-10-19 | — | Go fork 依赖基线与 advisory 扫描 | **现行规范**（依赖升级时更新） |

## 阅读须知

- **行号会漂移**。旧文档里的 `file:line` 只对它的基线提交有效；用文中的函数名/符号名定位。
- **状态会变化**。文档写“已修复/已完成”只代表当时；后续提交可能改写同一代码。改相关代码前先跑一遍
  文档末尾的验证命令，并检查本文档的“后续变更”一节。
- **不要按旧文档的“执行须知”操作新代码**。例如旧文档提到 `frontend/wailsjs/go/models.ts`，
  当前 Wails v3 绑定在 `frontend/bindings/github.com/ys-ll/uniterm/app`（gitignored）。

## 2026-11-22 合并后的已知变更（与旧文档不一致之处）

以下变更发生在旧审查文档的基线之后，阅读旧文档时需要叠加：

- **PostgreSQL 驱动**：`lib/pq` → `pgx v5 stdlib`（`DriverName()` 返回 `pgx`；占位符分支同时匹配）。
- **SQL Server 分页**：`OFFSET … FETCH` → `ROW_NUMBER() OVER (…)`（兼容 SQL Server 2008），
  因此 `2026-10-25-database-regression-audit.md` 中关于 OFFSET/FETCH 的个别结论已过时。
- **分页修复**：SQLite/rqlite `GetTablePage` 的 LIMIT/OFFSET 绑定顺序已修正。
- **新连接类型**：`local-file`、独立 `wsl-file`；server 模式统一拒绝 `local`/`local-file`/`wsl-file`。
- **全类型代理**：VNC/SPICE/FTP/SMB/TCP/WebDAV/S3/Elasticsearch/Redis/MongoDB/数据库/K8s 均走
  `dialFirstHop`；MySQL 的驱动注册表按代理身份复用。
- **同步**：快照后端（WebDAV/SFTP/S3/SMB）+ 按 scope 记录锚点 + 跨设备 scope 合并。
- **RDP**：引擎 0.5.5（vendor tarball），Credentials/CredSSP hex dump 已移除，证书 TOFU pin。
- **存储**：`decryptFailed` 状态与重新解锁流程已落地；`clearEncryptedFields` 与
  `storeHasSecrets` 已改由 `store.SecretFiles` manifest 派生。

## 当前约束去哪里找

- 项目级约定：根 `AGENTS.md` 与各子目录 `AGENTS.md`
- 安全不变量：`docs/dev/2026-10-18-security-audit-findings.md` §0（仍有效的加固清单）+
  `2026-11-19`、`2026-11-21` 的现行规则
- 新功能现状：`2026-11-22-new-feature-audit.md`（含每项修复提交）
- 依赖基线：`go-fork-baselines.md`（每次 bump 依赖时更新）

## 验证命令

```bash
go build ./... && go vet ./... && go test ./...
cd frontend && npm run typecheck && npx vitest run && npm run build
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
python3 scripts/check-fork-advisories.py     # 仅 fork 依赖相关改动
```
