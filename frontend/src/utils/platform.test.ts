import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

const importPlatform = () => import('./platform')

describe('isPhoneLayout', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.unstubAllGlobals()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('is false when there is no window (node/test env)', async () => {
    const { isPhoneLayout } = await importPlatform()
    expect(isPhoneLayout.value).toBe(false)
  })

  it('is true on a mobile platform with a narrow viewport', async () => {
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (Linux; Android 14)' })
    const listener: (() => void)[] = []
    vi.stubGlobal('window', {
      matchMedia: (q: string) => ({
        matches: q === '(max-width: 640px)',
        addEventListener: (_: string, cb: () => void) => listener.push(cb),
      }),
    })
    const { isPhoneLayout } = await importPlatform()
    expect(isPhoneLayout.value).toBe(true)
  })

  it('is false on a mobile platform with a wide viewport, and reacts to change', async () => {
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (Linux; Android 14)' })
    let matches = false
    const listener: (() => void)[] = []
    vi.stubGlobal('window', {
      matchMedia: () => ({
        get matches() {
          return matches
        },
        addEventListener: (_: string, cb: () => void) => listener.push(cb),
      }),
    })
    const { isPhoneLayout } = await importPlatform()
    expect(isPhoneLayout.value).toBe(false)
    matches = true
    listener.forEach((cb) => cb())
    expect(isPhoneLayout.value).toBe(true)
  })
})
