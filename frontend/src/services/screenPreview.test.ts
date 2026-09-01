// Issue #729 — screen preview (屏幕回看): pure logic for rendering xterm.js
// buffer lines into styled runs for the hover preview popup, and for mapping
// a scrollbar hover position to a buffer row.
//
// These functions are structurally typed against xterm.js's IBufferLine /
// IBufferCell APIs so tests can exercise them with plain fakes, and the real
// xterm objects satisfy the same shape at runtime.
import { describe, it, expect } from 'vitest'
import {
  buildPalette,
  colorToCss,
  lineToRuns,
  computePreviewStart,
  pickVerticalTrackIndex,
  type PreviewPalette,
  type PreviewBufferCell,
} from './screenPreview'

const palette: PreviewPalette = {
  defaultFg: '#cccccc',
  defaultBg: '#1e1e1e',
  ansi16: [
    '#000000', // 0 black
    '#cd3131', // 1 red
    '#0dbc79', // 2 green
    '#e5e510', // 3 yellow
    '#2472c8', // 4 blue
    '#bc3fbc', // 5 magenta
    '#11a8cd', // 6 cyan
    '#e5e5e5', // 7 white
    '#666666', // 8 brightBlack
    '#f14c4c', // 9 brightRed
    '#23d18b', // 10 brightGreen
    '#f5f543', // 11 brightYellow
    '#3b8eea', // 12 brightBlue
    '#d670d6', // 13 brightMagenta
    '#29b8db', // 14 brightCyan
    '#ffffff', // 15 brightWhite
  ],
}

function makeLine(cells: Array<Partial<PreviewBufferCell>>) {
  return {
    isWrapped: false,
    getCell(x: number): PreviewBufferCell | undefined {
      return cells[x]
    },
    translateToString(): string {
      return ''
    },
  }
}

describe('buildPalette', () => {
  it('maps the 16 ANSI colors from an xterm theme', () => {
    const p = buildPalette({
      foreground: '#f0f0f0',
      background: '#101010',
      black: '#010101',
      red: '#020202',
      green: '#030303',
      yellow: '#040404',
      blue: '#050505',
      magenta: '#060606',
      cyan: '#070707',
      white: '#080808',
      brightBlack: '#090909',
      brightRed: '#0a0a0a',
      brightGreen: '#0b0b0b',
      brightYellow: '#0c0c0c',
      brightBlue: '#0d0d0d',
      brightMagenta: '#0e0e0e',
      brightCyan: '#0f0f0f',
      brightWhite: '#101010',
    })
    expect(p.defaultFg).toBe('#f0f0f0')
    expect(p.defaultBg).toBe('#101010')
    expect(p.ansi16[0]).toBe('#010101')
    expect(p.ansi16[9]).toBe('#0a0a0a')
    expect(p.ansi16[15]).toBe('#101010')
  })

  it('falls back to sane defaults for missing theme entries', () => {
    const p = buildPalette(undefined)
    expect(p.defaultFg).toBeTruthy()
    expect(p.defaultBg).toBeTruthy()
    expect(p.ansi16).toHaveLength(16)
    expect(p.ansi16.every((c) => typeof c === 'string' && c.length > 0)).toBe(true)
  })
})

describe('colorToCss', () => {
  it('returns undefined for the default color mode (0)', () => {
    expect(colorToCss(0, 0, palette)).toBeUndefined()
  })

  // xterm.js v6 color modes: 0 = default, 1 = palette (index 0-255,
  // covering both the 16 base colors and the 256-color set), 2 = RGB.
  it('resolves palette 0-15 colors through ansi16', () => {
    expect(colorToCss(1, 4, palette)).toBe('#2472c8')
    expect(colorToCss(1, 9, palette)).toBe('#f14c4c')
  })

  it('resolves palette 256-color cube indices 16-231', () => {
    // 196 = cube (5,0,0) → rgb(255, 0, 0)
    expect(colorToCss(1, 196, palette)).toBe('rgb(255, 0, 0)')
    // 17 = cube (0,0,1) → rgb(0, 0, 95)
    expect(colorToCss(1, 17, palette)).toBe('rgb(0, 0, 95)')
    // 231 = cube (5,5,5) → rgb(255, 255, 255)
    expect(colorToCss(1, 231, palette)).toBe('rgb(255, 255, 255)')
  })

  it('resolves palette grayscale indices 232-255', () => {
    expect(colorToCss(1, 232, palette)).toBe('rgb(8, 8, 8)')
    expect(colorToCss(1, 255, palette)).toBe('rgb(238, 238, 238)')
  })

  it('resolves direct RGB mode (2)', () => {
    expect(colorToCss(2, 0xff0000, palette)).toBe('rgb(255, 0, 0)')
    expect(colorToCss(2, 0x102030, palette)).toBe('rgb(16, 32, 48)')
  })

  it('returns undefined for out-of-range indices', () => {
    expect(colorToCss(1, 999, palette)).toBeUndefined()
  })
})

