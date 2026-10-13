import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// Spy shared between the sessionStore mock factory and the assertions.
const { removeSession } = vi.hoisted(() => ({ removeSession: vi.fn() }))

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: vi.fn(() => () => {}),
    Off: vi.fn(),
  },
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
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))
vi.mock('@xterm/addon-unicode11', () => ({
  Unicode11Addon: class {},
}))
vi.mock('@xterm/addon-search', () => ({
  SearchAddon: class {},
}))
vi.mock('@xterm/addon-clipboard', () => ({
  ClipboardAddon: class {},
  Base64: class {},
}))
vi.mock('@xterm/addon-image', () => ({
  ImageAddon: class {},
}))
vi.mock('@xterm/addon-progress', () => ({
  ProgressAddon: class {
    // IEvent: called as a function, returns a disposable.
    onChange = () => ({ dispose() {} })
  },
}))
vi.mock('../composables/useTerminal', () => ({
  getXtermTheme: () => ({}),
}))
vi.mock('../composables/useTerminalTheme', () => ({
  resolveXtermBackground: () => ({}),
  resolveTerminalThemeName: () => 'dark',
  applyTerminalBgVar: () => {},
}))
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: () => ({
    settings: { terminal: { fontFamily: 'monospace', fontSize: 14, scrollback: 1000 } },
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
vi.mock('./sessionWriter', () => ({
  queuedSessionWrite: () => {},
}))
vi.mock('../stores/sessionStore', () => ({
  useSessionStore: () => ({
    removeSession,
    getChunkCount: () => 0,
    getDataFromChunk: () => '',
  }),
}))

import {
  acquireTerminal,
  releaseTerminal,
  disposeTerminal,
  transferTerminal,
} from './terminalManager'

// Closing a tab disposes its terminal; the sessionStore buffer (~256 KB per
// session, F-019) must be dropped on exactly that path — no more, no less.
describe('terminalManager session buffer removal wiring (F-019 close-tab leak)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    removeSession.mockClear()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('drops the session buffer when the last ref finishes its disposal timer', () => {
    acquireTerminal('dispose-release', 'ref-a', {})
    releaseTerminal('dispose-release', 'ref-a')
    // Not yet: the 500ms window must survive drag-and-drop lifecycle races.
    expect(removeSession).not.toHaveBeenCalled()

    vi.advanceTimersByTime(500)
    expect(removeSession).toHaveBeenCalledTimes(1)
    expect(removeSession).toHaveBeenCalledWith('dispose-release')
  })

  it('keeps the session buffer while another ref still holds the terminal', () => {
    acquireTerminal('dispose-shared', 'ref-a', {})
    acquireTerminal('dispose-shared', 'ref-b', {})
    releaseTerminal('dispose-shared', 'ref-a')
    vi.advanceTimersByTime(500)
    // ref-b is still mounted — the buffer is live and must not be dropped.
    expect(removeSession).not.toHaveBeenCalled()
  })

  it('keeps the session buffer when re-acquired inside the 500ms window', () => {
    acquireTerminal('dispose-reacquire', 'ref-a', {})
    releaseTerminal('dispose-reacquire', 'ref-a')
    vi.advanceTimersByTime(300)
    acquireTerminal('dispose-reacquire', 'ref-b', {})
    vi.advanceTimersByTime(500)
    expect(removeSession).not.toHaveBeenCalled()
  })

  it('disposeTerminal drops the session buffer immediately', () => {
    acquireTerminal('dispose-explicit', 'ref-a', {})
    disposeTerminal('dispose-explicit')
    expect(removeSession).toHaveBeenCalledTimes(1)
    expect(removeSession).toHaveBeenCalledWith('dispose-explicit')
  })

  it('transferTerminal drops only the old session buffer', () => {
    acquireTerminal('dispose-transfer-old', 'ref-a', {})
    expect(transferTerminal('dispose-transfer-old', 'dispose-transfer-new')).toBe(true)
    expect(removeSession).toHaveBeenCalledWith('dispose-transfer-old')
    // The new id's buffer feeds the reconnect replay — it must survive.
    expect(removeSession).not.toHaveBeenCalledWith('dispose-transfer-new')
  })
})
