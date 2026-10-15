// Startup background flash fix (issue #1135): the pre-paint script in
// index.html must set <html data-theme> before the first paint instead of
// leaving it to applyTheme(), which only runs after the settings IPC and the
// slow GetAvailableShells probe. The resolver is pure so the index.html
// inline script's cache-then-matchMedia decision is testable.
import { describe, it, expect } from 'vitest'
import { resolveInitialTheme, isValidAppTheme } from './initialTheme'

describe('resolveInitialTheme', () => {
  it('prefers the cached persisted theme over the OS preference', () => {
    expect(resolveInitialTheme('light', true)).toBe('light')
    expect(resolveInitialTheme('dark', false)).toBe('dark')
    expect(resolveInitialTheme('deep-blue', false)).toBe('deep-blue')
  })

  it('falls back to the OS color scheme when nothing is cached', () => {
    expect(resolveInitialTheme(null, true)).toBe('dark')
    expect(resolveInitialTheme(null, false)).toBe('light')
  })

  it('treats an unknown cached value as absent', () => {
    // 'system' is never cached (applyTheme resolves it first) but a tampered
    // or hand-edited localStorage value must not leak into data-theme.
    expect(resolveInitialTheme('system', false)).toBe('light')
    expect(resolveInitialTheme('garbage', true)).toBe('dark')
  })
})

describe('isValidAppTheme', () => {
  it('accepts exactly the data-theme values style.css defines', () => {
    expect(isValidAppTheme('dark')).toBe(true)
    expect(isValidAppTheme('light')).toBe(true)
    expect(isValidAppTheme('deep-blue')).toBe(true)
    expect(isValidAppTheme('system')).toBe(false)
    expect(isValidAppTheme('')).toBe(false)
    expect(isValidAppTheme(null)).toBe(false)
  })
})
