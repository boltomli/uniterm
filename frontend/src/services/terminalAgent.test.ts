import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// Use vi.hoisted to allow factory references to variables defined at top level
const { mockSessionWrite, mockStartForeground, mockPollForeground, mockInterruptForeground, mockCaptureScreen, mockSendKey, mockStartForegroundCapture } = vi.hoisted(() => {
  const mockSessionWrite = vi.fn().mockResolvedValue(undefined)
  // Backend execution-engine bindings (executeCommand/startCommand now run
  // through the MCP foreground job registry instead of the raw session:data stream).
  const mockStartForeground = vi.fn().mockResolvedValue('fg-job-1')
  const mockPollForeground = vi.fn().mockResolvedValue(['', '', 0, 'exited'])
  const mockInterruptForeground = vi.fn().mockResolvedValue(undefined)
  const mockCaptureScreen = vi.fn().mockResolvedValue('screen output')
  const mockSendKey = vi.fn().mockResolvedValue(undefined)
  const mockStartForegroundCapture = vi.fn().mockResolvedValue('fg-observer')
  return { mockSessionWrite, mockStartForeground, mockPollForeground, mockInterruptForeground, mockCaptureScreen, mockSendKey, mockStartForegroundCapture }
})

// ---- mock wailsjs modules ----
import { Events } from '@wailsio/runtime'
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  SessionWrite: mockSessionWrite,
  CaptureScreen: mockCaptureScreen,
  SendKey: mockSendKey,
  StartForegroundCapture: mockStartForegroundCapture,
  StartForeground: (...args: any[]) => mockStartForeground(...args),
  PollForeground: async (...args: any[]) => {
    const [out, err, code, state] = await mockPollForeground(...args)
    const encode = (s: string) => btoa(Array.from(new TextEncoder().encode(s), b => String.fromCharCode(b)).join(''))
    return [encode(out), encode(err), code, state]
  },
  InterruptForeground: (...args: any[]) => mockInterruptForeground(...args),
}))

vi.mock('../services/sessionWriter', () => ({
  queuedSessionWrite: (...args: any[]) => mockSessionWrite(...args),
}))

// ---- mock pinia stores ----
const mockPanel = {
  sessionId: 'test-session-id',
  config: { shellPath: '/bin/bash' },
}
const mockGetPanel = vi.fn().mockReturnValue(mockPanel)
const mockGetAILockedPanel = vi.fn().mockReturnValue(null)
const mockGetAILockedPanels = vi.fn().mockReturnValue([])

const mockActiveTab: { type: string; panelId: string } = {
  type: 'terminal',
  panelId: 'panel-1',
}
const mockTabStore = {
  getAILockedPanel: mockGetAILockedPanel,
  getAILockedPanels: mockGetAILockedPanels,
  activeTab: mockActiveTab,
}
const mockPanels = new Map<string, { id: string; title: string }>([
  ['panel-1', { id: 'panel-1', title: 'Panel A' }],
  ['panel-2', { id: 'panel-2', title: 'Prod DB' }],
])
const mockPanelStore = {
  getPanel: mockGetPanel,
  panels: mockPanels,
}

vi.mock('../stores/tabStore', () => ({
  useTabStore: vi.fn(() => mockTabStore),
}))
vi.mock('../stores/panelStore', () => ({
  usePanelStore: vi.fn(() => mockPanelStore),
}))
const mockGetRemoteOS = vi.fn().mockReturnValue(undefined)
const mockSessionStore = {
  getRemoteOS: mockGetRemoteOS,
  getChunkCount: vi.fn(() => 0),
  getDataFromChunk: vi.fn(() => ''),
}
vi.mock('../stores/sessionStore', () => ({
  useSessionStore: vi.fn(() => mockSessionStore),
}))

