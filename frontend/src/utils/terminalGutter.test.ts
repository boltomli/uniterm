import { describe, it, expect } from 'vitest'
import {
  buildGutterLines,
  lineNumberColumnMetrics,
  MIN_LINE_NUMBER_DIGITS,
  formatTimestampMs,
  resolveRowMeta,
  type BuildGutterLinesOptions,
  type GutterMeta,
} from './terminalGutter'

/** Build options from wrapped flags + a meta map keyed by absolute row. */
function mk(
  wrapped: boolean[],
  entries: Map<number, GutterMeta> = new Map(),
  over?: Partial<Omit<BuildGutterLinesOptions, 'getLine' | 'getMeta'>>
): BuildGutterLinesOptions {
  return {
    rows: wrapped.length,
    viewportY: 0,
    lineOffset: 0,
    cursorAbsoluteY: wrapped.length - 1,
    getLine: (n) => ({ isWrapped: wrapped[n] ?? false }),
    getMeta: (abs) => entries.get(abs),
    ...over,
  }
}

describe('buildGutterLines()', () => {
  it('numbers non-wrapped rows from their registry meta', () => {
    const entries = new Map<number, GutterMeta>([
      [0, { number: 1 }],
      [1, { number: 2 }],
      [2, { number: 3 }],
    ])
    const { lines } = buildGutterLines(mk([false, false, false], entries))
    expect(lines.map((l) => l.lineNumber)).toEqual(['1', '2', '3'])
  })

  it('leaves wrapped continuation rows blank so a wrapped line reads as one', () => {
    // A long command wraps onto rows 0,1,2; row 3 is the next logical line.
    // Numbers are per logical line: no gap where the wrapped rows sit.
    const entries = new Map<number, GutterMeta>([
      [0, { number: 1 }],
      [3, { number: 2 }],
      [4, { number: 3 }],
      [5, { number: 4 }],
    ])
    const { lines } = buildGutterLines(mk([false, true, true, false, false, false], entries))
    expect(lines.map((l) => l.lineNumber)).toEqual(['1', '', '', '2', '3', '4'])
  })

  it('leaves rows below the cursor blank (screen not fully written yet)', () => {
    // cursor is at buffer row 3 (0-based); the 5th row is unwritten.
    const entries = new Map<number, GutterMeta>([
      [0, { number: 1 }],
      [1, { number: 2 }],
      [2, { number: 3 }],
      [3, { number: 4 }],
    ])
    const { lines } = buildGutterLines(mk([false, false, false, false], entries, { rows: 5, cursorAbsoluteY: 3 }))
    expect(lines.map((l) => l.lineNumber)).toEqual(['1', '2', '3', '4', ''])
  })

  it('leaves rows without registry data blank', () => {
    // The empty row under the cursor was never registered (no content yet).
    const entries = new Map<number, GutterMeta>([[0, { number: 1 }]])
    const { lines } = buildGutterLines(mk([false, false], entries))
    expect(lines.map((l) => l.lineNumber)).toEqual(['1', ''])
  })

  it('keeps numbers stable when the viewport scrolls (no positional drift)', () => {
    // Same three logical lines, viewed from buffer row 2: numbers unchanged.
    const entries = new Map<number, GutterMeta>([
      [2, { number: 7 }],
      [3, { number: 8 }],
      [4, { number: 9 }],
    ])
    const { lines } = buildGutterLines(mk([false, false, false, false, false], entries, {
      viewportY: 2,
      rows: 3,
      cursorAbsoluteY: 4,
    }))
    expect(lines.map((l) => l.lineNumber)).toEqual(['7', '8', '9'])
  })

  it('reports the largest visible/hint number for column sizing', () => {
    const entries = new Map<number, GutterMeta>([
      [0, { number: 1 }],
      [1, { number: 2 }],
    ])
    const { maxLineNumber } = buildGutterLines(mk([false, false], entries, { maxNumberHint: 1000 }))
    expect(maxLineNumber).toBe(1000)
    const { maxLineNumber: visible } = buildGutterLines(mk([false, false], entries))
    expect(visible).toBe(2)
  })
})

