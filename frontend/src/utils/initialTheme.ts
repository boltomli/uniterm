// Startup background flash fix (issue #1135): the first paint must already
// carry the user's theme. The pre-paint script in index.html reads the theme
// that applyTheme() cached in localStorage and applies it to <html>, falling
// back to the OS color scheme (correct for the default 'system' theme) when
// nothing is cached yet. This module holds the pure decision logic; the
// index.html script duplicates it inline because it must run before the
// module graph loads — keep the two in sync.
export type InitialTheme = 'dark' | 'light' | 'deep-blue'

// The data-theme values style.css defines. 'system' is deliberately absent:
// applyTheme() always caches the resolved concrete theme, never the setting.
const VALID_THEMES: readonly string[] = ['dark', 'light', 'deep-blue']

export function isValidAppTheme(value: string | null): value is InitialTheme {
  return value !== null && VALID_THEMES.includes(value)
}

export function resolveInitialTheme(cached: string | null, prefersDark: boolean): InitialTheme {
  if (isValidAppTheme(cached)) {
    return cached
  }
  return prefersDark ? 'dark' : 'light'
}