// ---- import after mocks ----
import { collectOutput, executeCommand, truncateOutput, startCommand, sendTerminalKey, captureTerminal } from './terminalAgent'
import type { ExecuteResult } from './terminalAgent'
describe('truncateOutput', () => {
  it('returns full text when lines <= threshold', () => {
    const text = 'line1\nline2\nline3'
    const result = truncateOutput(text, 2, 2)
    expect(result).toBe(text)
  })

  it('truncates middle when lines > threshold', () => {
    const lines = Array.from({ length: 20 }, (_, i) => `line${i + 1}`)
    const text = lines.join('\n')
    const result = truncateOutput(text, 2, 3)

    expect(result).toContain('line1')
    expect(result).toContain('line2')
    expect(result).not.toContain('line3')
    expect(result).not.toContain('line17')
    expect(result).toContain('line18')
    expect(result).toContain('line19')
    expect(result).toContain('line20')
    expect(result).toContain('TRUNCATED')
    expect(result).toContain('omitted')
  })

  it('handles edge case: headLines=0', () => {
    const lines = Array.from({ length: 10 }, (_, i) => `line${i + 1}`)
    const text = lines.join('\n')
    const result = truncateOutput(text, 0, 2)

    expect(result).toContain('omitted')
    expect(result).toContain('line9')
    expect(result).toContain('line10')
  })

  it('handles edge case: tailLines=0', () => {
    const lines = Array.from({ length: 10 }, (_, i) => `line${i + 1}`)
    const text = lines.join('\n')
    const result = truncateOutput(text, 3, 0)

    expect(result).toContain('line1')
    expect(result).toContain('line3')
    expect(result).toContain('omitted')
  })

  it('handles single line input', () => {
    const result = truncateOutput('single', 1, 1)
    expect(result).toBe('single')
  })

  it('handles empty string', () => {
    const result = truncateOutput('', 1, 1)
    expect(result).toBe('')
  })
})

describe('ExecuteResult interface', () => {
  it('has optional timedOut field', () => {
    const result: ExecuteResult = {
      output: 'test',
      exitCode: 0,
      timedOut: false,
    }
    expect(result.timedOut).toBe(false)

    const result2: ExecuteResult = {
      output: 'test',
      exitCode: -1,
      timedOut: true,
    }
    expect(result2.timedOut).toBe(true)

    const result3: ExecuteResult = {
      output: 'test',
      exitCode: 0,
    }
    expect(result3.timedOut).toBeUndefined()
  })
})

