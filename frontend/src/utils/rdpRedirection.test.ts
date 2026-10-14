// Regression tests for the RDP redirection defaults (security.md P1:
// "RDP always shares every local drive and the clipboard").
//
// The security fix made redirection per-connection and tri-state: an absent
// field — connections saved before the fields existed, or synced from an
// older version — resolves to RDP_REDIRECTION_DEFAULT, clipboard ON (pasting
// local text into the remote desktop is the long-standing behavior such a
// connection must keep) and local drives OFF (the remote side never reaches
// the disks through \\tsclient without an explicit opt-in). An explicit
// value, including an opt-out, is respected verbatim. The backend mirrors
// these defaults in buildRdpSettings (backend/session/rdp_session.go).
//
// The ConnectionForm guard reads the component source: the form is a heavy
// SFC (no test-utils in this repo), and the wiring that matters — switches in
// the RDP section, default load/reset, fields carried into the saved config —
// is exactly what that source shows.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { RDP_REDIRECTION_DEFAULT, resolveRdpRedirection } from './rdpRedirection'

describe('RDP redirection defaults', () => {
  it('defaults a brand-new connection to clipboard on, drives off', () => {
    expect(RDP_REDIRECTION_DEFAULT).toEqual({
      rdpRedirectClipboard: true,
      rdpRedirectDrives: false,
    })
  })

  it('resolves a legacy connection with no redirect fields to the defaults', () => {
    expect(resolveRdpRedirection(undefined)).toEqual({
      rdpRedirectClipboard: true,
      rdpRedirectDrives: false,
    })
    expect(resolveRdpRedirection(null)).toEqual({
      rdpRedirectClipboard: true,
      rdpRedirectDrives: false,
    })
    expect(resolveRdpRedirection({})).toEqual({
      rdpRedirectClipboard: true,
      rdpRedirectDrives: false,
    })
  })

  it('keeps explicitly disabled flags off', () => {
    expect(
      resolveRdpRedirection({ rdpRedirectClipboard: false, rdpRedirectDrives: false })
    ).toEqual({ rdpRedirectClipboard: false, rdpRedirectDrives: false })
    expect(resolveRdpRedirection({ rdpRedirectClipboard: false })).toEqual({
      rdpRedirectClipboard: false,
      rdpRedirectDrives: false,
    })
  })

  it('honors explicit opt-ins independently of the other flag\'s default', () => {
    expect(resolveRdpRedirection({ rdpRedirectClipboard: true })).toEqual({
      rdpRedirectClipboard: true,
      rdpRedirectDrives: false,
    })
    // Drives opted in while clipboard is unspecified → clipboard keeps its on
    // default rather than being dragged along either way.
    expect(resolveRdpRedirection({ rdpRedirectDrives: true })).toEqual({
      rdpRedirectClipboard: true,
      rdpRedirectDrives: true,
    })
    expect(
      resolveRdpRedirection({ rdpRedirectClipboard: false, rdpRedirectDrives: true })
    ).toEqual({ rdpRedirectClipboard: false, rdpRedirectDrives: true })
  })
})

describe('ConnectionForm RDP redirection wiring', () => {
  const source = readFileSync(
    fileURLToPath(new URL('../components/ConnectionForm.vue', import.meta.url)),
    'utf-8'
  )

  it('exposes a switch for each redirection flag in the RDP section', () => {
    expect(source).toContain('v-model="form.rdpRedirectClipboard"')
    expect(source).toContain('v-model="form.rdpRedirectDrives"')
  })

  it('starts the form from the shared defaults and resets to them', () => {
    expect(source).toContain('...RDP_REDIRECTION_DEFAULT')
    expect(source).toContain('Object.assign(form, RDP_REDIRECTION_DEFAULT)')
  })

  it('loads stored connections through the shared-default resolver', () => {
    expect(source).toContain('resolveRdpRedirection(config)')
    expect(source).not.toContain('config.rdpRedirectClipboard ?? ')
  })

  it('never hardcodes a redirect default, always resolving through the module', () => {
    expect(source).not.toMatch(/rdpRedirectClipboard:\s*(true|false)/)
    expect(source).not.toMatch(/rdpRedirectDrives:\s*(true|false)/)
  })
})
