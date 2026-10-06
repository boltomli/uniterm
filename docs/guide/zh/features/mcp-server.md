# MCP Server(外部 AI Agent)

uniTerm 可以作为 MCP(Model Context Protocol)服务器运行,让 Claude Code、Codex、ZCode、Kimi、Gemini CLI、Trae、WorkBuddy、DeepSeek 等外部 AI Agent **直接在 uniTerm 管理的远程主机上执行命令、读取输出、传输文件**。

核心原则:**凭据永远留在 uniTerm**。Agent 拿不到任何密码或私钥——它只是向 uniTerm 发起请求,由 uniTerm 用已保存的连接代为执行,每一步都经过审批与审计。

## 工作原理

```
外部 Agent(Claude Code / Codex / ...)
      │  MCP over Streamable HTTP + Bearer Token
      ▼
uniTerm(127.0.0.1:61207/mcp)
      │  审批弹窗 → 策略引擎 → 审计日志
      ▼
已保存的 SSH 连接(凭据不出应用)
```

- **仅监听本机回环地址**(127.0.0.1),外部网络无法访问
- **每个客户端一枚 Bearer 令牌**,明文只在生成时显示一次,服务端只存哈希
- 所有调用记录在 `<数据目录>/mcp-audit.log`(JSONL),包含客户端名、命令、退出码

## 启用步骤

### 1. 打开 MCP 服务器

设置(`⌘,`)→ **MCP 服务器**:

1. 打开「启用 MCP 服务器」开关,状态行显示 `运行中 · 127.0.0.1:61207`
2. 按需调整:
   - **监听端口**(默认 61207,改后需重新生成接入配置)
   - **命令审批策略**(见下文)

所有工具默认全部可用,副作用完全由审批策略约束。

### 2. 生成令牌并接入客户端

1. 令牌名称填一个可辨识的名字(如 `claude-code`)→ 点「生成令牌」
2. 弹出「接入 AI 客户端」向导:**令牌只显示这一次**,请立即复制
3. 切换到你要用的客户端 tab,一键复制对应的配置

::: warning 令牌只显示一次
令牌明文不做存储(服务端只有 SHA-256 哈希),关闭向导后无法再查看。丢了只能重新生成——同名生成会轮换旧令牌,旧令牌立即失效,需同步更新客户端配置。
:::

### 3. 客户端配置示例

**Claude Code**(任意项目目录执行):

```bash
claude mcp add --transport http uniterm http://127.0.0.1:61207/mcp \
  --header "Authorization: Bearer <你的令牌>"
```

**Codex**(`~/.codex/config.toml` 追加):

```toml
[mcp_servers.uniterm]
url = "http://127.0.0.1:61207/mcp"
http_headers = { "Authorization" = "Bearer <你的令牌>" }
```

**ZCode**(`~/.zcode/cli/config.json` 的 `mcp.servers` 合并):

```json
{
  "mcp": {
    "servers": {
      "uniterm": {
        "type": "http",
        "url": "http://127.0.0.1:61207/mcp",
        "headers": { "Authorization": "Bearer <你的令牌>" }
      }
    }
  }
}
```

**Trae**(项目根 `.trae/mcp.json` 或用户级 `~/.trae/mcp.json`):

```json
{
  "mcpServers": {
    "uniterm": {
      "url": "http://127.0.0.1:61207/mcp",
      "headers": { "Authorization": "Bearer <你的令牌>" }
    }
  }
}
```

**WorkBuddy**(连接器目录 `~/.workbuddy/connectors-marketplace/connectors/uniterm/mcp.json`,并在 App 内启用):

```json
{
  "mcpServers": {
    "uniterm": {
      "type": "streamableHttp",
      "url": "http://127.0.0.1:61207/mcp",
      "headers": { "Authorization": "Bearer <你的令牌>" }
    }
  }
}
```

**DeepSeek**(dsh 的 Cordis overlay 片段,保存为 `uniterm.cordis.yml`,启动时 `dsh web --patch <该文件>`;令牌放在环境变量 `UNITERM_MCP_TOKEN` 中):

```yaml
- id: mcp-uniterm
  name: '@deepseek-ai/dsh-mcp-client'
  config:
    serverName: uniterm
    transport: streamable-http
    url: http://127.0.0.1:61207/mcp
    headers:
      Authorization: !!js `Bearer ${process.env.UNITERM_MCP_TOKEN}`
    toolCallTimeoutMs: 130000
```

**Kimi**(任意项目目录执行):

```bash
kimi mcp add --transport http uniterm http://127.0.0.1:61207/mcp \
  --header "Authorization: Bearer <你的令牌>"
```

**Gemini**(`~/.gemini/settings.json` 的 `mcpServers` 合并):

```json
{
  "mcpServers": {
    "uniterm": {
      "type": "http",
      "url": "http://127.0.0.1:61207/mcp",
      "headers": { "Authorization": "Bearer <你的令牌>" }
    }
  }
}
```

## 开始使用

