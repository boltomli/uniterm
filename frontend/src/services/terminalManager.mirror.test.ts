import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// P-01 (plan A): while a panel is foreground the headless mirror must only
// QUEUE session:data — parsing every chunk twice was the dominant
// main-thread cost of high-volume output. The backlog drains on idle, and
// while draining (old data) the mirror must not answer device queries.

const {
  sessionDataHandlers,
  queuedSessionWrite,
  storeGetData,
  storeGetChunkCount,
  storeGetDataFromChunk,
  createdTerminals,
  FakeTerminal,
} = vi.hoisted(() => {
  // Minimal xterm stand-in that records every write (with its parse
  // callback) and every onData responder registration.
  const createdTerminals: any[] = []
  class FakeTerminal {
    cols = 80
    rows = 24
    unicode = { activeVersion: '' }
    writes: Array<{ data: string; cb?: () => void }> = []
    resets = 0
    dataHandlers: Array<(data: string) => void> = []
    constructor() {
      createdTerminals.push(this)
    }
    loadAddon() {}
    attachCustomWheelEventHandler() {}
    onResize() {
      return { dispose() {} }
    }
    onData(cb: (data: string) => void) {
      this.dataHandlers.push(cb)
      return { dispose() {} }
    }
    write(data: string, cb?: () => void) {
      this.writes.push({ data, cb })
    }
    reset() {
      this.resets++
    }
    resize(_cols: number, _rows: number) {}
    dispose() {}
  }
  return {
    sessionDataHandlers: [] as Array<(ev: any) => void>,
    queuedSessionWrite: vi.fn(),
    storeGetData: vi.fn(() => ''),
    storeGetChunkCount: vi.fn(() => 0),
    storeGetDataFromChunk: vi.fn(() => ''),
    createdTerminals,
    FakeTerminal,
  }
})

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: vi.fn((name: string, cb: (ev: any) => void) => {
      if (name === 'session:data') sessionDataHandlers.push(cb)
      return () => {
        const i = sessionDataHandlers.indexOf(cb)
        if (i >= 0) sessionDataHandlers.splice(i, 1)
      }
    }),
    Off: vi.fn(),
  },
}))

// Minimal xterm stand-in (defined in vi.hoisted above, so the hoisted mock
// factories can reference it).
vi.mock('@xterm/xterm', () => ({ Terminal: FakeTerminal }))
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
vi.mock('../stores/panelStore', () => ({ usePanelStore: () => ({ panels: new Map() }) }))
vi.mock('../stores/tabStore', () => ({
  useTabStore: () => ({ tabs: [], setTabProgress: () => {} }),
}))
vi.mock('../stores/zmodemStore', () => ({
  useZmodemStore: () => ({ getCancelUntil: () => 0 }),
}))
vi.mock('./sessionWriter', () => ({ queuedSessionWrite: (...args: any[]) => queuedSessionWrite(...args) }))
vi.mock('../stores/sessionStore', () => ({
  useSessionStore: () => ({
    removeSession: vi.fn(),
    getData: storeGetData,
    getChunkCount: storeGetChunkCount,
    getDataFromChunk: storeGetDataFromChunk,
  }),
}))

import {
  acquireTerminal,
  disposeTerminal,
  markTerminalActive,
} from './terminalManager'

// Deliver a session:data event to every mirror subscribed to it.
function emitSessionData(id: string, data: string): void {
  for (const handler of [...sessionDataHandlers]) {
    handler({ data: { id, data } })
  }
}

// The mirror is the LAST Terminal constructed for a session (acquire creates
// the visible terminal first, then createMirror).
interface MirrorLike {
  writes: Array<{ data: string; cb?: () => void }>
  resets: number
  dataHandlers: Array<(data: string) => void>
}
function mirrorOf(): MirrorLike {
  return createdTerminals[createdTerminals.length - 1] as MirrorLike
}

// Idle callbacks armed via the requestIdleCallback stub (P-01 catch-up).
let idleCallbacks: Array<(deadline?: unknown) => void> = []

let sessionSeq = 0
let currentSid = ''

function setupSession(active: boolean): string {
  const sid = `mirror-${++sessionSeq}`
  currentSid = sid
  acquireTerminal(sid, 'ref-a', {})
  if (active) markTerminalActive(sid, 'ref-a', true)
  return sid
}

