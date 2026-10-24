import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// P-04 (plan A): renderer selection. 'dom' forces the DOM renderer, 'webgl'
// forces WebGL, 'auto' (default) picks WebGL unless keyword highlighting is
// enabled — the highlighter's CellColorOverrideStore patch only exists in
// our fork's DOM renderer. A lost WebGL context falls back to DOM for the
// rest of the session.

const { FakeWebglAddon, settingsState, createdTerminals, FakeTerminal } = vi.hoisted(() => {
  class FakeWebglAddon {
    static instances: FakeWebglAddon[] = []
    disposed = false
    private contextLossCb: (() => void) | null = null
    constructor() {
      FakeWebglAddon.instances.push(this)
    }
    onContextLoss(cb: () => void) {
      this.contextLossCb = cb
    }
    dispose() {
      this.disposed = true
    }
    loseContext() {
      this.contextLossCb?.()
    }
  }
  const createdTerminals: any[] = []
  class FakeTerminal {
    cols = 80
    rows = 24
    unicode = { activeVersion: '' }
    element: unknown = null
    options: Record<string, unknown> = { scrollback: 2500, theme: {} }
    loadedAddons: unknown[] = []
    constructor() {
      createdTerminals.push(this)
    }
    loadAddon(addon: unknown) {
      this.loadedAddons.push(addon)
    }
    attachCustomWheelEventHandler() {}
    onResize() {
      return { dispose() {} }
    }
    onData() {
      return { dispose() {} }
    }
    write() {}
    reset() {}
    open() {
      this.element = { parentElement: null }
    }
    resize(_cols: number, _rows: number) {}
    dispose() {}
  }
  return {
    FakeWebglAddon,
    settingsState: { terminal: { renderer: 'auto', highlightEnabled: true } as Record<string, unknown> },
    createdTerminals,
    FakeTerminal,
  }
})

vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))
vi.mock('@xterm/xterm', () => ({ Terminal: FakeTerminal }))
vi.mock('@xterm/addon-webgl', () => ({ WebglAddon: FakeWebglAddon }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('@xterm/addon-unicode11', () => ({ Unicode11Addon: class {} }))
vi.mock('@xterm/addon-search', () => ({ SearchAddon: class {} }))
vi.mock('@xterm/addon-clipboard', () => ({ ClipboardAddon: class {}, Base64: class {} }))
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
  useSettingsStore: () => ({ settings: settingsState, resolvedAppTheme: 'dark' }),
}))
vi.mock('../stores/localStateStore', () => ({
  useLocalStateStore: () => ({
    state: { sessionLogStates: {}, backgroundEnabled: false, backgroundImage: '' },
    setSessionLogState: () => {},
  }),
}))
vi.mock('../stores/panelStore', () => ({ usePanelStore: () => ({ panels: new Map() }) }))
vi.mock('../stores/tabStore', () => ({
  useTabStore: () => ({ tabs: [], setTabProgress: () => {} }),
}))
vi.mock('../stores/zmodemStore', () => ({
  useZmodemStore: () => ({ getCancelUntil: () => 0 }),
}))
vi.mock('./sessionWriter', () => ({ queuedSessionWrite: () => {} }))
vi.mock('../stores/sessionStore', () => ({
  useSessionStore: () => ({
    removeSession: vi.fn(),
    getData: () => '',
    getChunkCount: () => 0,
    getDataFromChunk: () => '',
  }),
}))
vi.mock('../utils/xtermImeCompatibility', () => ({
  installImeCompatibilityPatch: () => ({ dispose() {} }),
  resetXtermInputState: () => {},
}))

import {
  acquireTerminal,
  attachTerminal,
  disposeTerminal,
  refreshTerminalRenderer,
} from './terminalManager'

let sessionSeq = 0
let currentSid = ''

function setup(renderer: string, highlightEnabled: boolean): string {
  settingsState.terminal.renderer = renderer
  settingsState.terminal.highlightEnabled = highlightEnabled
  const sid = `renderer-${++sessionSeq}`
  currentSid = sid
  acquireTerminal(sid, 'ref-a', {})
  attachTerminal(sid, { appendChild: vi.fn() } as unknown as HTMLElement)
  return sid
}

describe('terminalManager renderer selection (P-04)', () => {
  beforeEach(() => {
    FakeWebglAddon.instances = []
    createdTerminals.length = 0
    // attachTerminal's font-ready path schedules a double rAF fit.
    vi.stubGlobal('requestAnimationFrame', () => 0 as unknown as number)
    vi.stubGlobal('cancelAnimationFrame', () => {})
  })

  afterEach(() => {
    if (currentSid) disposeTerminal(currentSid)
    currentSid = ''
    vi.unstubAllGlobals()
  })

  it('auto keeps DOM while keyword highlighting is on', () => {
    setup('auto', true)
    expect(FakeWebglAddon.instances).toHaveLength(0)
  })

  it('auto switches to WebGL when highlighting is off', () => {
    setup('auto', false)
    expect(FakeWebglAddon.instances).toHaveLength(1)
    expect(FakeWebglAddon.instances[0].disposed).toBe(false)
  })

  it('dom mode never loads WebGL, webgl mode always does', () => {
    setup('dom', false)
    expect(FakeWebglAddon.instances).toHaveLength(0)
    disposeTerminal(currentSid)
    FakeWebglAddon.instances = []
    setup('webgl', true)
    expect(FakeWebglAddon.instances).toHaveLength(1)
  })

  it('re-applies live when the highlight gate flips in auto mode', () => {
    const sid = setup('auto', false)
    expect(FakeWebglAddon.instances).toHaveLength(1)

    // Turning highlighting on must dispose WebGL (the highlighter is
    // DOM-renderer-only), and turning it back off must reload it.
    settingsState.terminal.highlightEnabled = true
    refreshTerminalRenderer(sid)
    expect(FakeWebglAddon.instances[0].disposed).toBe(true)
    expect(FakeWebglAddon.instances).toHaveLength(1)

    settingsState.terminal.highlightEnabled = false
    refreshTerminalRenderer(sid)
    expect(FakeWebglAddon.instances).toHaveLength(2)
  })

  it('falls back to DOM for the session after a context loss', () => {
    const sid = setup('webgl', true)
    expect(FakeWebglAddon.instances).toHaveLength(1)

    FakeWebglAddon.instances[0].loseContext()
    expect(FakeWebglAddon.instances[0].disposed).toBe(true)

    // A later refresh must not try to reload WebGL — it would just lose
    // the context again.
    refreshTerminalRenderer(sid)
    expect(FakeWebglAddon.instances).toHaveLength(1)
  })

  it('disposes the WebGL addon with the terminal', () => {
    setup('webgl', true)
    expect(FakeWebglAddon.instances).toHaveLength(1)
    disposeTerminal(currentSid)
    currentSid = ''
    expect(FakeWebglAddon.instances[0].disposed).toBe(true)
  })
})
