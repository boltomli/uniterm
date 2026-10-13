// Regression tests for the K8s list component half of security.md #8 and the
// metrics-poller leak:
//   #8   — `filtered`/`crdFiltered`/`compareCells` re-sorted with a fresh
//          localeCompare per render, and `enumFilters` scanned every item for
//          every filterable column on every render.
//   leak — `applySubs` awaits store.subscribe() and then unconditionally calls
//          startMetrics(); an unmount during those awaits runs onBeforeUnmount
//          first (stopMetrics), after which the resumed applySubs restarted
//          the 15s interval on a dead component — polling forever.
// This repo has no component-mount tests (node env, no jsdom), so the wiring
// is asserted on the SFC source — same pattern as BaseTerminal.cleanup.test.ts.

import { describe, it, expect } from 'vitest'
import { scriptBody, hookBody, readSrc } from '../testUtils/sfc'

const script = scriptBody(readSrc('components/K8sResourceList.vue'))

/** Body of a `function <decl>(...) { … }` declaration, brace-balanced. */
function functionBody(src: string, decl: string): string {
  const at = src.indexOf(decl)
  if (at < 0) return ''
  const open = src.indexOf('{', at + decl.length)
  if (open < 0) return ''
  let depth = 0
  for (let i = open; i < src.length; i++) {
    const ch = src[i]
    if (ch === '{') depth += 1
    else if (ch === '}') {
      depth -= 1
      if (depth === 0) return src.slice(open + 1, i)
    }
  }
  return ''
}

describe('K8sResourceList metrics poller lifecycle (disposed guard)', () => {
  it('declares a disposed flag', () => {
    expect(script).toMatch(/let disposed = false/)
  })

  it('applySubs checks disposed before (re)starting the metrics poller', () => {
    const body = functionBody(script, 'async function applySubs(')
    const guard = body.indexOf('disposed')
    const start = body.indexOf('startMetrics()')
    expect(guard).toBeGreaterThan(-1)
    expect(start).toBeGreaterThan(-1)
    expect(guard).toBeLessThan(start)
  })

  it('onBeforeUnmount marks disposed before stopping the poller', () => {
    const body = hookBody(script, 'onBeforeUnmount')
    const mark = body.indexOf('disposed = true')
    const stop = body.indexOf('stopMetrics()')
    expect(mark).toBeGreaterThan(-1)
    expect(stop).toBeGreaterThan(mark)
  })

  it('onBeforeUnmount still unsubscribes the list subscriptions', () => {
    expect(hookBody(script, 'onBeforeUnmount')).toMatch(/unsubscribe/)
  })
})

describe('K8sResourceList sort and enum-filter caching (security.md #8)', () => {
  it('sorts with the shared Intl.Collator instead of localeCompare', () => {
    expect(readSrc('services/k8sResources.ts')).toMatch(/export const textCollator = new Intl\.Collator\(\)/)
    expect(script).toMatch(/textCollator/)
    expect(script).not.toMatch(/localeCompare/)
  })

  it('builds enum filter options once per items change, not per render', () => {
    expect(script).toMatch(/const enumFilterCache = computed/)
    const body = functionBody(script, 'function enumFilters(')
    expect(body).toMatch(/enumFilterCache\.value/)
    expect(body).not.toMatch(/for \(const row of items\.value\)/)
  })
})
