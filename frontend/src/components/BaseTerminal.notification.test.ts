import { describe, it, expect } from 'vitest'
import { hookBody, hookBodies, scriptBody, readSrc } from '../testUtils/sfc'

// P-10: a background session flooding output used to walk panelStore.panels
// and tabStore.tabs on EVERY session:data chunk just to (re)mark the tab's
// notification dot. Now the first dot arms a local flag; later chunks skip
// the lookup until the tab returns to the foreground (or the session id
// changes).
//
// Source-level wiring test — the repo has no component-mount infrastructure
// (node env, no jsdom), same pattern as BaseTerminal.cleanup.test.ts.

const src = readSrc('components/BaseTerminal.vue')
const script = scriptBody(src)

describe('BaseTerminal.vue background notification gating (P-10)', () => {
  it('arms the inactive-notify flag when the dot lands', () => {
    expect(script).toMatch(/tabStore\.markTabNotification\(tab\.id\)[\s\S]{0,200}?notifiedWhileInactive = true/)
  })

  it('skips the panel/tab lookup once the dot has landed', () => {
    // The whole lookup (panel loop + tab find) sits inside the gated if.
    expect(script).toMatch(/!notifiedWhileInactive\) \{/)
    expect(script).toMatch(/for \(const \[id, p\] of panelStore\.panels\) \{[\s\S]*markTabNotification/)
  })

  it('resets on reactivation so the next background period dots again', () => {
    expect(hookBody(src, 'onActivated')).toMatch(/notifiedWhileInactive = false/)
  })

  it('resets when the session id changes', () => {
    const watcher = hookBodies(src, 'watch').find(b => b.includes('props.sessionId'))
    expect(watcher).toBeDefined()
    expect(watcher).toMatch(/notifiedWhileInactive = false/)
  })
})