describe('executeCommand', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(Events.On).mockReturnValue(() => {})
    vi.mocked(mockSessionWrite).mockClear()
    vi.mocked(mockGetPanel).mockReturnValue(mockPanel)
    vi.mocked(mockGetAILockedPanel).mockReturnValue(null)
    vi.mocked(mockGetAILockedPanels).mockReturnValue([])
    vi.mocked(mockGetRemoteOS).mockReturnValue(undefined)
    mockActiveTab.type = 'terminal'
    mockActiveTab.panelId = 'panel-1'
    mockStartForeground.mockResolvedValue('fg-job-1')
    mockPollForeground.mockResolvedValue(['', '', 0, 'exited'])
  })

  // A failing assertion inside a fake-timer test must not leak fake timers
  // into the next test (settle timers would never fire and it would hang).
  afterEach(() => {
    vi.useRealTimers()
  })

  it('throws when no active session', async () => {
    vi.mocked(mockGetPanel).mockReturnValue(null)
    mockActiveTab.type = 'settings' // not terminal, not workspace

    await expect(executeCommand('ls')).rejects.toThrow('No active terminal session')
  })

  it('hands the command to the backend engine with no injected marker', async () => {
    mockStartForeground.mockResolvedValueOnce('fg-job-1')
    mockPollForeground.mockResolvedValueOnce(['hello\n', '', 0, 'exited'])

    const result = await executeCommand('echo hello')

    expect(mockStartForeground).toHaveBeenCalledOnce()
    const [sid, cmd, hardLimitMs] = mockStartForeground.mock.calls[0]
    expect(sid).toBe('test-session-id')
    expect(cmd).toContain('echo hello')
    expect(cmd).not.toContain('__AI_DONE_')
    expect(cmd).not.toContain('echo "')
    expect(mockStartForeground.mock.calls[0]).toHaveLength(3)
    expect(hardLimitMs).toBe(60000)
    expect(result.exitCode).toBe(0)
    expect(result.timedOut).toBe(false)
    expect(result.output).toContain('hello')
  })

  it('sends no newline and no leading space for Windows OpenSSH (the engine appends CR)', async () => {
    vi.mocked(mockGetRemoteOS).mockReturnValue('windows-openssh')
    mockPollForeground.mockResolvedValueOnce(['dir listing\n', '', 0, 'exited'])

    await executeCommand('dir')

    expect(mockStartForeground).toHaveBeenCalledOnce()
    const cmd = mockStartForeground.mock.calls[0][1] as string
    expect(cmd).toBe('dir') // newline now lives in the backend newlineFor()
  })

  it('returns timedOut=true when the engine reports the timeout state', async () => {
    mockStartForeground.mockResolvedValueOnce('fg-job-2')
    let polls = 0
    mockPollForeground.mockImplementation(async () => {
      polls++
      if (polls === 1) return ['some output line1\nline2\nline3\nline4\nline5', '', 0, 'running']
      return ['', '', 0, 'timeout']
    })

    const result: ExecuteResult = await executeCommand('long-command', 1000, 2, 2)

    expect(result.exitCode).toBe(-1)
    expect(result.timedOut).toBe(true)
    expect(result.output).toContain('TRUNCATED')
    expect(result.output).toContain('line1')
    expect(result.output).toContain('line2')
    expect(result.output).toContain('line4')
    expect(result.output).toContain('line5')
    expect(result.output).not.toContain('line3') // truncated middle
  })

  it('returns cancelled and interrupts the job on stop', async () => {
    mockStartForeground.mockResolvedValueOnce('fg-job-3')
    let polls = 0
    mockPollForeground.mockImplementation(async () => {
      polls++
      return ['', '', 0, 'running']
    })
    // Cancel after the first poll so the loop's stop check fires on the next
    // iteration, exactly like the store's stopRequested flag does.
    const result = await executeCommand('long-command', 5000, 50, 300, () => polls >= 1)
    expect(result.cancelled).toBe(true)
    expect(mockInterruptForeground).toHaveBeenCalledWith('fg-job-3', 'SIGINT')
  }, 1000)

  it('advances byte offsets for Unicode output', async () => {
    mockPollForeground.mockResolvedValueOnce(['\u4e2d\u6587\u{1f642}', '', 0, 'running'])
      .mockResolvedValueOnce(['\u5b8c\u6210', '', 0, 'exited'])
    const result = await executeCommand('echo unicode')
    expect(result.output).toBe('\u4e2d\u6587\u{1f642}\u5b8c\u6210')
    expect(mockPollForeground.mock.calls[1][1]).toBe(10)
  })

  it('drains all pages after completion, including split UTF-8', async () => {
    const output = 'a'.repeat(65535) + '\u4e2d' + 'tail'
    // Supply the actual base64 wire pages, splitting the Chinese character.
    const bytes = new TextEncoder().encode(output)
    const spy = vi.spyOn(await import('../../bindings/github.com/ys-ll/uniterm/app'), 'PollForeground')
    const encode = (b: Uint8Array) => btoa(Array.from(b, c => String.fromCharCode(c)).join(''))
    spy.mockResolvedValueOnce([encode(bytes.slice(0, 65536)), '', 0, 'exited'] as any)
      .mockResolvedValueOnce([encode(bytes.slice(65536)), '', 0, 'exited'] as any)
    try {
      expect((await executeCommand('large')).output).toBe(output)
      expect(spy).toHaveBeenLastCalledWith('fg-job-1', 65536, 65536)
    } finally { spy.mockRestore() }
  })

  it('does not report a closed session as success', async () => {
    mockPollForeground.mockResolvedValueOnce(['partial', '', -1, 'session_closed'])
    const result = await executeCommand('cmd')
    expect(result.exitCode).toBe(-1)
    expect(result.completed).toBe(false)
  })

  it('resumes unread output from the original job after timeout', async () => {
    mockStartForeground.mockResolvedValueOnce('fg-resume')
    mockStartForegroundCapture.mockResolvedValueOnce('fg-resume')
    mockPollForeground.mockResolvedValueOnce(['中文 first\n', '', -1, 'timeout'])
      .mockResolvedValueOnce(['late replies\nstatistics\n', '', 0, 'exited'])
    const first = await executeCommand('ping', 1000)
    expect(first.timedOut).toBe(true)
    expect(first.completed).toBe(false)
    const next = await collectOutput(1000)
    expect(next).toEqual({ output: 'late replies\nstatistics\n', completed: true, timedOut: false })
    expect(mockPollForeground).toHaveBeenLastCalledWith('fg-resume', new TextEncoder().encode('中文 first\n').length, 65536)
    expect(mockStartForeground).toHaveBeenCalledTimes(1)
    expect(mockInterruptForeground).not.toHaveBeenCalled()
  })

  it('recognizes a completed command with a nonzero exit code', async () => {
    mockPollForeground.mockResolvedValueOnce(['failed', '', 7, 'exited'])
    const result = await executeCommand('false')
    expect(result.completed).toBe(true)
    expect(result.exitCode).toBe(7)
  })

  it('truncates long output on success path', async () => {
    const lines = Array.from({ length: 10 }, (_, i) => `line${i + 1}`)
    const output = lines.join('\n')

    mockStartForeground.mockResolvedValueOnce('fg-job-4')
    mockPollForeground.mockResolvedValueOnce([output, '', 0, 'exited'])

    // headLines=3 keeps line1 + line2 in the head; tailLines=3 keeps
    // line8 + line9 + line10 (no echoed-command line nor returning prompt
    // occupies slots anymore — the engine output starts after the echo).
    const result: ExecuteResult = await executeCommand('some-cmd', 5000, 3, 3)

    expect(result.exitCode).toBe(0)
    expect(result.timedOut).toBe(false)
    expect(result.output).toContain('TRUNCATED')
    expect(result.output).toContain('line1')
    expect(result.output).toContain('line2')
    expect(result.output).toContain('line8')
    expect(result.output).toContain('line9')
    expect(result.output).toContain('line10')
  })
})