describe('lineToRuns', () => {
  function cell(over: Partial<PreviewBufferCell>): PreviewBufferCell {
    return {
      getWidth: () => 1,
      getChars: () => 'a',
      getFgColor: () => 0,
      getBgColor: () => 0,
      isFgRGB: () => false,
      isFgPalette: () => false,
      isBgRGB: () => false,
      isBgPalette: () => false,
      isBold: () => false,
      isDim: () => false,
      isItalic: () => false,
      isUnderline: () => false,
      isInverse: () => false,
      ...over,
    }
  }

  it('joins consecutive same-style cells into a single run', () => {
    const line = makeLine([cell({ getChars: () => 'a' }), cell({ getChars: () => 'b' }), cell({ getChars: () => 'c' })])
    expect(lineToRuns(line, 3, palette)).toEqual([{ text: 'abc' }])
  })

  it('splits runs when the style changes', () => {
    const line = makeLine([
      cell({ getChars: () => 'a' }),
      cell({ getChars: () => 'b', isBold: () => true }),
      cell({ getChars: () => 'c', isBold: () => true }),
    ])
    expect(lineToRuns(line, 3, palette)).toEqual([
      { text: 'a' },
      { text: 'bc', bold: true },
    ])
  })

  it('resolves foreground and background colors', () => {
    const line = makeLine([
      cell({ getChars: () => 'x', isFgPalette: () => true, getFgColor: () => 1 }),
      cell({ getChars: () => 'y', isBgRGB: () => true, getBgColor: () => 0x00ff00 }),
    ])
    expect(lineToRuns(line, 2, palette)).toEqual([
      { text: 'x', fg: '#cd3131' },
      { text: 'y', bg: 'rgb(0, 255, 0)' },
    ])
  })

  it('resolves colors via predicate APIs even when getFgColorMode returns v6 flag masks', () => {
    // Regression: xterm v6's getFgColorMode() returns masked flag bits
    // (palette = 16777216/33554432, RGB = 50331648), NOT 0/1/2. Cells must be
    // classified through isFgRGB()/isFgPalette() predicates instead.
    const line = makeLine([
      cell({ getChars: () => 'p', isFgPalette: () => true, getFgColorMode: () => 16777216, getFgColor: () => 4 }),
      cell({ getChars: () => 'r', isFgRGB: () => true, getFgColorMode: () => 50331648, getFgColor: () => 0xff8000 }),
    ])
    expect(lineToRuns(line, 2, palette)).toEqual([
      { text: 'p', fg: '#2472c8' },
      { text: 'r', fg: 'rgb(255, 128, 0)' },
    ])
  })

  it('swaps colors for inverse video', () => {
    const line = makeLine([
      cell({ getChars: () => 'i', isInverse: () => true, isFgPalette: () => true, getFgColor: () => 1 }),
    ])
    // inverse: fg becomes the default background, bg becomes the cell fg
    expect(lineToRuns(line, 1, palette)).toEqual([
      { text: 'i', fg: '#1e1e1e', bg: '#cd3131', inverse: true },
    ])
  })

  it('skips zero-width continuation cells (CJK wide chars)', () => {
    const line = makeLine([
      cell({ getChars: () => '中', getWidth: () => 2 }),
      cell({ getChars: () => '', getWidth: () => 0 }), // trailing half of the wide char
      cell({ getChars: () => 'b' }),
    ])
    expect(lineToRuns(line, 3, palette)).toEqual([{ text: '中b' }])
  })

  it('trims trailing blank cells', () => {
    const line = makeLine([
      cell({ getChars: () => 'a' }),
      cell({ getChars: () => '' }),
      cell({ getChars: () => '' }),
    ])
    expect(lineToRuns(line, 3, palette)).toEqual([{ text: 'a' }])
  })

  it('returns an empty array for an empty line', () => {
    expect(lineToRuns(makeLine([]), 5, palette)).toEqual([])
  })
})

describe('computePreviewStart', () => {
  const rows = 30
  const previewRows = 10

  it('maps ratio 0 to the top of the buffer', () => {
    expect(computePreviewStart(0, 100, rows, previewRows)).toBe(0)
  })

  it('maps ratio 1 to the last scrollable position', () => {
    // 100 lines, 30 viewport rows → max scroll top is 70
    expect(computePreviewStart(1, 100, rows, previewRows)).toBe(70)
  })

  it('is monotonic between 0 and 1', () => {
    const prev = computePreviewStart(0.25, 100, rows, previewRows)
    const mid = computePreviewStart(0.5, 100, rows, previewRows)
    const next = computePreviewStart(0.75, 100, rows, previewRows)
    expect(prev).toBeLessThan(mid)
    expect(mid).toBeLessThan(next)
  })

  it('clamps out-of-range ratios', () => {
    expect(computePreviewStart(-0.5, 100, rows, previewRows)).toBe(0)
    expect(computePreviewStart(1.5, 100, rows, previewRows)).toBe(70)
  })

  it('keeps the preview window inside the buffer', () => {
    // 12 total lines, 30 rows, 10 preview rows: ratio 1 → start 0
    // (max scroll is negative → no scrolling, start clamped to 0)
    expect(computePreviewStart(1, 12, rows, previewRows)).toBe(0)
  })

  it('returns 0 for buffers with no scroll room', () => {
    expect(computePreviewStart(0.5, 10, rows, previewRows)).toBe(0)
  })
})

describe('pickVerticalTrackIndex', () => {
  it('picks the vertical track when the degenerate 0x0 horizontal track comes first', () => {
    // Real-world shape seen in diagnostics: horizontal 0x0, vertical 14x699.
    expect(pickVerticalTrackIndex([
      { width: 0, height: 0 },
      { width: 14, height: 699 },
    ])).toBe(1)
  })

  it('picks the only track when just the vertical one exists', () => {
    expect(pickVerticalTrackIndex([{ width: 14, height: 699 }])).toBe(0)
  })

  it('returns -1 when no candidate is a plausible vertical track', () => {
    expect(pickVerticalTrackIndex([{ width: 0, height: 0 }])).toBe(-1)
    expect(pickVerticalTrackIndex([{ width: 699, height: 14 }])).toBe(-1)
    expect(pickVerticalTrackIndex([])).toBe(-1)
  })
})
