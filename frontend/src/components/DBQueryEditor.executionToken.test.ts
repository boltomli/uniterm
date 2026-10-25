// Regression tests for DB-16 (database regression audit 2026-10-25):
// "onExecute 没有请求令牌，取消是假的". A component-level `let cancelled`
// was reset by the SECOND click, so the first (slow) query's response came
// back with cancelled === false and overwrote the newer result; the cancel
// button only flipped the flag while the statement kept running server-side.
// Now every run owns a monotonic token (runSeq) and the in-flight binding
// call is cancelled for real — the App methods take the request context.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const src = readSrc('components/DBQueryEditor.vue')
const script = scriptBody(src)

function bodyOf(src: string, startMarker: string, endMarker: string): string {
  const start = src.indexOf(startMarker)
  expect(start).toBeGreaterThan(-1)
  const end = src.indexOf(endMarker, start + startMarker.length)
  expect(end).toBeGreaterThan(start)
  return src.slice(start, end)
}

describe('DBQueryEditor execution token (DB-16)', () => {
  it('has no component-level cancelled flag left', () => {
    expect(script).not.toMatch(/let cancelled/)
    expect(script).not.toMatch(/cancelled = (true|false)/)
  })

  it('each run captures a fresh token and stale results are dropped', () => {
    const exec = bodyOf(script, 'async function onExecute', 'function isCancelError')
    expect(exec).toMatch(/const myRun = \+\+runSeq/)
    // Every branch guards its state writes with the captured token.
    const guards = exec.match(/if \(myRun !== runSeq\) return/g) ?? []
    expect(guards.length).toBeGreaterThanOrEqual(3)
    // The finally block only settles state for the run that still owns it.
    expect(exec).toMatch(/if \(myRun === runSeq\) \{\s*loading\.value = false/)
  })

  it('cancelling bumps the token and aborts the backend call', () => {
    const cancel = bodyOf(script, 'function onCancelQuery', 'const scriptResult')
    expect(cancel).toMatch(/runSeq\+\+/)
    expect(cancel).toMatch(/activeCall\?\.cancel\(\)/)
    // The token bump must come first: it makes the in-flight run a no-op.
    expect(cancel.indexOf('runSeq++')).toBeLessThan(cancel.indexOf('activeCall?.cancel()'))
  })

  it('treats a CancelError rejection as user-requested, not an error', () => {
    const exec = bodyOf(script, 'async function onExecute', 'function onCancelQuery')
    expect(exec).toMatch(/isCancelError\(e\)/)
  })

  it('registers the in-flight call for every execution path', () => {
    const exec = bodyOf(script, 'async function onExecute', 'function onCancelQuery')
    const calls = exec.match(/activeCall = call/g) ?? []
    expect(calls.length).toBeGreaterThanOrEqual(3)
    const fileRun = bodyOf(script, 'async function onOpenScriptFile', 'function decodeBase64')
    expect(fileRun).toMatch(/const myRun = \+\+runSeq/)
    expect(fileRun).toMatch(/activeCall = call/)
  })
})
