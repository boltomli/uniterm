// Support for security.md component chapter #13/#22 (BaseTerminal leaks):
// the shared xterm instance outlives every BaseTerminal component, so the
// per-component WebLinksAddon, the shared SearchAddon's onDidChangeResults
// disposable and the fit-retry timers must be released on unmount or they
// stack up on every remount (Ctrl+Click opens a URL N times, dead components
// stay referenced, SessionResize fires against a released terminal).

import { describe, it, expect, vi } from 'vitest'
import { createDisposeBag } from './disposeBag'

function fakeDisposable() {
  return { dispose: vi.fn() }
}

describe('createDisposeBag', () => {
  it('releases every collected disposable exactly once, most recent first', () => {
    const bag = createDisposeBag()
    const order: string[] = []
    bag.add(() => order.push('first'))
    const addon = fakeDisposable()
    bag.addDisposable(addon)
    bag.add(() => order.push('last'))

    expect(bag.size).toBe(3)
    bag.disposeAll()
    expect(addon.dispose).toHaveBeenCalledTimes(1)
    expect(order).toEqual(['last', 'first'])
  })

  it('returns the added disposable unchanged for inline use', () => {
    const bag = createDisposeBag()
    const addon = fakeDisposable()
    expect(bag.addDisposable(addon)).toBe(addon)
  })

  it('clears tracked timeouts at teardown', () => {
    vi.useFakeTimers()
    try {
      const bag = createDisposeBag()
      const fired = vi.fn()
      bag.addTimeout(setTimeout(fired, 50))
      bag.addTimeout(setTimeout(fired, 50))
      bag.disposeAll()
      vi.runAllTimers()
      expect(fired).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })

  it('is idempotent: a second disposeAll releases nothing again', () => {
    const bag = createDisposeBag()
    const addon = fakeDisposable()
    bag.addDisposable(addon)
    bag.disposeAll()
    bag.disposeAll()
    expect(addon.dispose).toHaveBeenCalledTimes(1)
    expect(bag.size).toBe(0)
  })

  it('keeps releasing after a failing release callback', () => {
    const bag = createDisposeBag()
    const addon = fakeDisposable()
    bag.addDisposable(addon)
    bag.add(() => { throw new Error('dispose failed') })
    expect(() => bag.disposeAll()).not.toThrow()
    expect(addon.dispose).toHaveBeenCalledTimes(1)
    expect(bag.size).toBe(0)
  })
})
