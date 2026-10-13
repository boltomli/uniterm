// Regression test for security.md #10: the K8s tab's fatal `error` was set by
// connect() but never cleared, so after a successful 「重连」 the error panel
// (v-if="error") stayed up even though reconnect()'s comment claims connect()
// resets it (the Container copy resets through containerStore.open).
// This repo has no component-mount tests (node env, no jsdom), so the
// connect() body is asserted on the SFC source — same pattern as
// BaseTerminal.cleanup.test.ts.

import { describe, it, expect } from 'vitest'
import { scriptBody, readSrc } from '../testUtils/sfc'

const script = scriptBody(readSrc('components/K8sTabContent.vue'))

/** Body of `async function <name>(...) { … }`, brace-balanced. */
function functionBody(src: string, name: string): string {
  const decl = src.indexOf(`function ${name}(`)
  if (decl < 0) return ''
  const open = src.indexOf('{', decl)
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

describe('K8sTabContent reconnect clears a fatal error (security.md #10)', () => {
  const body = functionBody(script, 'connect')

  it('found the connect() body', () => {
    expect(body.length).toBeGreaterThan(0)
  })

  it("clears the stale error as connect()'s first statement", () => {
    expect(body.trimStart().startsWith("error.value = ''")).toBe(true)
  })

  it('clears the error before re-entering the connecting status', () => {
    const clear = body.indexOf("error.value = ''")
    const setStatus = body.indexOf("setConnStatus(props.connection.id, 'connecting')")
    expect(clear).toBeGreaterThan(-1)
    expect(setStatus).toBeGreaterThan(clear)
  })

  it('still records failures in the catch branch', () => {
    expect(body).toMatch(/error\.value = String\(e\?\.message \|\| e\)/)
  })
})
