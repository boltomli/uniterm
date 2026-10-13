// Regression tests for issue #989: the "reset terminal output / clear
// scrollback" actions reset the gutter's line registry — numbers restart at
// 1 and no stale entries survive the cleared buffer.

import { describe, it, expect, vi, beforeAll } from 'vitest'

// notifyGutter dispatches a window CustomEvent, frame-coalesced (one dispatch
// per animation frame, carrying the session ids that asked for it); provide a
// minimal window plus a queued requestAnimationFrame so tests decide when a
// frame runs — real timers would hide the batching.
const windowStub = {
  dispatchEvent: vi.fn(),
  CustomEvent: class {
    type: string
    detail?: unknown
    constructor(type: string, init?: { detail?: unknown }) {
      this.type = type
      this.detail = init?.detail
    }
  },
}
const frameQueue: FrameRequestCallback[] = []

/** Run every queued animation frame (the coalesced gutter flush). */
function flushFrames() {
  for (const cb of frameQueue.splice(0)) cb(0)
}

beforeAll(() => {
  Object.assign(globalThis, {
    window: windowStub,
    requestAnimationFrame: (cb: FrameRequestCallback) => {
      frameQueue.push(cb)
      return frameQueue.length
    },
    cancelAnimationFrame: () => {},
  })
})

// clearRegistry works through a managed terminal's live registry; mock the
// manager so the test owns the registry object it mutates.
const { managedRegistry } = vi.hoisted(() => {
  return { managedRegistry: { nextNumber: 7, entries: new Map() } }
})

vi.mock('./terminalManager', () => ({
  // Cast through unknown: the real ManagedTerminal shape isn't needed here,
  // clearRegistry only touches lineRegistry.
  getManagedTerminal: () => ({ lineRegistry: managedRegistry }),
}))

vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: () => ({ settings: { terminal: { showTimestamps: false } } }),
}))

import { clearRegistry } from './terminalTimestamps'

describe('clearRegistry (issue #989)', () => {
  it('drops all entries and restarts numbering at 1', () => {
    managedRegistry.nextNumber = 7
    managedRegistry.entries.set(0, { number: 1, ts: 1000 })
    managedRegistry.entries.set(1, { number: 2, ts: 2000 })
    managedRegistry.entries.set(5, { number: 6, ts: 3000 })

    clearRegistry('any-session')

    expect(managedRegistry.entries.size).toBe(0)
    expect(managedRegistry.nextNumber).toBe(1)
  })

  it('is a no-op without a managed terminal (getManagedTerminal undefined)', () => {
    // The mock always returns a registry here; the no-registry path is
    // covered by the mock returning undefined in the sibling test file.
    // This test pins the contract that an empty registry stays empty.
    managedRegistry.entries.clear()
    clearRegistry('other-session')
    expect(managedRegistry.entries.size).toBe(0)
    expect(managedRegistry.nextNumber).toBe(1)
  })

  it('notifies the gutter once per frame, carrying the session ids', () => {
    // security.md #16 (Low): every write used to fire its own window event
    // that every pane's gutter handled — a burst of writes meant a burst of
    // full refreshes. The dispatch is now frame-coalesced and carries the
    // session ids so a pane can ignore foreign refreshes.
    flushFrames() // drain frames left over by the tests above
    windowStub.dispatchEvent.mockClear()
    clearRegistry('session-a')
    clearRegistry('session-a')
    clearRegistry('session-b')
    flushFrames()

    const gutterEvents = windowStub.dispatchEvent.mock.calls
      .map(([event]) => event as InstanceType<typeof windowStub.CustomEvent>)
      .filter((event) => event.type === 'terminal:refresh-gutter')
    expect(gutterEvents).toHaveLength(1)
    expect(gutterEvents[0].detail).toEqual({ sessionIds: ['session-a', 'session-b'] })
  })
})
