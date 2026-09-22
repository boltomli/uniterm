// Regression tests for issue #974: when a command requires confirmation in
// multi-panel mode, the panel (plus timeout / head_lines / tail_lines) must
// survive the confirm-and-replay path. Previously setPendingCommand dropped
// those fields, so approveTool replayed on the active tab / first locked
// panel instead of the panel the model targeted.

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

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

// approveTool gates on hasActiveSession(), which reads the active tab's panel.
vi.mock('../stores/tabStore', () => ({
  useTabStore: vi.fn(() => ({
    getAILockedPanel: vi.fn().mockReturnValue(null),
    getAILockedPanels: vi.fn().mockReturnValue([]),
    activeTab: { type: 'terminal', panelId: 'panel-1' },
  })),
}))
vi.mock('../stores/panelStore', () => ({
  usePanelStore: vi.fn(() => ({
    getPanel: vi.fn().mockReturnValue({ id: 'panel-1', sessionId: 'sess-1', title: 'Panel A', config: { shellPath: '/bin/bash' } }),
  })),
}))

// runAgent('') at the end of approveTool reads maxTurns from settingsStore.
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: vi.fn(() => ({ settings: { ai: { maxTurns: 0 } } })),
}))

// Mock the terminal agent layer: capture the panelTitle argument so the
// replay path can be asserted without a real terminal.
const mockExecuteCommand = vi.fn().mockResolvedValue({ output: 'ok', exitCode: 0, timedOut: false })
const mockStartCommand = vi.fn().mockResolvedValue({ output: 'started', started: true })
vi.mock('./terminalAgent', () => ({
  executeCommand: (...args: any[]) => mockExecuteCommand(...(args as [])),
  startCommand: (...args: any[]) => mockStartCommand(...(args as [])),
  sendTerminalKey: vi.fn().mockResolvedValue({ output: '' }),
  captureTerminal: vi.fn().mockReturnValue({ output: '' }),
  collectOutput: vi.fn().mockResolvedValue({ output: '', completed: true }),
  hasActiveSession: vi.fn().mockReturnValue(true),
}))

import { approveTool } from './agent'
import { useAIStore } from '../stores/aiStore'

describe('approveTool replays the confirmed command on its original panel (issue #974)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    mockExecuteCommand.mockResolvedValue({ output: 'ok', exitCode: 0, timedOut: false })
    mockStartCommand.mockResolvedValue({ output: 'started', started: true })
  })

  it('passes panel + timeout + head/tail lines to executeCommand', async () => {
    const store = useAIStore()
    store.setPendingCommand({
      messageId: 'msg-1',
      toolId: 'tool-1',
      toolName: 'execute_command',
      command: 'rm -rf ./build',
      risk: 'dangerous',
      dangerous: true,
      panel: 'Panel B',
      timeout: 300000,
      headLines: 10,
      tailLines: 20,
    })

    await approveTool('msg-1')

    expect(mockExecuteCommand).toHaveBeenCalledTimes(1)
    const args = mockExecuteCommand.mock.calls[0]
    expect(args[0]).toBe('rm -rf ./build')
    expect(args[1]).toBe(300000)
    expect(args[2]).toBe(10)
    expect(args[3]).toBe(20)
    expect(args[5]).toBe('Panel B')
    // Pending command cleared and a tool result recorded for the model.
    expect(store.pendingCommand).toBeNull()
  })

  it('passes panel to startCommand', async () => {
    const store = useAIStore()
    store.setPendingCommand({
      messageId: 'msg-2',
      toolId: 'tool-2',
      toolName: 'start_command',
      command: 'npm run dev',
      risk: 'write',
      dangerous: false,
      panel: 'Panel A',
    })

    await approveTool('msg-2')

    expect(mockStartCommand).toHaveBeenCalledTimes(1)
    const args = mockStartCommand.mock.calls[0]
    expect(args[0]).toBe('npm run dev')
    expect(args[1]).toBe('Panel A')
  })

  it('replays without a panel when the model never specified one', async () => {
    const store = useAIStore()
    store.setPendingCommand({
      messageId: 'msg-3',
      toolId: 'tool-3',
      toolName: 'execute_command',
      command: 'ls',
      risk: 'read',
      dangerous: false,
    })

    await approveTool('msg-3')

    expect(mockExecuteCommand).toHaveBeenCalledTimes(1)
    const args = mockExecuteCommand.mock.calls[0]
    expect(args[5]).toBeUndefined()
    // Defaults from the executeCommand signature apply (60s / 50 / 300).
    expect(args[1]).toBeUndefined()
  })
})
