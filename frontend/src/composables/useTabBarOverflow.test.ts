// Regression tests for security.md finding 11 (batch-2, Medium):
//   The DB / Mongo tab-overflow "more" button never reacted to resizing. The
//   ResizeObserver was constructed in onMounted, but the observed scroll
//   element sits behind v-if and only exists once a tab is open — observe()
//   never ran when a component mounted without tabs. The observer lifecycle
//   now lives in useTabBarOverflow, which watches the template ref itself.
// The repo has no component-mount tests (node env, no jsdom), so the
// component wiring is asserted on the SFC source (pattern:
// components/BaseTerminal.cleanup.test.ts); the composable's observer
// lifecycle is covered behaviourally below with a fake ResizeObserver.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { hookBodies, scriptBody, readSrc } from '../testUtils/sfc'
import { useTabBarOverflow, type TabBarOverflow } from './useTabBarOverflow'

const dbSrc = readSrc('components/DBTabContent.vue')
const dbScript = scriptBody(dbSrc)
const mongoSrc = readSrc('components/MongoDBTabContent.vue')
const mongoScript = scriptBody(mongoSrc)

describe('DBTabContent.vue tab overflow wiring (#11)', () => {
  it('routes the overflow measurement through useTabBarOverflow', () => {
    expect(dbScript).toMatch(/useTabBarOverflow\(docTabsScrollRef\)/)
  })

  it('no longer constructs or observes a ResizeObserver itself', () => {
    expect(dbScript).not.toMatch(/ResizeObserver/)
    expect(hookBodies(dbSrc, 'onMounted').join('\n')).not.toMatch(/ResizeObserver/)
    expect(hookBodies(dbSrc, 'onBeforeUnmount').join('\n')).not.toMatch(/ResizeObserver/)
  })
})

describe('MongoDBTabContent.vue tab overflow wiring (#11)', () => {
  it('routes the overflow measurement through useTabBarOverflow', () => {
    expect(mongoScript).toMatch(/useTabBarOverflow\(tabScrollRef\)/)
  })

  it('no longer constructs or observes a ResizeObserver itself', () => {
    expect(mongoScript).not.toMatch(/ResizeObserver/)
    expect(hookBodies(mongoSrc, 'onMounted').join('\n')).not.toMatch(/ResizeObserver/)
    expect(hookBodies(mongoSrc, 'onUnmounted').join('\n')).not.toMatch(/ResizeObserver/)
  })
})

// ── Behavioural coverage of the composable itself ──

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = []
  readonly observed: Element[] = []
  readonly unobserved: Element[] = []
  disconnected = false
  constructor(readonly callback: ResizeObserverCallback) {
    FakeResizeObserver.instances.push(this)
  }

  observe(el: Element): void {
    this.observed.push(el)
  }

  unobserve(el: Element): void {
    this.unobserved.push(el)
  }

  disconnect(): void {
    this.disconnected = true
  }
}

/** Mutable stand-in for the scroll element (node env: no DOM). */
interface StubEl {
  scrollWidth: number
  clientWidth: number
}

function makeEl(scrollWidth: number, clientWidth: number): StubEl {
  return { scrollWidth, clientWidth }
}

function bind(scrollRef: { value: HTMLElement | null }, el: StubEl): void {
  scrollRef.value = el as unknown as HTMLElement
}

describe('useTabBarOverflow', () => {
  let scope: EffectScope

  beforeEach(() => {
    FakeResizeObserver.instances = []
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)
    scope = effectScope()
  })

  afterEach(() => {
    scope.stop()
    vi.unstubAllGlobals()
  })

  it('never observes anything while the scroll ref stays null', async () => {
    const scrollRef = ref<HTMLElement | null>(null)
    scope.run(() => useTabBarOverflow(scrollRef))
    await nextTick()
    expect(FakeResizeObserver.instances.flatMap(o => o.observed)).toHaveLength(0)
  })

  it('observes the element exactly once when the ref is bound', async () => {
    const scrollRef = ref<HTMLElement | null>(null)
    scope.run(() => useTabBarOverflow(scrollRef))
    const el = makeEl(100, 100)
    bind(scrollRef, el)
    await nextTick()
    expect(FakeResizeObserver.instances).toHaveLength(1)
    expect(FakeResizeObserver.instances[0].observed).toHaveLength(1)
    expect(FakeResizeObserver.instances[0].observed[0]).toBe(scrollRef.value)
  })

  it('unobserves the element when the ref goes back to null', async () => {
    const scrollRef = ref<HTMLElement | null>(null)
    scope.run(() => useTabBarOverflow(scrollRef))
    const el = makeEl(100, 100)
    bind(scrollRef, el)
    await nextTick()
    // ref() wraps plain object values in a reactive proxy; assert against the
    // value that was actually handed to observe().
    const bound = scrollRef.value!
    scrollRef.value = null
    await nextTick()
    const observer = FakeResizeObserver.instances[0]
    expect(observer.unobserved).toHaveLength(1)
    expect(observer.unobserved[0]).toBe(bound)
    expect(observer.disconnected).toBe(false)
  })

  it('disconnects on scope stop and ignores later ref changes', async () => {
    const scrollRef = ref<HTMLElement | null>(null)
    scope.run(() => useTabBarOverflow(scrollRef))
    bind(scrollRef, makeEl(100, 100))
    await nextTick()
    scope.stop()
    expect(FakeResizeObserver.instances[0].disconnected).toBe(true)
    bind(scrollRef, makeEl(300, 100))
    await nextTick()
    expect(FakeResizeObserver.instances[0].observed).toHaveLength(1)
    expect(FakeResizeObserver.instances).toHaveLength(1)
  })

  it('flips the exposed overflow state from the observer callback', async () => {
    const scrollRef = ref<HTMLElement | null>(null)
    const api: TabBarOverflow = scope.run(() => useTabBarOverflow(scrollRef))!
    const el = makeEl(100, 100)
    bind(scrollRef, el)
    await nextTick()
    expect(api.showMore.value).toBe(false)
    el.scrollWidth = 300
    el.clientWidth = 100
    const observer = FakeResizeObserver.instances[0]
    observer.callback([], observer as unknown as ResizeObserver)
    expect(api.showMore.value).toBe(true)
    // Non-overflowing strip flips back.
    el.scrollWidth = 100
    observer.callback([], observer as unknown as ResizeObserver)
    expect(api.showMore.value).toBe(false)
  })
})
