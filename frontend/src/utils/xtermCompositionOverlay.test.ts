import { describe, expect, it } from 'vitest'
import { compositionText, imeDebugEnabled, isNativeCompositionVisible } from './xtermCompositionOverlay'

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
