// Regression tests for security.md component chapter #2 (High): SettingsTab
// installs a capture-phase `keydown` listener while rebinding a shortcut and
// only removed it from stopRebind(). The tab lives in <KeepAlive> — switching
// tabs deactivated it without ending the rebind, so every keystroke was
// swallowed (preventDefault + stopPropagation), the app's global shortcuts
// stayed uninstalled, and the next Ctrl/Alt combo got saved as a binding.
// The listener registration now lives in a symmetric start/stop pair and both
// lifecycle exits call stopRebind().

import { describe, it, expect } from 'vitest'
import { createRebindCapture } from './rebindCapture'
import { hookBodies, scriptBody, readSrc } from '../testUtils/sfc'

interface FakeTarget {
  added: number
  removed: number
  /** Capture options each live listener was registered with, in order. */
  listenerOptions(type: string): unknown[]
  addEventListener(type: string, listener: (e: Event) => void, options?: unknown): void
  removeEventListener(type: string, listener: (e: Event) => void, options?: unknown): void
}

function makeTarget(): FakeTarget {
  const listeners = new Map<string, Map<(e: Event) => void, unknown>>()
  const target: FakeTarget = {
    added: 0,
    removed: 0,
    addEventListener(type, listener, options) {
      target.added += 1
      if (!listeners.has(type)) listeners.set(type, new Map())
      listeners.get(type)!.set(listener, options)
    },
    removeEventListener(type, listener) {
      target.removed += 1
      listeners.get(type)?.delete(listener)
    },
    listenerOptions: (type) => [...(listeners.get(type) ?? []).values()],
  }
  return target
}

function setup() {
  const doc = makeTarget()
  const win = makeTarget()
  const onKeydown = () => {}
  const onBlur = () => {}
  return { doc, win, onKeydown, onBlur, capture: createRebindCapture({ document: doc, window: win }, { onKeydown, onBlur }) }
}

describe('rebind capture listener', () => {
  it('registers exactly one capture keydown and one blur listener on start', () => {
    const { doc, win, capture } = setup()
    capture.start()
    expect(capture.isActive()).toBe(true)
    expect(doc.added).toBe(1)
    expect(win.added).toBe(1)
    // Capture phase: the listener must beat every other keydown handler.
    expect(doc.listenerOptions('keydown')[0]).toBe(true)
  })

  it('is idempotent: a second start does not stack another listener', () => {
    const { doc, win, capture } = setup()
    capture.start()
    capture.start()
    expect(doc.added).toBe(1)
    expect(win.added).toBe(1)
  })

  it('removes both listeners on stop (count returns to zero)', () => {
    const { doc, win, capture } = setup()
    capture.start()
    capture.stop()
    expect(capture.isActive()).toBe(false)
    expect(doc.added).toBe(doc.removed)
    expect(win.added).toBe(win.removed)
    expect(doc.listenerOptions('keydown')).toHaveLength(0)
    expect(win.listenerOptions('blur')).toHaveLength(0)
  })

  it('is safe to stop without a start and to stop twice', () => {
    const { doc, win, capture } = setup()
    capture.stop()
    capture.start()
    capture.stop()
    capture.stop()
    expect(doc.removed).toBe(1)
    expect(win.removed).toBe(1)
  })

  it('a tab switch mid-rebind (start → stop) leaves no listener swallowing keys', () => {
    const { doc, capture } = setup()
    capture.start()
    // What onDeactivated/onUnmounted now run:
    capture.stop()
    expect(doc.listenerOptions('keydown')).toHaveLength(0)
  })
})

describe('SettingsTab.vue wiring', () => {
  const src = readSrc('components/SettingsTab.vue')
  const script = scriptBody(src)

  it('routes the capture listener through the symmetric helper', () => {
    expect(script).toMatch(/createRebindCapture\(/)
    expect(script).toMatch(/rebindCapture\.start\(\)/)
    expect(script).toMatch(/rebindCapture\.stop\(\)/)
    expect(script).not.toMatch(/document\.addEventListener\('keydown', onRebindKeydown, true\)/)
    expect(script).not.toMatch(/let rebindListenerActive/)
  })

  it('ends any in-flight rebind when the tab deactivates or unmounts', () => {
    // The Settings tab registers more than one onUnmounted hook (the sidebar
    // ResizeObserver cleanup is a separate one), so check every registration.
    expect(hookBodies(src, 'onDeactivated').some((arg) => /stopRebind/.test(arg))).toBe(true)
    expect(hookBodies(src, 'onUnmounted').some((arg) => /stopRebind/.test(arg))).toBe(true)
  })
})
