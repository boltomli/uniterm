// Regression tests for security.md component chapter #13 and #22:
//   #13 — BaseTerminal loads a per-component WebLinksAddon onto the SHARED
//         xterm instance and subscribes to the shared SearchAddon's
//         onDidChangeResults; neither disposable was ever released, so every
//         remount (split, drag, merge) stacked another one (Ctrl+Click opens
//         a URL N times; dead components stay referenced).
//   #22 — the resizeTimer (window resize / native resize end) and the
//         [50..2000]ms fit-retry timers could fire SessionResize / fit()
//         against a terminal already released by unmount.
// The repo has no component-mount tests (node env, no jsdom), so the wiring
// is asserted on the SFC source; the teardown helper itself is covered in
// utils/disposeBag.test.ts.

import { describe, it, expect } from 'vitest'
import { hookBody, scriptBody, readSrc } from '../testUtils/sfc'

const src = readSrc('components/BaseTerminal.vue')
const script = scriptBody(src)

describe('BaseTerminal.vue addon teardown (#13)', () => {
  it('tracks the per-component WebLinksAddon in the teardown bag', () => {
    expect(script).toMatch(/const teardown = createDisposeBag\(\)/)
    expect(script).toMatch(/terminal\.loadAddon\(webLinksAddon\)/)
    expect(script).toMatch(/teardown\.addDisposable\(webLinksAddon\)/)
  })

  it('tracks the shared search addon listener', () => {
    expect(script).toMatch(/teardown\.addDisposable\(\s*managed\.searchAddon\.onDidChangeResults\(/)
  })

  it('releases the bag in onUnmounted', () => {
    expect(hookBody(src, 'onUnmounted')).toMatch(/teardown\.disposeAll\(\)/)
  })
})

describe('BaseTerminal.vue timer teardown (#22)', () => {
  it('clears the resizeTimer on unmount', () => {
    expect(hookBody(src, 'onUnmounted')).toMatch(/clearTimeout\(resizeTimer\)/)
  })

  it('tracks the initial fit-retry timers so unmount cancels them', () => {
    expect(script).toMatch(/\[50, 150, 300, 600, 1000, 2000\]\.forEach\(d => \{/)
    expect(script).toMatch(/const timer = setTimeout\(/)
    expect(script).toMatch(/teardown\.addTimeout\(timer\)/)
  })
})
