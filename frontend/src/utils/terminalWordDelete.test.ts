import { describe, expect, it, vi } from 'vitest'
import { handleTerminalWordDelete, type WordDeleteContext } from './terminalWordDelete'
import { applyBackspaceKey } from './terminalOptions'
import { useTerminalInput } from '../composables/useTerminalInput'

const context: WordDeleteContext = { connectionType: 'ssh', connected: true, alternateScreen: false, passwordPrompt: false }
function event(overrides: Record<string, unknown> = {}) {
  return { type: 'keydown', key: 'Backspace', ctrlKey: true, shiftKey: false, altKey: false, metaKey: false, isComposing: false, preventDefault: vi.fn(), ...overrides }
}

describe('Ctrl+Backspace terminal routing', () => {
  it.each(['ssh', 'mosh', 'wsl'])('sends one Ctrl+W through input for %s', connectionType => {
    const key = event()
    const input = vi.fn()
    expect(handleTerminalWordDelete(key, { ...context, connectionType }, input)).toBe(true)
    expect(input).toHaveBeenCalledExactlyOnceWith('\x17')
    expect(key.preventDefault).toHaveBeenCalledOnce()
    for (const mode of ['del', 'bs', 'vt220'] as const) expect(applyBackspaceKey('\x17', mode, 'ssh')).toBe('\x17')
  })

  it.each(['linux', 'darwin', 'android'])('supports local shells on a %s app host', hostPlatform => {
    expect(handleTerminalWordDelete(event(), { ...context, connectionType: 'local', hostPlatform }, vi.fn())).toBe(true)
  })

  it.each([
    { ctrlKey: false }, { shiftKey: true }, { altKey: true }, { metaKey: true },
    { type: 'keyup' }, { key: 'Delete' }, { isComposing: true },
  ])('leaves other keys and composition untouched: %o', overrides => {
    const key = event(overrides)
    const input = vi.fn()
    expect(handleTerminalWordDelete(key, context, input)).toBe(false)
    expect(input).not.toHaveBeenCalled()
    expect(key.preventDefault).not.toHaveBeenCalled()
  })

  it.each([
    { connected: false }, { alternateScreen: true }, { passwordPrompt: true },
    { remoteOS: 'windows-openssh' },
    { connectionType: 'local', hostPlatform: 'windows' },
    { connectionType: 'local', hostPlatform: undefined },
    { connectionType: 'telnet' }, { connectionType: 'serial' },
    { connectionType: 'container-exec' }, { connectionType: 'sftp' }, { connectionType: undefined },
  ])('preserves native behavior outside a connected Unix shell path: %o', state => {
    const key = event()
    const input = vi.fn()
    expect(handleTerminalWordDelete(key, { ...context, ...state }, input)).toBe(false)
    expect(input).not.toHaveBeenCalled()
    expect(key.preventDefault).not.toHaveBeenCalled()
  })

  it('uses real xterm onData to update the suggestion buffer and deliver only Ctrl+W', async () => {
    ;(globalThis as any).self = globalThis
    ;(globalThis as any).requestAnimationFrame ??= () => 0
    ;(globalThis as any).cancelAnimationFrame ??= () => {}
    const { Terminal } = await import('@xterm/xterm')
    const terminal = new Terminal({ allowProposedApi: true })
    const model = useTerminalInput(terminal, { mode: 'ssh', sessionId: 'test' })
    const sent: string[] = []
    const subscription = terminal.onData(data => {
      model.handleInput(data)
      sent.push(applyBackspaceKey(data, 'vt220', 'ssh'))
    })
    try {
      terminal.input('curl --header value')
      expect(model.currentToken.value).toBe('curl --header value')
      const before = sent.length
      expect(handleTerminalWordDelete(event(), context, data => terminal.input(data))).toBe(true)
      expect(sent.slice(before)).toEqual(['\x17'])
      expect(model.lineBuffer.value).toBe('curl --header ')
      expect(model.currentToken.value).toBe('curl --header')
    } finally {
      subscription.dispose()
      terminal.dispose()
    }
  })
})