describe('terminalManager mirror foreground queueing (P-01 plan A)', () => {
  beforeEach(() => {
    idleCallbacks = []
    vi.stubGlobal('requestIdleCallback', (cb: (deadline?: unknown) => void) => {
      idleCallbacks.push(cb)
      return idleCallbacks.length
    })
    vi.stubGlobal('cancelIdleCallback', vi.fn())
    queuedSessionWrite.mockClear()
    storeGetData.mockReturnValue('')
    storeGetChunkCount.mockReturnValue(0)
    createdTerminals.length = 0
  })

  afterEach(() => {
    if (currentSid) disposeTerminal(currentSid)
    currentSid = ''
    vi.unstubAllGlobals()
  })

  it('does not parse session:data while the panel is foreground', () => {
    const sid = setupSession(true)
    const mirror = mirrorOf()

    emitSessionData(sid, 'seq 1 2000000')
    emitSessionData(sid, 'more output')

    expect(mirror.writes).toEqual([])
    // The backlog is queued, not lost.
    expect(mirror.resets).toBe(0)
  })

  it('writes directly while the panel is in the background', () => {
    const sid = setupSession(false)
    const mirror = mirrorOf()

    emitSessionData(sid, 'background output')

    expect(mirror.writes.map(w => w.data)).toEqual(['background output'])
  })

  it('flushes the whole backlog in order when the panel deactivates', () => {
    const sid = setupSession(true)
    const mirror = mirrorOf()

    emitSessionData(sid, 'A')
    emitSessionData(sid, 'B')
    expect(mirror.writes).toEqual([])

    markTerminalActive(sid, 'ref-a', false)

    // One coalesced catch-up write, oldest data first…
    expect(mirror.writes.map(w => w.data)).toEqual(['AB'])
    // …and live background data appends after it, preserving stream order.
    emitSessionData(sid, 'C')
    expect(mirror.writes.map(w => w.data)).toEqual(['AB', 'C'])
  })

  it('stays silent for device replies while catch-up is in flight', () => {
    const sid = setupSession(true)
    const mirror = mirrorOf()

    emitSessionData(sid, 'old output with a query')
    markTerminalActive(sid, 'ref-a', false)
    expect(mirror.writes).toHaveLength(1)

    // Catch-up parse not finished yet: the queued data's query was already
    // answered by the visible terminal — a second reply is the "0R" bug.
    mirror.dataHandlers.forEach(h => h('\x1b[c'))
    expect(queuedSessionWrite).not.toHaveBeenCalled()

    // Catch-up finished and the backlog is empty: the mirror resumes as the
    // background responder.
    mirror.writes[0].cb?.()
    mirror.dataHandlers.forEach(h => h('\x1b[c'))
    expect(queuedSessionWrite).toHaveBeenCalledWith(sid, '\x1b[c')
  })

  it('drains the backlog on requestIdleCallback while foreground', () => {
    const sid = setupSession(true)
    const mirror = mirrorOf()

    emitSessionData(sid, 'x'.repeat(1000))
    expect(mirror.writes).toEqual([])
    expect(idleCallbacks).toHaveLength(1)

    idleCallbacks.pop()!()
    expect(mirror.writes.map(w => w.data)).toEqual(['x'.repeat(1000)])
    expect(mirror.writes[0].cb).toBeTypeOf('function')
  })

  it('batches idle drains so one callback never takes the whole backlog', () => {
    const sid = setupSession(true)
    const mirror = mirrorOf()

    // 3 × 40 KiB: a single idle drain may take at most 64 KiB of entries.
    emitSessionData(sid, 'a'.repeat(40 * 1024))
    emitSessionData(sid, 'b'.repeat(40 * 1024))
    emitSessionData(sid, 'c'.repeat(40 * 1024))

    idleCallbacks.pop()!()
    expect(mirror.writes).toHaveLength(1)
    expect(mirror.writes[0].data).toHaveLength(80 * 1024)

    // The parse callback re-arms an idle drain for the remainder.
    mirror.writes[0].cb?.()
    expect(idleCallbacks).toHaveLength(1)
    idleCallbacks.pop()!()
    expect(mirror.writes).toHaveLength(2)
    expect(mirror.writes[1].data).toHaveLength(40 * 1024)
  })

  it('caps the backlog by resetting and replaying recent session history', () => {
    storeGetData.mockReturnValue('REPLAY')
    const sid = setupSession(true)
    const mirror = mirrorOf()

    emitSessionData(sid, 'y'.repeat(2 * 1024 * 1024 + 1))

    // Over the 2 MiB cap: queue dropped, mirror reset, history staged.
    expect(mirror.resets).toBe(1)
    expect(mirror.writes).toEqual([])

    idleCallbacks.pop()!()
    expect(mirror.writes.map(w => w.data)).toEqual(['REPLAY'])
  })

  it('does not answer queries while the panel is foreground', () => {
    setupSession(true)
    const mirror = mirrorOf()

    // Foreground: the visible terminal is the responder.
    mirror.dataHandlers.forEach(h => h('\x1b[c'))
    expect(queuedSessionWrite).not.toHaveBeenCalled()
  })
})
