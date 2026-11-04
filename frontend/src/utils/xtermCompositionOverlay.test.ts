import { describe, expect, it } from 'vitest'
import {
  compositionText,
  cursorViewportRow,
  imeDebugEnabled,
  isNativeCompositionVisible,
  preeditLeft,
} from './xtermCompositionOverlay'

const host = { left: 0, top: 0, width: 800, height: 400 }

describe('IME preedit overlay helpers', () => {
  it('prefers the composition data and falls back to the textarea value', () => {
    expect(compositionText('shuru', 'shuru')).toBe('shuru')
    // Some IMEs leave compositionupdate.data empty and only fill the textarea.
    expect(compositionText('', 'nihao')).toBe('nihao')
    expect(compositionText(null, 'nihao')).toBe('nihao')
    expect(compositionText(undefined, '')).toBe('')
  })

  it('accepts a native preedit that paints inside the terminal screen', () => {
    expect(isNativeCompositionVisible({ left: 80, top: 40, width: 60, height: 20 }, host)).toBe(true)
  })

  it('rejects a zero-sized, clipped or off-screen native preedit', () => {
    expect(isNativeCompositionVisible({ left: 80, top: 40, width: 0, height: 20 }, host)).toBe(false)
    expect(isNativeCompositionVisible({ left: 80, top: 40, width: 60, height: 0 }, host)).toBe(false)
    // Scrolled/clipped above the visible screen.
    expect(isNativeCompositionVisible({ left: 80, top: -60, width: 60, height: 20 }, host)).toBe(false)
    expect(isNativeCompositionVisible({ left: 900, top: 40, width: 60, height: 20 }, host)).toBe(false)
    // Partially visible still counts: the user can read it.
    expect(isNativeCompositionVisible({ left: -30, top: 40, width: 60, height: 20 }, host)).toBe(true)
  })

  it('keeps diagnostics off unless explicitly enabled', () => {
    expect(imeDebugEnabled()).toBe(false)
  })
})

describe('cursorViewportRow', () => {
  it('equals cursorY when the terminal is scrolled to the bottom', () => {
    // baseY === viewportY: the cursor row and the viewport row coincide.
    expect(cursorViewportRow(120, 7, 120)).toBe(7)
  })

  it('offsets by how far the viewport has been scrolled up', () => {
    // buffer.cursorY is relative to baseY, but the screen repaints the visible
    // rows from viewportY — a preedit pinned to raw cursorY would sit 40 rows
    // above the caret here.
    expect(cursorViewportRow(120, 7, 80)).toBe(47)
  })

  it('goes negative once the cursor is scrolled out of view', () => {
    expect(cursorViewportRow(120, 2, 150)).toBe(-28)
  })
})

describe('preeditLeft', () => {
  const CELL = 8
  const SCREEN = 800

  it('places the preedit on the caret while it fits', () => {
    expect(preeditLeft(10, CELL, 48, SCREEN)).toBe(80)
  })

  it('pulls a preedit back when the caret sits at the right edge', () => {
    // 98 columns in, a 64px string would run 36px past the screen, where
    // .terminal-host's overflow:clip hides it.
    expect(preeditLeft(98, CELL, 64, SCREEN)).toBe(736)
    expect(preeditLeft(98, CELL, 64, SCREEN) + 64).toBe(SCREEN)
  })

  it('pins an over-long preedit to the left edge', () => {
    expect(preeditLeft(90, CELL, 1200, SCREEN)).toBe(0)
  })

  it('never emits an unpositionable value from bogus metrics', () => {
    // Stale cell metrics after a window drag / DPI change can be absurd.
    expect(preeditLeft(50, Number.POSITIVE_INFINITY, 40, SCREEN)).toBe(0)
    expect(preeditLeft(Number.NaN, CELL, 40, SCREEN)).toBe(0)
    expect(preeditLeft(50, CELL, 40, Number.NaN)).toBe(400)
    // A cursor reported left of the screen is clamped to it.
    expect(preeditLeft(-3, CELL, 40, SCREEN)).toBe(0)
  })
})
