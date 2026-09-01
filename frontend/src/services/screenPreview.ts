// Issue #729 — screen preview (屏幕回看): pure helpers behind the scrollbar
// hover preview popup (WindTerm/Termora-style). The popup renders ~10 lines of
// scrollback next to the scrollbar while the mouse hovers over it.
//
// Everything here is pure and structurally typed against xterm.js v6's
// IBufferLine / IBufferCell so it can be tested with plain fakes; the real
// xterm objects satisfy the same shape at runtime.
import type { ITheme } from '@xterm/xterm'

export interface PreviewPalette {
  defaultFg: string
  defaultBg: string
  /** The 16 base ANSI colors, index 0 (black) … 15 (brightWhite). */
  ansi16: string[]
}

export interface PreviewStyleRun {
  text: string
  fg?: string
  bg?: string
  bold?: boolean
  dim?: boolean
  italic?: boolean
  underline?: boolean
  inverse?: boolean
}

/**
 * Structural subset of xterm.js v6 `IBufferCell`. Color modes must be
 * classified through the predicate APIs — `getFgColorMode()` returns masked
 * flag bits in v6 (palette = 16777216/33554432, RGB = 50331648), not 0/1/2.
 */
export interface PreviewBufferCell {
  getWidth(): number
  getChars(): string
  getFgColor(): number
  getBgColor(): number
  isFgRGB(): boolean
  isFgPalette(): boolean
  isBgRGB(): boolean
  isBgPalette(): boolean
  isBold(): boolean | number
  isDim(): boolean | number
  isItalic(): boolean | number
  isUnderline(): boolean | number
  isInverse(): boolean | number
}

/** Structural subset of xterm.js v6 `IBufferLine`. */
export interface PreviewBufferLine {
  getCell(x: number): PreviewBufferCell | undefined
  isWrapped: boolean
}

const FALLBACK_ANSI16 = [
  '#2e3436', '#cc0000', '#4e9a06', '#c4a000',
  '#3465a4', '#75507b', '#06989a', '#d3d7cf',
  '#555753', '#ef2929', '#8ae234', '#fce94f',
  '#729fcf', '#ad7fa8', '#34e2e2', '#eeeeec',
]

const CUBE_STEPS = [0, 95, 135, 175, 215, 255]

export function buildPalette(theme?: Partial<ITheme>): PreviewPalette {
  const ansi16 = [
    'black', 'red', 'green', 'yellow',
    'blue', 'magenta', 'cyan', 'white',
    'brightBlack', 'brightRed', 'brightGreen', 'brightYellow',
    'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite',
  ].map((key) => theme?.[key] || FALLBACK_ANSI16.shift() || '#888888') as string[]
  return {
    defaultFg: theme?.foreground || '#ffffff',
    defaultBg: theme?.background || '#000000',
    ansi16,
  }
}

/** Resolve an xterm cell color (mode + value) to a CSS color string. */
export function colorToCss(
  mode: number,
  color: number,
  palette: PreviewPalette,
): string | undefined {
  if (mode === 0) return undefined
  if (mode === 1) {
    if (color < 0 || color > 255) return undefined
    if (color < 16) return palette.ansi16[color]
    if (color < 232) {
      const n = color - 16
      const r = CUBE_STEPS[Math.floor(n / 36)]
      const g = CUBE_STEPS[Math.floor(n / 6) % 6]
      const b = CUBE_STEPS[n % 6]
      return `rgb(${r}, ${g}, ${b})`
    }
    const v = 8 + (color - 232) * 10
    return `rgb(${v}, ${v}, ${v})`
  }
  if (mode === 2) {
    const r = (color >> 16) & 0xff
    const g = (color >> 8) & 0xff
    const b = color & 0xff
    return `rgb(${r}, ${g}, ${b})`
  }
  return undefined
}

/**
 * Convert one buffer line into styled text runs (consecutive cells with the
 * same style merged). Zero-width cells (the trailing half of CJK wide chars)
 * are skipped and trailing blanks are trimmed.
 */
export function lineToRuns(
  line: PreviewBufferLine,
  cols: number,
  palette: PreviewPalette,
): PreviewStyleRun[] {
  const runs: PreviewStyleRun[] = []
  for (let x = 0; x < cols; x++) {
    const cell = line.getCell(x)
    if (!cell || cell.getWidth() === 0) continue
    const ch = cell.getChars() || ' '

    // xterm v6: classify color modes via the documented predicates —
    // getFgColorMode()'s numeric value is a flag mask, not 0/1/2.
    const fgMode = cell.isFgRGB() ? 2 : cell.isFgPalette() ? 1 : 0
    const bgMode = cell.isBgRGB() ? 2 : cell.isBgPalette() ? 1 : 0
    const fgRaw = colorToCss(fgMode, cell.getFgColor(), palette)
    const bgRaw = colorToCss(bgMode, cell.getBgColor(), palette)
    const inverse = cell.isInverse()
    const fg = inverse ? (bgRaw ?? palette.defaultBg) : fgRaw
    const bg = inverse ? (fgRaw ?? palette.defaultFg) : bgRaw

    const style: PreviewStyleRun = { text: ch }
    if (fg) style.fg = fg
    if (bg) style.bg = bg
    if (cell.isBold()) style.bold = true
    if (cell.isDim()) style.dim = true
    if (cell.isItalic()) style.italic = true
    if (cell.isUnderline()) style.underline = true
    if (inverse) style.inverse = true

    const prev = runs[runs.length - 1]
    if (prev && sameStyle(prev, style)) {
      prev.text += ch
    } else {
      runs.push(style)
    }
  }
  // Trim trailing blank cells (drop runs that are pure spaces, then trim
  // trailing spaces off the last remaining run).
  while (runs.length > 0 && runs[runs.length - 1].text.trim() === '') runs.pop()
  const tail = runs[runs.length - 1]
  if (tail) tail.text = tail.text.replace(/ +$/, '')
  return runs
}

function sameStyle(a: PreviewStyleRun, b: PreviewStyleRun): boolean {
  return (
    a.fg === b.fg && a.bg === b.bg && !!a.bold === !!b.bold && !!a.dim === !!b.dim &&
    !!a.italic === !!b.italic && !!a.underline === !!b.underline && !!a.inverse === !!b.inverse
  )
}

/**
 * Map a scrollbar hover ratio (0 = top, 1 = bottom) to the first buffer row
 * the preview window should show. The window is clamped so that
 * `start + previewRows` never exceeds `totalLines`.
 */
export function computePreviewStart(
  ratio: number,
  totalLines: number,
  rows: number,
  previewRows: number,
): number {
  const r = Math.min(1, Math.max(0, ratio))
  const maxScroll = Math.max(0, totalLines - rows)
  const maxStart = Math.max(0, totalLines - previewRows)
  return Math.min(Math.round(r * maxScroll), maxStart)
}

/**
 * Pick the vertical scrollbar track from candidate track rects. xterm v6 has
 * both a horizontal and a vertical track; the horizontal one can be
 * degenerate (0x0), so require a strictly taller-than-wide, non-empty rect.
 * Returns the index into `rects`, or -1 when none qualifies.
 */
export function pickVerticalTrackIndex(
  rects: Array<{ width: number; height: number }>,
): number {
  for (let i = 0; i < rects.length; i++) {
    if (rects[i].height > 0 && rects[i].height > rects[i].width) return i
  }
  return -1
}
