# MCP Server (External AI Agents)

uniTerm can run as an MCP (Model Context Protocol) server, letting external AI agents — Claude Code, Codex, ZCode, Kimi, Gemini CLI, Trae, WorkBuddy — **run commands, read output, and transfer files on the remote hosts uniTerm manages**.

The core principle: **credentials never leave uniTerm**. Agents never see a password or private key — they send requests to uniTerm, which executes them over its saved connections, with approval and audit at every step.

## How It Works

```
External agent (Claude Code / Codex / ...)
      │  MCP over Streamable HTTP + Bearer token
      ▼
uniTerm (127.0.0.1:61207/mcp)
      │  approval dialog → policy engine → audit log
      ▼
Saved SSH connections (credentials stay in the app)
```

- **Loopback-only listener** (127.0.0.1) — unreachable from the network
- **One Bearer token per client**; the plaintext is shown exactly once at creation, only a SHA-256 hash is stored
- Every call is logged to `<data-dir>/mcp-audit.log` (JSONL) with the client name, command, and exit code

## Getting Started

### 1. Enable the MCP server

Settings (`⌘,`) → **AI** → "MCP Server (external AI agents)":

1. Turn on the "Enable MCP server" switch; the status line shows `Running · 127.0.0.1:61207`
2. Adjust as needed:
   - **Listen port** (default 61207; client configs must be regenerated after changing it)
   - **Command approval policy** (see below)
   - **Tool groups**: command execution, file transfers (only command execution is on by default)

### 2. Generate a token and connect a client

1. Enter a recognizable token name (e.g. `claude-code`) → click "Generate token"
2. The setup wizard opens: **the token is shown only once** — copy it immediately
3. Switch to the tab of the client you use and copy its config snippet

::: warning One-time token display
The plaintext is never stored (the server keeps only a SHA-256 hash); it cannot be viewed again after the wizard closes. If lost, regenerate — generating with the same name rotates the token and invalidates the old one immediately, so client configs must be updated.
:::

### 3. Client configuration examples

**Claude Code** (run in any project directory):

```bash
claude mcp add --transport http uniterm http://127.0.0.1:61207/mcp \
  --header "Authorization: Bearer <your-token>"
```

**Codex** (append to `~/.codex/config.toml`):

```toml
[mcp_servers.uniterm]
url = "http://127.0.0.1:61207/mcp"
http_headers = { "Authorization" = "Bearer <your-token>" }
```

**ZCode** (merge into `mcp.servers` of `~/.zcode/cli/config.json`):

```json
{
  "mcp": {
    "servers": {
      "uniterm": {
        "type": "http",
        "url": "http://127.0.0.1:61207/mcp",
        "headers": { "Authorization": "Bearer <your-token>" }
      }
    }
  }
}
```

**Trae** (project-level `.trae/mcp.json` or user-level `~/.trae/mcp.json`):

```json
{
  "mcpServers": {
    "uniterm": {
      "url": "http://127.0.0.1:61207/mcp",
      "headers": { "Authorization": "Bearer <your-token>" }
    }
  }
}
```

**WorkBuddy** (connector dir `~/.workbuddy/connectors-marketplace/connectors/uniterm/mcp.json`, then enable in the app):

```json
{
  "mcpServers": {
    "uniterm": {
      "type": "streamableHttp",
      "url": "http://127.0.0.1:61207/mcp",
      "headers": { "Authorization": "Bearer <your-token>" }
    }
  }
}
```

**Kimi** (run in any project directory):

```bash
kimi mcp add --transport http uniterm http://127.0.0.1:61207/mcp \
  --header "Authorization: Bearer <your-token>"
```

**Gemini** (merge into `mcpServers` of `~/.gemini/settings.json`):

```json
{
  "mcpServers": {
    "uniterm": {
      "type": "http",
      "url": "http://127.0.0.1:61207/mcp",
      "headers": { "Authorization": "Bearer <your-token>" }
    }
  }
}
```

## Using It

Once connected, just describe the task in natural language:

> Use uniterm to list my SSH connections, then run `nginx -t` on web-01 to check the config

