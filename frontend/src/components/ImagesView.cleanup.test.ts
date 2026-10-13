// Regression test for security.md component chapter #22 (leaks and timers):
//   - ImagesView.vue:452-454: a pull or push stream handle is lost if the view
//     unmounts while `await client.startPull/startPush` is still starting —
//     the handle lands on the task after unmount and nothing ever stops it.
//   - ImagesView.vue:484: the 1 s elapsed-time interval keeps ticking while
//     the page/tab and the KeepAlive-cached app tab are hidden.
// The repo has no component-mount tests (node env, no jsdom), so the wiring is
// asserted on the SFC source, same as BaseTerminal.cleanup.test.ts.

import { describe, it, expect } from 'vitest'
import { hookBody, scriptBody, readSrc } from '../testUtils/sfc'

const src = readSrc('components/ImagesView.vue')
const script = scriptBody(src)

describe('ImagesView.vue pull/push stream teardown', () => {
  it('routes both the pull and the push handle into storage unmount can reach', () => {
    // One variable captures both branches of the ternary, so neither the pull
    // nor the push path can produce a handle that exists only as an async local.
    expect(script).toMatch(/const handle = kind === 'pull'[\s\S]{0,160}await client\.startPull\(/)
    expect(script).toMatch(/tk\.handle = handle/)
    // The unmount hook arms the race flag, then stops every registered handle.
    expect(hookBody(src, 'onBeforeUnmount')).toMatch(/unmounted = true/)
    expect(hookBody(src, 'onBeforeUnmount')).toMatch(/t\.handle\?\.stop\(\)/)
  })

  it('aborts a handle whose async start races the unmount', () => {
    // If the view unmounts while startPull/startPush is awaiting, the resolved
    // handle would be stored on a dead task and never stopped; it must be
    // stopped on the spot instead.
    expect(script).toMatch(/if \(unmounted\) \{[\s\S]{0,80}handle\.stop\(\)/)
  })
})

describe('ImagesView.vue tick interval teardown', () => {
  it('clears the interval on unmount', () => {
    expect(hookBody(src, 'onBeforeUnmount')).toMatch(/pauseTick\(\)/)
    expect(script).toMatch(/function pauseTick\(\) \{[\s\S]{0,120}clearInterval\(tickTimer\)/)
  })

  it('pauses while the page/tab is hidden and resumes on show', () => {
    // visibilitychange pattern as in App.vue / BaseTerminal.vue: the listener
    // drives one scheduler that gates on document.hidden.
    expect(script).toMatch(/document\.addEventListener\('visibilitychange', onVisibilityChange\)/)
    expect(script).toMatch(/function onVisibilityChange\(\) \{[\s\S]{0,60}syncTick\(\)/)
    expect(script).toMatch(/function syncTick\(\) \{[\s\S]{0,120}document\.hidden/)
    expect(script).toMatch(/else startTick\(\)/)
    // KeepAlive app-tab switch = hidden, the pattern MonitorTabContent.vue and
    // BaseTerminal.vue use to idle while the tab is cached away.
    expect(hookBody(src, 'onDeactivated')).toMatch(/pauseTick\(\)/)
    expect(hookBody(src, 'onActivated')).toMatch(/syncTick\(\)/)
  })

  it('removes the visibilitychange listener on unmount', () => {
    expect(hookBody(src, 'onBeforeUnmount')).toMatch(
      /document\.removeEventListener\('visibilitychange', onVisibilityChange\)/,
    )
  })
})
