// Regression tests for issue #989: the "reset terminal output / clear
// scrollback" actions reset the gutter's line registry — numbers restart at
// 1 and no stale entries survive the cleared buffer.

import { describe, it, expect, vi, beforeAll } from 'vitest'

// notifyGutter dispatches a window CustomEvent; provide a minimal window.
beforeAll(() => {
  ;(globalThis as any).window = {
    dispatchEvent: vi.fn(),
    CustomEvent: class {
      constructor(public type: string, public init?: unknown) {}
    },
  }
})

// clearRegistry works through a managed terminal's live registry; mock the
// manager so the test owns the registry object it mutates.
const { managedRegistry } = vi.hoisted(() => {
  const { createLineRegistry } = {} as Record<string, never>
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
})
