import { ListMCPTools, CallMCPTool } from '../../bindings/github.com/ys-ll/uniterm/app'

/**
 * External MCP client tools.
 *
 * uniTerm connects to user-configured external MCP servers (stdio /
 * streamable-http / sse) and surfaces their tools to the built-in agent. Each
 * remote tool is namespaced as `mcp__<server>__<tool>` so it can never collide
 * with a built-in tool name, and routed back to the Go client manager on call.
 *
 * This is the CLIENT half. The MCP Server section in settings (uniTerm exposed
 * to external agents) is unrelated and unchanged.
 */

export interface RemoteMCPTool {
  server: string
  name: string
  qualifiedName: string
  description: string
  inputSchema?: unknown
}

export interface AgentToolEntry {
  name: string
  description: string
  input_schema: {
    type: string
    properties: Record<string, unknown>
    required?: string[]
  }
}

// Cache the flattened tool list for the duration of one agent run; the backend
// manager owns reconnection. Refresh on each runAgent call so a newly added
// server is picked up without a restart.
let cached: { at: number; tools: RemoteMCPTool[] } | null = null
const CACHE_TTL_MS = 15_000

export async function loadRemoteMCPTools(): Promise<RemoteMCPTool[]> {
  if (cached && Date.now() - cached.at < CACHE_TTL_MS) return cached.tools
  let list: RemoteMCPTool[] = []
  try {
    list = (await ListMCPTools()) ?? []
  } catch {
    // A broken external server must never break the chat run: degrade to no
    // remote tools and let the model use the built-in set.
    list = []
  }
  cached = { at: Date.now(), tools: list }
  return list
}

// Convert a remote tool into the agent's `{name, description, input_schema}`
// entry shape. The remote input schema already follows JSON Schema; pass it
// through, normalizing to the object form the tool loop expects.
export function toAgentToolEntry(t: RemoteMCPTool): AgentToolEntry {
  const schema = (t.inputSchema && typeof t.inputSchema === 'object'
    ? t.inputSchema
    : {}) as {
    type?: string
    properties?: Record<string, unknown>
    required?: string[]
  }
  return {
    name: t.qualifiedName,
    description:
      (t.description || `External MCP tool ${t.name} (server: ${t.server})`) +
      `\n\n[External MCP tool from server "${t.server}". Call it directly; arguments follow its input schema.]`,
    input_schema: {
      type: 'object',
      properties: schema.properties ?? {},
      required: schema.required ?? [],
    },
  }
}

export async function callRemoteMCPTool(qualifiedName: string, args: Record<string, unknown>): Promise<string> {
  return CallMCPTool(qualifiedName, args ?? {})
}

// Namespace prefix used by both sides (Go: mcpclient.QualifiedName).
export const REMOTE_TOOL_PREFIX = 'mcp__'

export function isRemoteMCPTool(name: string): boolean {
  return name.startsWith(REMOTE_TOOL_PREFIX)
}