describe('buildGutterLines() with timestamps', () => {
  it('shows the group timestamp only on non-wrapped rendered rows', () => {
    // row 0 stamped; rows 1,2 wrapped (inherit group time); row 3 own stamp.
    // Local Date construction keeps the assertion timezone-independent.
    const stamp = new Date(2026, 7, 18, 12, 34, 56).getTime()
    const stamp2 = new Date(2026, 7, 18, 13, 0, 1).getTime()
    const entries = new Map<number, GutterMeta>([
      [0, { number: 1, ts: stamp }],
      [3, { number: 2, ts: stamp2 }],
    ])
    const { lines } = buildGutterLines(mk([false, true, true, false], entries, { showTimestamps: true }))
    expect(lines.map((l) => l.timestamp)).toEqual(['12:34:56', '', '', '13:00:01'])
  })

  it('shows nothing when showTimestamps is off', () => {
    const entries = new Map<number, GutterMeta>([[0, { number: 1, ts: 5000 }]])
    const { lines } = buildGutterLines(mk([false, false], entries))
    expect(lines.map((l) => l.timestamp)).toEqual(['', ''])
  })
})

describe('formatTimestampMs', () => {
  it('renders HH:mm:ss by default', () => {
    const d = new Date(2026, 7, 18, 6, 5, 9)
    expect(formatTimestampMs(d.getTime())).toBe('06:05:09')
  })

  it('renders a YYYY-MM-DD HH:mm:ss template', () => {
    const d = new Date(2026, 7, 18, 6, 5, 9)
    expect(formatTimestampMs(d.getTime(), 'YYYY-MM-DD HH:mm:ss')).toBe('2026-08-18 06:05:09')
  })

  it('keeps literals and pads single-digit fields', () => {
    const d = new Date(2026, 0, 3, 9, 4, 5)
    expect(formatTimestampMs(d.getTime(), 'HH:mm')).toBe('09:04')
  })

  it('renders milliseconds with the SSS token', () => {
    const d = new Date(2026, 7, 18, 6, 5, 9, 42)
    expect(formatTimestampMs(d.getTime(), 'HH:mm:ss.SSS')).toBe('06:05:09.042')
  })
})

describe('resolveRowMeta', () => {
  const wrapped = [false, true, true, false] // rows 1,2 are continuation of row 0
  const getLine = (n: number) => ({ isWrapped: wrapped[n] ?? false })

  it('finds the meta from the wrapped group start', () => {
    // rows 1 and 2 belong to the group started at row 0 (line 1).
    const m = resolveRowMeta(2, 0, getLine, (i) => (i === 0 ? { number: 1, ts: 111 } : undefined))
    expect(m).toEqual({ number: 1, ts: 111 })
  })

  it('uses the row itself when it carries the meta', () => {
    const m = resolveRowMeta(3, 0, getLine, (i) => (i === 3 ? { number: 2 } : undefined))
    expect(m).toEqual({ number: 2 })
  })

  it('returns undefined when no row in the group is registered', () => {
    const m = resolveRowMeta(1, 0, getLine, () => undefined)
    expect(m).toBeUndefined()
  })
})


describe('line-number column reservation', () => {
  it('reserves the same five-digit width from initialization through 99999', () => {
    const initial = lineNumberColumnMetrics(8, 0)
    expect(initial).toEqual({ digits: MIN_LINE_NUMBER_DIGITS, width: 48 })
    for (const line of [1, 9, 10, 99, 100, 999, 1000, 9999, 10000, 99999]) {
      expect(lineNumberColumnMetrics(8, line)).toEqual(initial)
    }
  })

  it('expands beyond five digits and retains the reservation when visible numbers decrease', () => {
    const expanded = lineNumberColumnMetrics(8, 100000)
    expect(expanded).toEqual({ digits: 6, width: 56 })
    // The shared terminal carries digits through scrollback, alternate-screen
    // switches, line-registry clears and gutter component remounts.
    for (const line of [99999, 100, 24, 1, 0]) {
      expect(lineNumberColumnMetrics(8, line, expanded.digits)).toEqual(expanded)
    }
    expect(lineNumberColumnMetrics(8, 1000000, expanded.digits)).toEqual({ digits: 7, width: 64 })
  })

  it('recomputes pixel width for a font change without losing reserved digits', () => {
    const expanded = lineNumberColumnMetrics(8, 100000)
    expect(lineNumberColumnMetrics(10.25, 1, expanded.digits)).toEqual({ digits: 6, width: 70 })
    expect(lineNumberColumnMetrics(6, 1, expanded.digits)).toEqual({ digits: 6, width: 44 })
  })

  it('starts a new terminal at five digits instead of carrying another session width', () => {
    expect(lineNumberColumnMetrics(8, 1)).toEqual({ digits: 5, width: 48 })
  })
})
