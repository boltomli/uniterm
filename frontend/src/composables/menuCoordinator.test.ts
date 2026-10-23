// Regression test for security.md component chapter #12: Menu.vue added a
// `window` global:close-context-menus listener on every mount (plus document
// click/keydown listeners on first open) and never removed any of them, so
// every closed tab with menus left listeners holding the dead instance's
// state. The comment claiming those variables were module-scoped was wrong:
// under <script setup> they are per instance. The coordinator now lives in a
// real module singleton, ref-counted by the number of mounted menus.

import { describe, it, expect, vi } from 'vitest'
import { createMenuCoordinator, type ActiveMenu, type ListenerTarget } from './menuCoordinator'
import { scriptBody, readSrc } from '../testUtils/sfc'

// activate() releases the remote-desktop keyboard grab, which reads the real
// document.activeElement — stub it for the node test environment.
vi.stubGlobal('document', { activeElement: null })

interface FakeTarget extends ListenerTarget {
  added: number
  removed: number
  events: string[]
  listeners(type: string): number
  fire(type: string, event?: object): void
}

function makeTarget(): FakeTarget {
  const listeners = new Map<string, Set<(e: Event) => void>>()
  const target: FakeTarget = {
    added: 0,
    removed: 0,
    events: [],
    addEventListener(type, listener) {
      target.added += 1
      if (!listeners.has(type)) listeners.set(type, new Set())
      listeners.get(type)!.add(listener)
    },
    removeEventListener(type, listener) {
      target.removed += 1
      listeners.get(type)?.delete(listener)
    },
    dispatchEvent(event) {
      target.events.push(event.type)
      for (const listener of [...(listeners.get(event.type) ?? [])]) listener(event)
      return true
    },
    listeners: (type) => listeners.get(type)?.size ?? 0,
    fire: (type, event) => { target.dispatchEvent({ type, ...event } as Event) },
  }
  return target
}

function menu(overrides: Partial<ActiveMenu> = {}): ActiveMenu {
  return { el: () => null, close: () => {}, ...overrides }
}

function setup() {
  const win = makeTarget()
  const doc = makeTarget()
  return { win, doc, coordinator: createMenuCoordinator(win, doc) }
}

describe('menu coordinator listener lifecycle', () => {
  it('binds the shared listeners once no matter how many menus mount', () => {
    const { win, doc, coordinator } = setup()
    for (let i = 0; i < 5; i += 1) coordinator.retain(menu())

    expect(win.listeners('global:close-context-menus')).toBe(1)
    expect(doc.listeners('click')).toBe(1)
    expect(doc.listeners('keydown')).toBe(1)
    expect(win.added + doc.added).toBe(3)
  })

  it('removes every listener once the last menu unmounts (count returns to zero)', () => {
    const { win, doc, coordinator } = setup()
    const menus = [menu(), menu(), menu()]
    for (const m of menus) coordinator.retain(m)
    for (const m of menus) coordinator.release(m)

    expect(win.listeners('global:close-context-menus')).toBe(0)
    expect(doc.listeners('click')).toBe(0)
    expect(doc.listeners('keydown')).toBe(0)
    expect(win.added).toBe(win.removed)
    expect(doc.added).toBe(doc.removed)
    expect(coordinator.listenersBound()).toBe(false)
  })

  it('closing any surface that broadcasts global:close-context-menus closes the active menu', () => {
    const { win, doc, coordinator } = setup()
    const closed: string[] = []
    const first = menu({ close: () => closed.push('first') })
    coordinator.retain(first)
    coordinator.activate(first)

    win.fire('global:close-context-menus')
    expect(closed).toEqual(['first'])
    expect(doc.listeners('click')).toBe(1)
  })

  it('closes on outside click but not on clicks inside the menu', () => {
    const { doc, coordinator } = setup()
    const closed: string[] = []
    const inside = { closest: () => null } as unknown as HTMLElement
    const outside = { closest: () => null } as unknown as HTMLElement
    const menuEl = { contains: (t: unknown) => t === inside } as unknown as HTMLElement
    const m = menu({ el: () => menuEl, close: () => closed.push('x') })
    coordinator.retain(m)
    coordinator.activate(m)

    doc.fire('click', { target: inside })
    expect(closed).toEqual([])
    doc.fire('click', { target: outside })
    expect(closed).toEqual(['x'])
  })

  it('closes the active menu on Escape', () => {
    const { doc, coordinator } = setup()
    const closed: string[] = []
    const m = menu({ close: () => closed.push('esc') })
    coordinator.retain(m)
    coordinator.activate(m)

    doc.fire('keydown', { key: 'Escape' })
    expect(closed).toEqual(['esc'])
  })

  it('keeps single-open semantics', () => {
    const { win, coordinator } = setup()
    const closed: string[] = []
    // In the app, close() emits update:visible=false and the visible-prop
    // watcher calls deactivate — model that pairing here.
    const tracked = (name: string): ActiveMenu => {
      const m: ActiveMenu = { el: () => null, close: () => { closed.push(name); coordinator.deactivate(m) } }
      return m
    }
    const first = tracked('first')
    const second = tracked('second')
    coordinator.retain(first)
    coordinator.retain(second)

    coordinator.activate(first)
    // Re-activating the SAME menu (open + visible watcher) must not re-broadcast.
    const broadcasts = win.events.filter((e) => e === 'global:close-context-menus').length
    coordinator.activate(first)
    expect(win.events.filter((e) => e === 'global:close-context-menus')).toHaveLength(broadcasts)

    coordinator.activate(second)
    expect(closed).toEqual(['first'])
    coordinator.deactivate(second)
    expect(coordinator.isActive(second)).toBe(false)
  })

  it('releasing the menu while it is open drops it', () => {
    const { coordinator } = setup()
    const m = menu()
    coordinator.retain(m)
    coordinator.activate(m)

    // The component is gone: the coordinator only has to stop referencing it
    // (no dangling `active`).
    coordinator.release(m)
    expect(coordinator.isActive(m)).toBe(false)
  })

  it('ignores a stray release without a matching retain', () => {
    const { win, doc, coordinator } = setup()
    coordinator.release(menu())
    expect(coordinator.listenersBound()).toBe(false)
    expect(win.added + doc.added).toBe(0)
  })
})

describe('Menu.vue wiring', () => {
  const script = scriptBody(readSrc('components/Menu.vue'))

  it('retains the coordinator per instance and releases it on unmount', () => {
    expect(script).toMatch(/menuCoordinator\.retain\(menuController\)/)
    expect(script).toMatch(/onBeforeUnmount\(\(\) => menuCoordinator\.release\(menuController\)\)/)
  })

  it('no longer owns per-instance coordinator state or listeners', () => {
    // The old copy declared `active`/`docBound`/`menuOpenCount` in
    // <script setup> (per instance) and bound the window listener at setup.
    expect(script).not.toMatch(/window\.addEventListener\('global:close-context-menus'/)
    expect(script).not.toMatch(/document\.addEventListener\('click', onDocClick\)/)
    expect(script).not.toMatch(/let active: ActiveMenu/)
  })
})