接入后,直接用自然语言对 Agent 下达任务,例如:

> 用 uniterm 列一下我有哪些 SSH 连接,然后在 web-01 那台上跑 `nginx -t` 看看配置有没有问题

Agent 会自动调用相应工具完成「发现连接 → 建立会话 → 执行命令 → 读取结果」的全流程。Agent 通过 `connect` 建立的连接会在 uniTerm 中打开终端标签页,**你全程可见,也可以随时接管输入**。

## 工具一览

| 工具 | 组 | 说明 |
|------|----|------|
| `list_connections` | 发现 | 列出已保存的 SSH 连接(id、名称、host、user)。**永不返回凭据** |
| `list_sessions` | 发现 | 列出当前活动会话(id、标题、状态、当前目录) |
| `connect` | 执行 | 按连接 id 建立新 SSH 会话(打开前需审批) |
| `exec_command` | 执行 | 在已连接会话上执行命令:独立非 PTY 通道,stdout/stderr 分离、真实退出码,不影响你正在使用的终端 |
| `get_command_output` | 执行 | 按命令 id 轮询长命令的输出与状态(超过 20 秒自动转异步) |
| `interrupt_command` | 执行 | 向运行中的命令发送 SIGINT / SIGTERM / SIGKILL |
| `upload_file` | 文件 | 上传本地文件到远程主机(流式,内容不经过模型) |
| `download_file` | 文件 | 下载远程文件到本地(流式) |
| `list_remote_dir` | 文件 | 列出远程目录(500 条上限) |
| `read_remote_file` | 文件 | 读取远程文件内容(≤256KB,支持分页) |

**典型工作流**:`list_connections` / `list_sessions` 找到目标 → 没有会话就 `connect` → `exec_command` 执行 → 长命令用 `get_command_output` 轮询 → 需要搬文件用 `upload_file` / `download_file`。

## 命令审批策略

| 策略 | 行为 |
|------|------|
| 每条命令都确认(默认) | 任何 `exec_command` / `connect` / 文件传输都弹窗确认 |
| 仅写入类命令确认 | 命令分级为「写入」及以上才弹窗(如 mv、systemctl restart、重定向) |
| 仅危险命令确认 | 仅危险级命令弹窗(rm -rf、mkfs、`curl \| sh`、写 /etc 等) |
| 全部放行 | 不弹窗,**但危险级命令仍然强制弹窗** |

审批弹窗会显示:请求的客户端名、目标连接、**完整命令全文**。你可以:

- **允许** — 执行
- **拒绝** — 阻断执行
- **拒绝并说明** — 理由会回传给 Agent,它能据此调整方案

::: warning 超时即拒绝
110 秒内未响应(切走了窗口、不在电脑前),审批自动拒绝并告知 Agent「用户未响应」。这是刻意设计:宁可让 Agent 重试,也不让命令在你不知情时执行。
:::

## 文件传输的本地目录限制

`upload_file` / `download_file` 的**本地路径必须位于允许目录内**,允许目录来自 SFTP 面板的「本地路径书签」(设置 → 存储,或文件面板侧栏保存的路径)。

- 路径会经过符号链接解析,**逃逸路径会被拒绝**(如允许 `/tmp/allowed`,但软链指向 `/etc` 的路径无法通过)
- 允许列表为空时,所有传输一律拒绝——这是刻意的:需要传文件就先把目录加入书签
- 远端路径无限制,但同样受审批策略约束

## 安全模型速查

| 层 | 机制 |
|----|------|
| 传输 | 仅 127.0.0.1 监听,本机其他进程仍需 Bearer 令牌 |
| 认证 | 每客户端一枚令牌,SHA-256 存储,可吊销,可热加载 |
| 授权 | 审批策略矩阵(命令按风险分级,策略决定何时弹窗) |
| 执行 | 独立 exec 通道,不触碰你的交互终端;并发上限 8,单命令 5 分钟硬超时 |
| 审计 | JSONL 全量记录(客户端 / 工具 / 命令 / 退出码 / 审批结果) |
| 凭据 | 永不出应用——Agent 看不到任何密码、私钥、密钥内容 |

## 常见问题

**`claude mcp list` 显示连接失败?**
确认设置页开关已打开且状态行为「运行中」;若改过端口,客户端配置里的 URL 也要同步改。

**调用返回 401?**
令牌错误或已被吊销/轮换。重新生成令牌并更新客户端配置。

**命令一直没有执行?**
大概率在等审批弹窗——切回 uniTerm 窗口处理。110 秒未响应会自动拒绝。

**exec_command 报 `session is not connected`?**
Agent 需要先 `connect` 建立会话;或你在 uniTerm 里手动打开该主机的终端标签页,再让 Agent `list_sessions` 找到它。

**上传/下载报「outside the allowed directories」?**
本地路径不在 SFTP 本地书签内。把目标目录加入书签后重试。

**令牌丢了怎么办?**
无法找回(设计如此)。重新生成一枚,客户端配置同步更新。旧令牌立即失效。
