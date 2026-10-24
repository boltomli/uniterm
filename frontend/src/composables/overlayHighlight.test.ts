import { describe, expect, it } from 'vitest'
import { logicalLineStart, pruneOverrideStore, shiftOverrideStore } from './overlayHighlight'

// xterm marks isWrapped on the row wrapped TO: row N.isWrapped=true ⇒
// row N continues row N-1. The walk-up must ask each row about ITSELF,
// not about its predecessor.
function fakeBuffer(flags: boolean[]) {
  return (y: number) => (y >= 0 && y < flags.length ? { isWrapped: flags[y] } : undefined)
}

// rows: 0 start, 1 wrap, 2 wrap, 3 start (next entry), 4 wrap, 5 start
const flags = [false, true, true, false, true, false]

describe('logicalLineStart()', () => {
  it('keeps an entry-start row after a wrapped row as its own group', () => {
    // Regression: checking the PREVIOUS row's isWrapped drags a start row
    // into the group above, and the downward walk then excludes it — the
    // row is never scanned and keeps stale overrides.
    expect(logicalLineStart(fakeBuffer(flags), 3, 64)).toBe(3)
    expect(logicalLineStart(fakeBuffer(flags), 5, 64)).toBe(5)
  })

  it('walks a continuation row up to its logical line start', () => {
    expect(logicalLineStart(fakeBuffer(flags), 1, 64)).toBe(0)
    expect(logicalLineStart(fakeBuffer(flags), 2, 64)).toBe(0)
    expect(logicalLineStart(fakeBuffer(flags), 4, 64)).toBe(3)
  })

  it('stops at buffer top and honours the stitch cap', () => {
    expect(logicalLineStart(fakeBuffer(flags), 0, 64)).toBe(0)
    const long = Array.from({ length: 20 }, (_, i) => i > 0)
    // Cap of 5 rows: from y=10 the walk may climb at most 4 rows.
    expect(logicalLineStart(fakeBuffer(long), 10, 5)).toBe(6)
  })
})

// shiftLineCache is gone (P-06): the span caches key on
// `index + totalTrim`, so trims never rebuild them — covered by the
// trim-holdover test in overlayHighlight.refresh.test.ts.

describe('pruneOverrideStore()', () => {
  function fakeTerm(overrides?: Map<number, unknown>) {
    return (overrides
      ? { _core: { cellColorOverrides: { _overrides: overrides } } }
      : {}) as unknown as Parameters<typeof pruneOverrideStore>[0]
  }

  it('drops entries more than the slack window above the viewport', () => {
    const store = new Map<number, unknown>([
      [1, 'far-above'],
      [12, 'near-above'],
      [20, 'in-viewport'],
    ])
    expect(pruneOverrideStore(fakeTerm(store), 10)).toBe(true)
    // Same Map instance (the renderer holds a reference).
    expect([...store.entries()]).toEqual([
      [12, 'near-above'],
      [20, 'in-viewport'],
    ])
  })

  it('is a no-op when everything is within the window', () => {
    const store = new Map<number, unknown>([[0, 'a'], [5, 'b']])
    expect(pruneOverrideStore(fakeTerm(store), -10)).toBe(true)
    expect(store.size).toBe(2)
  })

  it('returns false when the fork store is unavailable', () => {
    expect(pruneOverrideStore(fakeTerm(), 10)).toBe(false)
    expect(pruneOverrideStore(fakeTerm(new Map()), 0)).toBe(true)
  })
})

describe('shiftOverrideStore()', () => {
  // Minimal stand-in for the xterm fork: Terminal._core.cellColorOverrides
  // owns a private lineY → spans Map.
  function fakeTerm(overrides?: Map<number, unknown>) {
    return (overrides
      ? { _core: { cellColorOverrides: { _overrides: overrides } } }
      : {}) as unknown as Parameters<typeof shiftOverrideStore>[0]
  }

  it('re-keys surviving lines and drops trimmed ones in place', () => {
    const store = new Map<number, unknown>([
      [2, 'trimmed'],
      [5, 'kept-a'],
      [9, 'kept-b'],
    ])
    const map = store
    expect(shiftOverrideStore(fakeTerm(store), 3)).toBe(true)
    // Same Map instance (the renderer holds a reference), re-keyed.
    expect(map).toBe(store)
    expect([...store.entries()]).toEqual([
      [2, 'kept-a'],
      [6, 'kept-b'],
    ])
  })

  it('is a no-op for a zero trim', () => {
    const store = new Map<number, unknown>([[3, 'x']])
    expect(shiftOverrideStore(fakeTerm(store), 0)).toBe(true)
    expect([...store.keys()]).toEqual([3])
  })

  it('reports failure when the fork store is unavailable', () => {
    expect(shiftOverrideStore(fakeTerm(), 5)).toBe(false)
    expect(shiftOverrideStore(fakeTerm(new Map()), 0)).toBe(true)
  })
})
