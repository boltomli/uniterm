// Regression test: RDP connects made from a SAVED connection (仅保存, then
// connect from the list) failed authentication with 登录错误.
//
// LoadConnections redacts every secret, so the panel config handed to
// RDPTabContent carries password: '' and only the hasPassword flag.
// IronRDP's CredSSP handshake runs in the webview and needs the PLAINTEXT,
// so reading `props.config.password` directly sent an empty password to the
// server (NLA → logon failure). VNC/SPICE already resolved the stored secret
// per connection via ResolveConnectionSecret; RDP did not — 保存并连接 worked
// only because that path keeps the typed password inline in the config.
//
// The repo has no component-mount tests (node env, no jsdom), so the wiring
// is asserted on the SFC source (same pattern as vncSpice.reconnect.test.ts).

import { describe, it, expect } from 'vitest'
import { scriptBody, readSrc } from '../testUtils/sfc'

/** Body of the first top-level `function name(...)` in the script source. */
function functionBody(script: string, name: string): string {
  const start = script.indexOf(`function ${name}(`)
  expect(start, `function ${name} must exist`).toBeGreaterThanOrEqual(0)
  const open = script.indexOf('{', start)
  expect(open, `${name} must have a body`).toBeGreaterThanOrEqual(0)
  let depth = 0
  for (let i = open; i < script.length; i += 1) {
    const ch = script[i]
    if (ch === '{') depth += 1
    else if (ch === '}') {
      depth -= 1
      if (depth === 0) return script.slice(start, i + 1)
    }
  }
  throw new Error(`${name}: unbalanced braces`)
}

describe('RDPTabContent.vue resolves the stored RDP password', () => {
  const src = readSrc('components/RDPTabContent.vue')
  const script = scriptBody(src)

  it('imports the per-connection secret resolver', () => {
    expect(script).toMatch(/ResolveConnectionSecret/)
  })

  it('prefers the inline (one-shot) password, then resolves by connection id', () => {
    const body = functionBody(script, 'resolvePassword')
    const inline = body.indexOf('if (props.config.password) return props.config.password')
    expect(inline, 'inline credentials from the form/credential dialog must win').toBeGreaterThanOrEqual(0)
    const resolved = body.indexOf('ResolveConnectionSecret(props.config.id)')
    expect(resolved).toBeGreaterThan(inline)
  })

  it('does not read props.config.password directly in startClient', () => {
    const body = functionBody(script, 'startClient')
    expect(body, 'redacted config.password must never reach the WASM handshake')
      .not.toMatch(/props\.config\.password/)
    expect(body, 'the resolved plaintext feeds the credential builder').toMatch(/\.password\(password\)/)
    // autologon decides on the RESOLVED password: a redacted config used to
    // disable it (empty password) while the server still required NLA.
    expect(body).toMatch(/Extension\('autologon', !!password\)/)
  })

  it('resolves the password before the handshake, with a generation guard', () => {
    const body = functionBody(script, 'startClient')
    const resolveIdx = body.indexOf('await resolvePassword()')
    expect(resolveIdx, 'startClient must resolve the stored secret').toBeGreaterThanOrEqual(0)
    const guardIdx = body.indexOf('generation !== clientGeneration', resolveIdx)
    expect(guardIdx, 'a generation guard must follow the await').toBeGreaterThan(resolveIdx)
    const wasmIdx = body.indexOf('await loadWasm()')
    expect(wasmIdx).toBeGreaterThan(resolveIdx)
  })
})
