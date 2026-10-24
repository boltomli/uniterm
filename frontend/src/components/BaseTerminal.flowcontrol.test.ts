import { describe, it, expect } from 'vitest'
import { hookBody, hookBodies, scriptBody, readSrc } from '../testUtils/sfc'

// P-02 end-to-end flow control: writeStamped counts bytes handed to xterm
// but not yet parsed; crossing the high-water mark pauses the backend
// stream (readLoop backpressure), draining below the low-water mark resumes
// it. Every path where consumption stops — deactivate, unmount, session-id
// switch — must release the pause, and the backend bounds it with a
// watchdog regardless.
//
// The repo has no component-mount tests (node env, no jsdom), so the wiring
// is asserted on the SFC source, same as BaseTerminal.cleanup.test.ts.

const src = readSrc('components/BaseTerminal.vue')
const script = scriptBody(src)

describe('BaseTerminal.vue output flow control (P-02)', () => {
  it('counts unwritten bytes around every writeStamped write', () => {
    expect(script).toMatch(/trackOutputWrite\(data\.length\)/)
    expect(script).toMatch(/trackOutputParsed\(data\.length\)/)
  })

  it('pauses above the high-water mark and resumes below the low-water mark', () => {
    expect(script).toMatch(/OUTPUT_PENDING_HIGH = 512 \* 1024/)
    expect(script).toMatch(/OUTPUT_PENDING_LOW = 128 \* 1024/)
    expect(script).toMatch(/SessionPauseOutput\(outputPausedSid\)/)
    expect(script).toMatch(/SessionResumeOutput\(sid\)/)
  })

  it('releases the pause whenever this component stops consuming', () => {
    // KeepAlive background: the visible terminal stops writing, so its
    // pending accounting can never drain on its own.
    expect(hookBody(src, 'onDeactivated')).toMatch(/resumeBackendOutput\(\)/)
    expect(hookBody(src, 'onBeforeUnmount')).toMatch(/resumeBackendOutput\(\)/)
  })

  it('releases a pause held against a replaced session id', () => {
    const watcher = hookBodies(src, 'watch').find(b => b.includes('props.sessionId'))
    expect(watcher).toBeDefined()
    expect(watcher).toMatch(/resumeBackendOutput\(\)/)
  })

  it('uses the paused sid (not the current props) when releasing', () => {
    // A resume must target the session that was actually paused — props may
    // already point at a different id after a reconnect.
    expect(script).toMatch(/const sid = outputPausedSid/)
  })
})
