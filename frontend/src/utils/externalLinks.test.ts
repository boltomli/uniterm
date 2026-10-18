// F-09c: clicks inside the webview must never navigate the app window.
// A remote page opened in the WebView inherits the injected Wails runtime —
// every Go binding — which is RCE. External links go to the SYSTEM browser,
// dangerous schemes are dropped outright.

import { describe, expect, it, vi, beforeEach } from 'vitest'

vi.mock('@wailsio/runtime', () => ({
  Browser: { OpenURL: vi.fn() },
}))

import { decideLinkAction, installExternalLinkGuard, openExternalURL } from './externalLinks'
import { Browser } from '@wailsio/runtime'

const APP_ORIGIN = 'http://wails.localhost:34115'

describe('decideLinkAction', () => {
  it('keeps in-page and same-origin targets in the app', () => {
    expect(decideLinkAction('', APP_ORIGIN)).toEqual({ kind: 'allow' })
    expect(decideLinkAction('#section', APP_ORIGIN)).toEqual({ kind: 'allow' })
    expect(decideLinkAction('/settings', APP_ORIGIN)).toEqual({ kind: 'allow' })
    expect(decideLinkAction('docs/page.html', APP_ORIGIN)).toEqual({ kind: 'allow' })
    expect(decideLinkAction(`${APP_ORIGIN}/index.html`, APP_ORIGIN)).toEqual({ kind: 'allow' })
  })

  it('routes http(s)/mailto targets to the system browser', () => {
    expect(decideLinkAction('https://evil.example/pwn', APP_ORIGIN)).toEqual({
      kind: 'open-external',
      url: 'https://evil.example/pwn',
    })
    expect(decideLinkAction('http://127.0.0.1:8080/x', APP_ORIGIN).kind).toBe('open-external')
    expect(decideLinkAction('mailto:ops@example.com', APP_ORIGIN).kind).toBe('open-external')
    // Protocol-relative resolves against the app origin first (stays local
    // only when it really points at the app host).
    expect(decideLinkAction('//evil.example/x', APP_ORIGIN).kind).toBe('open-external')
  })

  it('blocks dangerous or exotic schemes outright', () => {
    for (const href of [
      'javascript:alert(1)',
      'data:text/html,<script>alert(1)</script>',
      'vbscript:msgbox(1)',
      'file:///etc/passwd',
      'wails:runtime',
      'blob:http://evil/x',
    ]) {
      expect(decideLinkAction(href, APP_ORIGIN), href).toEqual({ kind: 'block' })
    }
  })
})

// Minimal DOM doubles — the guard only needs closest/getAttribute plus an
// event-target-shaped host, so the tests run in the default node environment.
function makeAnchor(href: string | null) {
  return {
    getAttribute: (name: string) => (name === 'href' ? href : null),
  }
}
function makeTarget() {
  const listeners = new Map<string, (ev: any) => void>()
  return {
    addEventListener: (type: string, fn: (ev: any) => void) => listeners.set(type, fn),
    removeEventListener: (type: string, fn: (ev: any) => void) => {
      if (listeners.get(type) === fn) listeners.delete(type)
    },
    fire: (type: string, ev: any) => listeners.get(type)?.(ev),
    size: () => listeners.size,
  }
}
function makeEvent(target: unknown) {
  return {
    target,
    button: 0,
    preventDefault: vi.fn(),
    stopPropagation: vi.fn(),
  }
}

describe('installExternalLinkGuard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('intercepts external anchor clicks: prevented + opened in the system browser', () => {
    const target = makeTarget()
    const originalOpen = vi.fn(() => 'popup')
    const win = { location: { origin: APP_ORIGIN }, open: originalOpen as any }
    const cleanup = installExternalLinkGuard(target as any, win as any)

    const ev = makeEvent({ closest: () => makeAnchor('https://evil.example/pwn') })
    target.fire('click', ev)

    expect(ev.preventDefault).toHaveBeenCalled()
    expect(ev.stopPropagation).toHaveBeenCalled()
    expect(Browser.OpenURL).toHaveBeenCalledWith('https://evil.example/pwn')
    expect(originalOpen).not.toHaveBeenCalled()

    cleanup()
  })

  it('lets same-origin anchor clicks through untouched', () => {
    const target = makeTarget()
    const win = { location: { origin: APP_ORIGIN }, open: vi.fn() }
    const cleanup = installExternalLinkGuard(target as any, win as any)

    const ev = makeEvent({ closest: () => makeAnchor('/settings') })
    target.fire('click', ev)

    expect(ev.preventDefault).not.toHaveBeenCalled()
    expect(Browser.OpenURL).not.toHaveBeenCalled()

    cleanup()
  })

  it('blocks javascript: hrefs without opening anything', () => {
    const target = makeTarget()
    const win = { location: { origin: APP_ORIGIN }, open: vi.fn() }
    const cleanup = installExternalLinkGuard(target as any, win as any)

    const ev = makeEvent({ closest: () => makeAnchor('javascript:alert(1)') })
    target.fire('click', ev)

    expect(ev.preventDefault).toHaveBeenCalled()
    expect(Browser.OpenURL).not.toHaveBeenCalled()

    cleanup()
  })

  it('ignores clicks that are not anchor navigations', () => {
    const target = makeTarget()
    const win = { location: { origin: APP_ORIGIN }, open: vi.fn() }
    const cleanup = installExternalLinkGuard(target as any, win as any)

    const ev = makeEvent({ closest: () => null })
    target.fire('click', ev)

    expect(ev.preventDefault).not.toHaveBeenCalled()

    cleanup()
  })

  it('overrides window.open: external URLs go to the OS, nothing opens in-app', () => {
    const target = makeTarget()
    const originalOpen = vi.fn(() => 'popup')
    const win = { location: { origin: APP_ORIGIN }, open: originalOpen as any }
    const cleanup = installExternalLinkGuard(target as any, win as any)

    expect(win.open('https://evil.example/')).toBeNull()
    expect(Browser.OpenURL).toHaveBeenCalledWith('https://evil.example/')
    expect(originalOpen).not.toHaveBeenCalled() // original never reached

    expect(win.open('javascript:alert(1)')).toBeNull()
    expect(Browser.OpenURL).toHaveBeenCalledTimes(1) // still just the https one

    cleanup()
    // Restored: the original opener is back.
    expect(win.open).toBe(originalOpen)
  })

  it('cleans up its listeners', () => {
    const target = makeTarget()
    const win = { location: { origin: APP_ORIGIN }, open: vi.fn() }
    const cleanup = installExternalLinkGuard(target as any, win as any)
    expect(target.size()).toBe(2) // click + auxclick
    cleanup()
    expect(target.size()).toBe(0)
  })
})

describe('openExternalURL', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('never throws when the OS handler fails', () => {
    vi.mocked(Browser.OpenURL).mockImplementation(() => {
      throw new Error('no handler')
    })
    expect(() => openExternalURL('https://x.example')).not.toThrow()
  })
})