describe('startCommand', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(Events.On).mockReturnValue(() => {})
    mockStartForeground.mockResolvedValue('fg-job-1')
    mockPollForeground.mockResolvedValue(['', '', 0, 'running'])
  })

  it('returns the engine output sampled during the window', async () => {
    vi.useFakeTimers()
    mockStartForeground.mockResolvedValueOnce('fg-job-5')
    mockPollForeground.mockResolvedValue(['log line A\nlog line B', '', 0, 'running'])

    const p = startCommand('tail -f app.log')
    await vi.advanceTimersByTimeAsync(3000)

    const result = await p
    expect(result.started).toBe(true)
    expect(result.output).toContain('log line A')
    expect(result.output).toContain('log line B')
    vi.useRealTimers()
  })

  it('returns the engine screen-resolved output — redraw frames never reach the caller', async () => {
    vi.useFakeTimers()
    mockStartForeground.mockResolvedValueOnce('fg-job-6')
    // The backend emulator already resolved the ConPTY frames; the frontend
    // sees only the final text.
    mockPollForeground.mockResolvedValue(['final state only', '', 0, 'running'])

    const p = startCommand('cmd-with-redraws')
    await vi.advanceTimersByTimeAsync(3000)

    const result = await p
    expect(result.output).toBe('final state only')
    expect(result.output).not.toContain('frame1')
    vi.useRealTimers()
  })
})

