# Security Policy / 安全策略

## Supported versions / 支持的版本

| Version / 版本 | Supported |
|---|---|
| Latest release (最新发布版本) | ✅ |
| main branch (主分支) | ✅ |
| Older releases (旧版本) | ❌ 请升级到最新版 / please upgrade to the latest release |

---

## Reporting a vulnerability / 报告漏洞

**English:**

Please do NOT open a public issue for security vulnerabilities. Use GitHub's
private reporting instead:

→ **https://github.com/ys-ll/uniterm/security/advisories/new**

Include: affected version/commit, reproduction steps, impact, and any
suggested fix. You should receive an acknowledgement within a few working
days; fixes for confirmed issues are released through the normal release
cycle (sooner for high-severity issues). Please keep the report private
until a fix is available (coordinated disclosure).

**中文：**

安全漏洞请**不要**公开提交 issue，请使用 GitHub 私密报告：

→ **https://github.com/ys-ll/uniterm/security/advisories/new**

请附上：受影响的版本/commit、复现步骤、影响范围以及修复建议。通常会在几个
工作日内收到确认回复；确认的问题会随正常发布周期修复（高危问题会加速发布）。
修复发布前请保密（协同披露）。

---

## Security design notes / 安全设计说明

uniTerm 会接触凭据、终端与真实基础设施，因此在多层做了加固（修改这些区域前
请先阅读对应文件内的 `security.md` / `F-xxx` / `SEC-xx` 注释）：

uniTerm handles credentials, terminals and real infrastructure, so hardening
is layered (read the `security.md` / `F-xxx` / `SEC-xx` comments in the
touched files before changing them):

- **File access / 文件访问**: dialog/drop-based path grants
  (`file_grants.go`), `SafeLocalJoin`/`SafeRemoteName` path validation,
  symlink-resolved containment for the SFTP local pane.
- **Credentials / 凭据**: encrypted at rest, redacted from every
  renderer-facing listing (`LoadConnections`/`LoadIdentities`/`LoadProxies`);
  model API keys never leave the Go side (renderer sees `***`).
- **Updates / 更新**: Ed25519 signature + SHA256 + host allowlist chain
  (`backend/update/*`) — do not weaken it.
- **SSH host keys**: TOFU fail-closed (`backend/session/knownhosts.go`).
- **AI execution / AI 执行**: default policy `confirm_write`; the local risk
  classifier is mirrored in Go and TypeScript with a shared fixture
  (`backend/mcp/testdata/risk_cases.json`) so the two copies cannot drift;
  external MCP tools and agent skill saves require confirmation.
- **MCP server**: loopback-only listener, per-client bearer tokens with
  optional scopes (`exec` / `files` / `readonly`), Origin/Host guards, body
  limits, approval rate limiting, size-rotated audit log.

### Known trade-offs / 已知取舍

- **Read-only MCP tools (F-12.11)**: under the default policies
  (`confirm_write`), the MCP server's read-class tools —
  `uniterm_capture` (terminal screen snapshot) and `get_command_output`
  (command output) — execute without a per-call dialog because they carry no
  side effects; a screen or output buffer may however contain passwords.
  Mitigations: set the MCP policy to `confirm_all`
  (Settings → MCP), issue scope-restricted tokens, or disable the MCP
  endpoint entirely.
  默认策略（`confirm_write`）下 MCP 的只读工具（`uniterm_capture` 屏幕快照、
  `get_command_output` 命令输出）不弹单次确认，但屏幕/输出可能含密码。缓解：
  将 MCP 策略设为 `confirm_all`、签发受限 scope 的 token，或关闭 MCP 端点。
- **Terminal browsing / 终端浏览**: the SFTP local pane can *browse* the
  whole disk, but every mutating/content operation requires an explicitly
  granted directory (native picker); OSC 52 clipboard writes ask by default.

## Dependency scanning / 依赖扫描

- CI runs `govulncheck`, `gosec`, `npm audit --omit=dev` and an upstream
  advisory lookup for forked Go modules
  (`.github/workflows/security.yml`; currently non-blocking).
- Three `replace` directives point at personal forks (`x/crypto`,
  `simples3`, `mosh-go`) which plain govulncheck cannot see under their
  upstream names — baselines and the upgrade procedure are tracked in
  [`docs/dev/go-fork-baselines.md`](docs/dev/go-fork-baselines.md).
- Dependabot opens weekly bump PRs (`.github/dependabot.yml`).
