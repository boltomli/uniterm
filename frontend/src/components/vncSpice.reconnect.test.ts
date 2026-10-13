// Regression tests for security.md component chapter finding 14:
//   Restored VNC and SPICE tabs lose their event wiring. The cache-restore
//   branch in onMounted returned early, skipping `session:status`,
//   `panel:reconnect` (and SPICE's canvas MutationObserver), so after a
//   zero-delay tab switch disconnects showed no error overlay and the tab
//   right-click 重连 menu did nothing. VNC's cached rfb also kept the
//   previous component instance's handlers, which wrote to that dead
//   instance's refs.
// The repo has no component-mount tests (node env, no jsdom), so the wiring
// is asserted on the SFC source (same pattern as BaseTerminal.cleanup.test.ts).

import { describe, it, expect } from 'vitest'
import { hookBody, scriptBody, readSrc } from '../testUtils/sfc'

/** Index of the cache-restore branch inside the onMounted body — the early
 *  return that used to skip every registration. */
function cacheBranchIndex(mounted: string, getter: string): number {
  const idx = mounted.indexOf(`const cached = panelStore.${getter}(`)
  expect(idx, 'onMounted must contain the cache-restore branch').toBeGreaterThanOrEqual(0)
  return idx
}

describe('VNCTabContent.vue restore wiring (security.md #14)', () => {
  const src = readSrc('components/VNCTabContent.vue')
  const script = scriptBody(src)
  const mounted = hookBody(src, 'onMounted')
  const unmount = hookBody(src, 'onBeforeUnmount')

  it('registers session:status before the cache-restore early return', () => {
    const statusIdx = mounted.indexOf("Events.On('session:status'")
    expect(statusIdx, 'onMounted must register session:status').toBeGreaterThanOrEqual(0)
    expect(statusIdx).toBeLessThan(cacheBranchIndex(mounted, 'getVNCCache'))
  })

  it('registers panel:reconnect before the cache-restore early return', () => {
    const reconnectIdx = mounted.indexOf("window.addEventListener('panel:reconnect'")
    expect(reconnectIdx, 'onMounted must register panel:reconnect').toBeGreaterThanOrEqual(0)
    expect(reconnectIdx).toBeLessThan(cacheBranchIndex(mounted, 'getVNCCache'))
  })

  it('registers each window/session listener exactly once per mount', () => {
    expect((mounted.match(/Events\.On\('session:status'/g) ?? []).length).toBe(1)
    expect((mounted.match(/window\.addEventListener\('panel:reconnect'/g) ?? []).length).toBe(1)
  })

  it('rebinds the rfb handlers inside the cache-restore branch', () => {
    const cacheIdx = cacheBranchIndex(mounted, 'getVNCCache')
    const earlyReturn = mounted.indexOf('return', cacheIdx)
    expect(earlyReturn).toBeGreaterThan(cacheIdx)
    const branch = mounted.slice(cacheIdx, earlyReturn)
    expect(branch).toMatch(/bindRFBHandlers\(rfb\)/)
  })

  it('drops stale rfb handlers before attaching this instance’s', () => {
    const bindIdx = script.indexOf('function bindRFBHandlers(')
    expect(bindIdx, 'a shared RFB handler binder must exist').toBeGreaterThanOrEqual(0)
    const next = script.indexOf('\nfunction ', bindIdx + 1)
    const body = script.slice(bindIdx, next < 0 ? script.length : next)
    const removeIdx = body.indexOf('removeEventListener(')
    const addIdx = body.indexOf('addEventListener(')
    expect(removeIdx, 'stale handlers must be removed').toBeGreaterThan(-1)
    expect(addIdx, 'handlers must be attached').toBeGreaterThan(-1)
    expect(removeIdx, 'stale handlers removed before attaching').toBeLessThan(addIdx)
    expect(body, 'bound pairs recorded for the next instance to remove').toMatch(/__unitermHandlers/)
  })

  it('routes createRFB through the shared binder (no inline handlers left)', () => {
    const createIdx = script.indexOf('function createRFB(')
    expect(createIdx).toBeGreaterThanOrEqual(0)
    const next = script.indexOf('\nfunction ', createIdx + 1)
    const body = script.slice(createIdx, next < 0 ? script.length : next)
    expect(body).toMatch(/bindRFBHandlers\(rfb\)/)
    expect(script, 'no inline rfb.addEventListener may remain (stacking)').not.toMatch(/rfb\.addEventListener\(/)
  })

  it('unmount still removes the listeners it registers', () => {
    expect(unmount).toMatch(/unsubStatus\?\.\(\)/)
    expect(unmount).toMatch(/window\.removeEventListener\('panel:reconnect', onReconnectEvent\)/)
  })
})

describe('SPICETabContent.vue restore wiring (security.md #14)', () => {
  const src = readSrc('components/SPICETabContent.vue')
  const mounted = hookBody(src, 'onMounted')
  const unmount = hookBody(src, 'onBeforeUnmount')

  it('registers session:status before the cache-restore early return', () => {
    const statusIdx = mounted.indexOf("Events.On('session:status'")
    expect(statusIdx, 'onMounted must register session:status').toBeGreaterThanOrEqual(0)
    expect(statusIdx).toBeLessThan(cacheBranchIndex(mounted, 'getSPICECache'))
  })

  it('registers panel:reconnect before the cache-restore early return', () => {
    const reconnectIdx = mounted.indexOf("window.addEventListener('panel:reconnect'")
    expect(reconnectIdx, 'onMounted must register panel:reconnect').toBeGreaterThanOrEqual(0)
    expect(reconnectIdx).toBeLessThan(cacheBranchIndex(mounted, 'getSPICECache'))
  })

  it('registers the canvas MutationObserver before the cache-restore early return', () => {
    const moIdx = mounted.indexOf('new MutationObserver(')
    expect(moIdx, 'onMounted must create the canvas observer').toBeGreaterThanOrEqual(0)
    expect(moIdx).toBeLessThan(cacheBranchIndex(mounted, 'getSPICECache'))
  })

  it('registers each listener exactly once per mount', () => {
    expect((mounted.match(/Events\.On\('session:status'/g) ?? []).length).toBe(1)
    expect((mounted.match(/window\.addEventListener\('panel:reconnect'/g) ?? []).length).toBe(1)
    expect((mounted.match(/new MutationObserver\(/g) ?? []).length).toBe(1)
  })

  it('unmount still removes the listeners and disconnects the observer', () => {
    expect(unmount).toMatch(/unsubStatus\?\.\(\)/)
    expect(unmount).toMatch(/window\.removeEventListener\('panel:reconnect', onReconnectEvent\)/)
    expect(unmount).toMatch(/mo\.disconnect\(\)/)
    expect(unmount).toMatch(/__domObserver = null/)
  })
})
