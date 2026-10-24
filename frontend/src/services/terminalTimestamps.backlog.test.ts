import { describe, it, expect, vi, beforeEach } from 'vitest'

// P-07: line registration must stay linear under a write backlog. Parses run
// in write order, so recordWrite uses the previous parse's end row
// (managed.lastParsedAbs) as the scan start — never the (stale) write-time
// cursor every queued write captured — and the completion backfill (step 2)
// is capped at rows*4 for oversized bands.

const { managedRef } = vi.hoisted(() => ({ managedRef: { current: null as any } }))

vi.mock('./terminalManager', () => ({
  getManagedTerminal: () => managedRef.current,
}))
vi.mock('../stores/settingsStore', () => ({
  // gutterVisible() false → no notifyGutter → no window event in node env.
  useSettingsStore: () => ({ settings: { terminal: {} } }),
}))

import { recordWrite, createLineRegistry, recordWrittenLines } from './terminalTimestamps'
import type { LineRegistryRowSource } from './terminalTimestamps'

/** Fake live terminal: cursor at `cursorY`, rows 0..contentUpTo hold text. */
function makeManaged(opts?: { rows?: number; alternate?: boolean }) {
  const rows = opts?.rows ?? 24
  const perRowCalls = new Map<number, number>()
  const buf = {
    type: opts?.alternate ? ('alternate' as const) : ('normal' as const),
    baseY: 0,
    cursorY: 0,
    contentUpTo: 0,
    getLine(y: number) {
      perRowCalls.set(y, (perRowCalls.get(y) ?? 0) + 1)
      return {
        isWrapped: false,
        translateToString: () => (y <= buf.contentUpTo ? 'x' : ''),
      }
    },
  }
  const managed = {
    terminal: {
      rows,
      buffer: { active: buf },
    },
    lineOffset: 0,
    lastParsedAbs: null as number | null,
    lineRegistry: createLineRegistry(),
  }
  return { managed, buf, perRowCalls }
}

function sid(): string {
  return `p07-${Math.random().toString(36).slice(2)}`
}

describe('recordWrite() backlog scan start (P-07)', () => {
  beforeEach(() => {
    managedRef.current = null
  })

  it('keeps getLine calls linear across 50 backlogged parses', () => {
    const { managed, buf, perRowCalls } = makeManaged()
    managedRef.current = managed
    const id = sid()

    // 50 writes were queued while the cursor sat at row 0; each parse then
    // advances the cursor by one row. Every callback carries the SAME stale
    // write-time fallback (0) — the pre-fix behaviour.
    for (let i = 0; i < 50; i++) {
      buf.cursorY = i
      buf.contentUpTo = i
      recordWrite(id, 0, 1_000 + i)
    }

    // Every row registered exactly once, with sequential numbers.
    expect(managed.lineRegistry.entries.size).toBe(50)
    const numbers = [...managed.lineRegistry.entries.values()].map(m => m.number)
    expect(numbers).toEqual([...Array(50)].map((_, i) => i + 1))

    // Linear, not O(n²): a stale-start scan would probe row 0 on EVERY
    // callback (sum of bands ≈ n²/2 ≈ 1275+ probes) instead of a handful.
    let probes = 0
    for (const n of perRowCalls.values()) probes += n
    expect(probes).toBeLessThan(800)
    expect(perRowCalls.get(0)!).toBeLessThan(10)
  })

  it('falls back to the write-time cursor for the first parse', () => {
    const { managed, buf } = makeManaged()
    managedRef.current = managed
    const id = sid()

    buf.cursorY = 3
    buf.contentUpTo = 3
    recordWrite(id, /* no lastParsedAbs yet → fallback */ 0, 100)

    expect(managed.lastParsedAbs).toBe(3)
    expect([...managed.lineRegistry.entries.keys()]).toEqual([0, 1, 2, 3])
  })

  it('never advances lastParsedAbs while an alternate screen owns the buffer', () => {
    const { managed, buf } = makeManaged()
    managedRef.current = managed
    const id = sid()

    // Prime the normal-buffer position.
    buf.cursorY = 5
    buf.contentUpTo = 5
    recordWrite(id, 0, 100)
    expect(managed.lastParsedAbs).toBe(5)

    // A TUI takes over the alternate screen: its transient rows must not
    // re-anchor the normal-buffer scan position.
    ;(buf as any).type = 'alternate'
    buf.cursorY = 2
    recordWrite(id, 5, 101)
    expect(managed.lastParsedAbs).toBe(5)
    expect(managed.lineRegistry.entries.size).toBe(6)
  })
})

describe('recordWrittenLines() completion backfill cap (P-07)', () => {
  /** Source over fixed content rows 0..9 (all complete above the cursor). */
  const src: LineRegistryRowSource = {
    isWrapped: () => false,
    hasContent: (y) => y >= 0 && y <= 9,
  }

  function seededState() {
    const state = createLineRegistry()
    recordWrittenLines(state, {
      lineOffset: 0,
      beforeAbs: 0,
      afterAbs: 0,
      source: src,
      now: 1,
      rows: 24,
    })
    return state
  }

  it('skips the backfill when the band exceeds rows*4 but still registers', () => {
    const state = seededState()
    expect(state.entries.get(0)?.ts).toBe(1)

    // Band 0..200 > 24*4: registration still runs, completion does not.
    recordWrittenLines(state, {
      lineOffset: 0,
      beforeAbs: 0,
      afterAbs: 200,
      source: src,
      now: 2,
      rows: 24,
    })
    // All content rows still registered (line numbers stay correct)…
    for (let y = 0; y <= 9; y++) {
      expect(state.entries.has(y)).toBe(true)
    }
    // …but their completion timestamps were not rewritten.
    expect(state.entries.get(0)?.ts).toBe(1)
  })

  it('backfills normally for bands within the cap', () => {
    const state = seededState()

    recordWrittenLines(state, {
      lineOffset: 0,
      beforeAbs: 0,
      afterAbs: 20, // ≤ 24*4
      source: src,
      now: 2,
      rows: 24,
    })
    // Cursor ended at row 20, past every content row → completion stamped.
    expect(state.entries.get(0)?.ts).toBe(2)
    expect(state.entries.get(9)?.ts).toBe(2)
  })

  it('caps only when rows is provided (tests omit it → uncapped)', () => {
    const state = seededState()

    recordWrittenLines(state, {
      lineOffset: 0,
      beforeAbs: 0,
      afterAbs: 200,
      source: src,
      now: 2,
    })
    expect(state.entries.get(0)?.ts).toBe(2)
  })
})
