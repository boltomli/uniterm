import { describe, expect, it } from 'vitest'
import { logicalLineStart } from './overlayHighlight'

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
