// F-03 regression tests: external MCP client tools (mcp__<server>__<tool>)
// used to be routed straight to the Go manager with NO confirmation, so a
// prompt-injected model could write files / delete directories / push code
// through a configured filesystem/shell MCP server without the user ever
// seeing a dialog. Now they are gated exactly like a terminal command:
//
//   untrusted server (default) -> risk 'dangerous' -> confirm panel
//   explicitly trusted server  -> risk 'read'       -> runs under confirm_write
//
// The rejection path must answer with a normal tool message so the agent loop
// continues instead of aborting.

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const h = vi.hoisted(() => ({
  remoteTools: [] as Array<Record<string, unknown>>,
  chatCalls: 0 as number,
  toolUseQueue: [] as Array<Array<{ id?: string; name: string; input: Record<string, unknown> }>>,
  callRemoteMCPTool: vi.fn(),
}))

// agent.ts transitively pulls in terminalAgent.ts → terminalManager.ts → xterm
// addons that expect a browser `self` global. Stub the wails runtime + the
// terminal manager so the module graph is small enough to load in vitest.
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  ChatCompletion: vi.fn(),
  GetSkillFile: vi.fn(),
  ListSkillFiles: vi.fn(),
  SessionWrite: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('./terminalManager', () => ({
  getManagedTerminal: vi.fn(),
}))

// Approval/continuation reads the active tab's panel.
vi.mock('../stores/tabStore', () => ({
  useTabStore: vi.fn(() => ({
    getAILockedPanel: vi.fn().mockReturnValue(null),
    getAILockedPanels: vi.fn().mockReturnValue([]),
    addAILockedPanel: vi.fn(),
    activeTab: { type: 'terminal', panelId: 'panel-1' },
  })),
}))
vi.mock('../stores/panelStore', () => ({
  usePanelStore: vi.fn(() => ({
    getPanel: vi.fn().mockReturnValue({ id: 'panel-1', sessionId: 'sess-1', title: 'Panel A', config: { shellPath: '/bin/bash' } },
    ),
  })),
}))

// maxTurns > 0 so the mocked chat loop terminates on its own.
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: vi.fn(() => ({ settings: { ai: { maxTurns: 3 } } })),
}))

vi.mock('./terminalAgent', () => ({
  executeCommand: vi.fn().mockResolvedValue({ output: 'ok', exitCode: 0, timedOut: false }),
  startCommand: vi.fn().mockResolvedValue({ output: 'started', started: true }),
  sendTerminalKey: vi.fn().mockResolvedValue({ output: '' }),
  captureTerminal: vi.fn().mockReturnValue({ output: '' }),
  collectOutput: vi.fn().mockResolvedValue({ output: '', completed: true }),
  hasActiveSession: vi.fn().mockReturnValue(true),
}))

// Scripted model: each chat call drains one queued batch of tool uses.
vi.mock('./llm', () => ({
  chat: vi.fn(async (opts: any) => {
    h.chatCalls++
    const batch = h.toolUseQueue.shift()
    if (batch) {
      for (const tu of batch) opts.onToolUse({ id: tu.id ?? `t-${h.chatCalls}`, name: tu.name, input: tu.input })
    }
  }),
  AVAILABLE_TOOLS: [
    { name: 'execute_command', description: 'run', input_schema: { type: 'object', properties: {} } },
  ],
  ChatCancelledError: class ChatCancelledError extends Error {},
  ChatTimeoutError: class ChatTimeoutError extends Error {},
}))

vi.mock('./mcpTools', () => ({
  loadRemoteMCPTools: vi.fn(async () => h.remoteTools),
  toAgentToolEntry: (t: any) => ({
    name: t.qualifiedName,
    description: t.description || '',
    input_schema: { type: 'object', properties: {} },
  }),
  callRemoteMCPTool: (...args: unknown[]) => h.callRemoteMCPTool(...args),
  isRemoteMCPTool: (name: string) => name.startsWith('mcp__'),
}))

import { runAgent, approveTool, rejectTool } from './agent'
import { useAIStore } from '../stores/aiStore'

const TOOL = 'mcp__fs__write_file'
const ARGS = { path: '/tmp/owned.txt', content: 'pwned by prompt injection' }

function remoteTool(trusted: boolean) {
  return {
    server: 'fs',
    name: 'write_file',
    qualifiedName: TOOL,
    description: 'write a file',
    trusted,
  }
}