describe('shared backend terminal tools', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockActiveTab.type = 'terminal'
    mockActiveTab.panelId = 'panel-1'
    mockGetPanel.mockReturnValue(mockPanel)
    mockGetAILockedPanels.mockReturnValue([])
    mockCaptureScreen.mockResolvedValue('backend screen')
    mockStartForegroundCapture.mockResolvedValue('fg-observer')
    mockPollForeground.mockResolvedValue(['collected', '', 0, 'exited'])
  })

  afterEach(() => { vi.useRealTimers() })

  it('captures through the same screen backend as MCP', async () => {
    expect(await captureTerminal(80)).toEqual({ output: 'backend screen' })
    expect(mockCaptureScreen).toHaveBeenCalledWith('test-session-id', 80)
  })

  it('propagates capture errors', async () => {
    mockCaptureScreen.mockRejectedValueOnce(new Error('session closed'))
    await expect(captureTerminal()).rejects.toThrow('session closed')
  })

  it('sends text through the same input backend as MCP', async () => {
    await sendTerminalKey('yes', undefined, false)
    expect(mockSendKey).toHaveBeenCalledWith('test-session-id', 'yes', '', false)
    expect(mockSessionWrite).not.toHaveBeenCalled()
  })

  it('uses the shared screen for the response to ctrl_c', async () => {
    vi.useFakeTimers()
    const pending = sendTerminalKey(undefined, 'ctrl_c')
    await vi.advanceTimersByTimeAsync(1000)
    expect(await pending).toEqual({ output: 'backend screen' })
    expect(mockSendKey).toHaveBeenCalledWith('test-session-id', '', 'ctrl_c', true)
    expect(mockCaptureScreen).toHaveBeenCalledWith('test-session-id', 200)
  })

  it('collects using a read-only backend job without sending a command', async () => {
    expect(await collectOutput(5000)).toEqual({ output: 'collected', completed: true, timedOut: false })
    expect(mockStartForegroundCapture).toHaveBeenCalledWith('test-session-id', 5000)
    expect(mockPollForeground).toHaveBeenCalledWith('fg-observer', 0, 65536)
    expect(mockStartForeground).not.toHaveBeenCalled()
    expect(mockSessionWrite).not.toHaveBeenCalled()
  })

  it('keeps timeout distinct from completion during collection', async () => {
    mockPollForeground.mockResolvedValueOnce(['partial', '', -1, 'timeout'])
    expect(await collectOutput()).toEqual({ output: 'partial', completed: false, timedOut: true })
  })

  it('cancels the observer without typing Ctrl+C into the terminal', async () => {
    const result = await collectOutput(5000, 100, 300, () => true)
    expect(result.completed).toBe(false)
    expect(mockInterruptForeground).toHaveBeenCalledWith('fg-observer', 'SIGINT')
    expect(mockSendKey).not.toHaveBeenCalled()
  })
})

describe('resolveActiveSession explicit-panel restriction', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(Events.On).mockReturnValue(() => {})
    vi.mocked(mockSessionWrite).mockClear()
    vi.mocked(mockGetAILockedPanel).mockReturnValue(null)
    vi.mocked(mockGetAILockedPanels).mockReturnValue([])
    vi.mocked(mockGetRemoteOS).mockReturnValue(undefined)
    mockActiveTab.type = 'terminal'
    mockActiveTab.panelId = 'panel-1'
    vi.mocked(mockGetPanel).mockImplementation((id: string) => ({
      id,
      sessionId: `sess-${id}`,
      title: id === 'panel-2' ? 'Prod DB' : 'Panel A',
      config: { shellPath: '/bin/bash' },
    }))
    mockStartForeground.mockResolvedValue('fg-job-1')
    mockPollForeground.mockResolvedValue(['', '', 0, 'exited'])
  })

  it('rejects a panel that is neither locked nor active (no locks)', async () => {
    await expect(executeCommand('rm -rf /', 1000, 1, 1, undefined, 'Prod DB'))
      .rejects.toThrow('not associated with this AI conversation')
    expect(mockStartForeground).not.toHaveBeenCalled()
  })

  it('allows the active panel when nothing is locked', async () => {
    await expect(executeCommand('ls', 10, 1, 1, undefined, 'Panel A')).resolves.toBeDefined()
    expect(mockStartForeground).toHaveBeenCalledWith('sess-panel-1', expect.stringContaining('ls'), 10)
  })

  it('allows an explicitly locked panel', async () => {
    vi.mocked(mockGetAILockedPanels).mockReturnValue(['panel-2'])
    await expect(executeCommand('ls', 10, 1, 1, undefined, 'Prod DB')).resolves.toBeDefined()
    expect(mockStartForeground).toHaveBeenCalledWith('sess-panel-2', expect.stringContaining('ls'), 10)
  })

  it('rejects another panel when a different one is locked', async () => {
    vi.mocked(mockGetAILockedPanels).mockReturnValue(['panel-1'])
    await expect(executeCommand('ls', 1000, 1, 1, undefined, 'Prod DB'))
      .rejects.toThrow('not associated with this AI conversation')
  })

  it('rejects the "title (id: …)" suffix form for a non-associated panel', async () => {
    await expect(executeCommand('ls', 1000, 1, 1, undefined, 'Prod DB (id: panel-2)'))
      .rejects.toThrow('not associated with this AI conversation')
  })
})