The agent drives the whole flow itself: discover connections → open a session → execute → read results. Sessions the agent opens via `connect` appear as terminal tabs in uniTerm — **fully visible to you, and you can take over input at any time**.

## Tool Reference

| Tool | Group | Description |
|------|-------|-------------|
| `list_connections` | Discovery | List saved SSH connections (id, name, host, user). **Never returns credentials** |
| `list_sessions` | Discovery | List live sessions (id, title, status, cwd) |
| `connect` | Exec | Open a new SSH session for a saved connection (approval required) |
| `exec_command` | Exec | Run a command on a connected session: dedicated non-PTY channel, separate stdout/stderr, real exit code, never touches your interactive terminal |
| `get_command_output` | Exec | Poll output/status of long commands by command id (auto-async after 20s) |
| `interrupt_command` | Exec | Send SIGINT / SIGTERM / SIGKILL to a running command |
| `upload_file` | Files | Upload a local file to the remote host (streamed; content never passes through the model) |
| `download_file` | Files | Download a remote file (streamed) |
| `list_remote_dir` | Files | List a remote directory (capped at 500 entries) |
| `read_remote_file` | Files | Read remote file content (≤256KB, paged) |

**Typical flow**: find the target with `list_connections` / `list_sessions` → `connect` if no session exists → `exec_command` → poll long commands with `get_command_output` → move files with `upload_file` / `download_file`.

## Approval Policy

| Policy | Behavior |
|--------|----------|
| Confirm every command (default) | Every `exec_command` / `connect` / transfer prompts for approval |
| Confirm write commands | Prompts for write-level commands and above (mv, systemctl restart, redirections, …) |
| Confirm dangerous commands | Prompts only for dangerous commands (rm -rf, mkfs, `curl \| sh`, writing /etc, …) |
| Bypass all | No dialogs — **but dangerous commands still prompt** |

The approval dialog shows: the requesting client, the target connection, and **the full command text**. You can:

- **Allow** — execute
- **Deny** — block the call
- **Deny with reason** — the reason is sent back to the agent, which can adjust its plan

::: warning Timeout = denial
If you don't respond within 110 seconds (switched away, away from the machine), the approval is auto-denied and the agent is told "user did not respond". This is deliberate: better to have the agent retry than to run a command without your knowledge.
:::

## Local Directory Restriction for Transfers

The **local side** of `upload_file` / `download_file` must be inside an allowed directory, taken from the SFTP panel's saved local path bookmarks (Settings → Storage, or paths saved in the file panel sidebar).

- Paths are resolved through symlinks — **escape attempts are rejected** (e.g. a symlink under `/tmp/allowed` pointing to `/etc` fails)
- An empty bookmark list rejects every transfer — on purpose: add the directory to bookmarks first
- Remote paths are unrestricted but governed by the approval policy

## Security Model at a Glance

| Layer | Mechanism |
|-------|-----------|
| Transport | Loopback-only listener; local processes still need a Bearer token |
| Auth | Per-client token, SHA-256 at rest, revocable, hot-reloaded |
| Authorization | Tool group toggles + approval policy matrix |
| Execution | Dedicated exec channel, never touches your interactive terminal; concurrency cap 8, 5-minute hard timeout per command |
| Audit | Full JSONL log (client / tool / command / exit code / approval outcome) |
| Credentials | Never leave the app — agents never see passwords, keys, or key contents |

## Troubleshooting

**`claude mcp list` shows connection failed?**
Verify the switch is on and the status line says "Running"; if you changed the port, update the URL in client configs too.

**Calls return 401?**
Wrong, revoked, or rotated token. Regenerate and update the client config.

**A command never executes?**
Most likely waiting for the approval dialog — switch back to uniTerm. It auto-denies after 110 seconds.

**exec_command says `session is not connected`?**
The agent needs to `connect` first; or open a terminal tab to that host yourself and let the agent find it via `list_sessions`.

**Transfers fail with "outside the allowed directories"?**
The local path is not in the SFTP local bookmarks. Add the directory and retry.

**Lost the token?**
It cannot be recovered (by design). Regenerate one and update client configs; the old token dies immediately.