describe('F-03: external MCP client tools require confirmation', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    h.remoteTools = [remoteTool(false)]
    h.toolUseQueue = []
    h.chatCalls = 0
    h.callRemoteMCPTool.mockResolvedValue('wrote /tmp/owned.txt')
  })

  it('does not call the tool directly — it enters confirming first', async () => {
    h.toolUseQueue = [[{ name: TOOL, input: ARGS }]]

    await runAgent('write a file please')

    expect(h.callRemoteMCPTool).not.toHaveBeenCalled()
    const store = useAIStore()
    expect(store.status).toBe('confirming')
    expect(store.isRunning).toBe(false)
    expect(store.pendingCommand).not.toBeNull()

    const pending = store.pendingCommand!
    expect(pending.toolName).toBe(TOOL)
    expect(pending.risk).toBe('dangerous')
    expect(pending.dangerous).toBe(true)
    // Panel preview shows server + tool + JSON, truncated if huge…
    expect(pending.command).toContain(TOOL)
    expect(pending.command).toContain('/tmp/owned.txt')
    // …while the pending payload keeps the COMPLETE arguments.
    expect(pending.mcpTool?.qualifiedName).toBe(TOOL)
    expect(pending.mcpTool?.server).toBe('fs')
    expect(pending.mcpTool?.args).toEqual(ARGS)
  })

  it('approval forwards the complete arguments to the backend', async () => {
    h.toolUseQueue = [[{ name: TOOL, input: ARGS }]]
    await runAgent('write a file please')
    const store = useAIStore()
    expect(store.pendingCommand).not.toBeNull()

    await approveTool(store.pendingCommand!.messageId)

    expect(h.callRemoteMCPTool).toHaveBeenCalledTimes(1)
    expect(h.callRemoteMCPTool).toHaveBeenCalledWith(TOOL, ARGS)
    expect(store.pendingCommand).toBeNull()
    // The tool result is recorded for the model (follow-up assistant
    // placeholder from the continued run may come after it).
    const result = store.messages.find(m => m.role === 'tool' && m.tool_call_id === 't-1')
    expect(result).toBeTruthy()
    expect(result!.content).toContain('wrote /tmp/owned.txt')
  })

  it('rejection answers with a tool message and the session continues', async () => {
    h.toolUseQueue = [[{ name: TOOL, input: ARGS }]]
    await runAgent('write a file please')
    const store = useAIStore()
    const toolId = store.pendingCommand!.toolId

    rejectTool(store.pendingCommand!.messageId)

    expect(h.callRemoteMCPTool).not.toHaveBeenCalled()
    expect(store.pendingCommand).toBeNull()
    const rejection = store.messages.find(m => m.role === 'tool' && m.tool_call_id === toolId)
    expect(rejection?.content).toContain('User rejected the external MCP tool call')
    expect(rejection?.content).toContain(TOOL)
    // rejectTool schedules runAgent('') — the loop must keep going: the
    // scheduled run calls chat again and terminates cleanly on the drained
    // queue (both conditions observed on the still-active store).
    await vi.waitFor(() => {
      expect(h.chatCalls).toBeGreaterThanOrEqual(2)
      expect(store.isRunning).toBe(false)
    })
  })

  it('a large argument payload is truncated in the panel preview only', async () => {
    const big = { blob: 'x'.repeat(2000) }
    h.toolUseQueue = [[{ name: TOOL, input: big }]]
    await runAgent('write a file please')

    const pending = useAIStore().pendingCommand!
    expect(pending.command.length).toBeLessThan(900)
    expect(pending.command).toContain('(+')
    expect(pending.mcpTool?.args).toEqual(big)
  })

  it('an explicitly trusted server runs without a prompt under confirm_write', async () => {
    h.remoteTools = [remoteTool(true)]
    h.toolUseQueue = [[{ name: TOOL, input: ARGS }]]

    await runAgent('write a file please')

    expect(useAIStore().pendingCommand).toBeNull()
    expect(h.callRemoteMCPTool).toHaveBeenCalledTimes(1)
    expect(h.callRemoteMCPTool).toHaveBeenCalledWith(TOOL, ARGS)
  })

  it('an unknown remote tool (metadata miss) still prompts', async () => {
    h.remoteTools = [] // cache miss / server dropped between list and call
    h.toolUseQueue = [[{ name: 'mcp__gone__tool', input: {} }]]

    await runAgent('do something')

    expect(h.callRemoteMCPTool).not.toHaveBeenCalled()
    expect(useAIStore().pendingCommand).not.toBeNull()
    expect(useAIStore().pendingCommand!.risk).toBe('dangerous')
  })
})
