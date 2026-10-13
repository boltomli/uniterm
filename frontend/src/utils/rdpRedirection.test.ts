// Regression tests for the RDP redirection defaults (security.md P1:
// "RDP always shares every local drive and the clipboard").
//
// The pre-fix behavior hardcoded RedirectClipboard/RedirectDrives to true on
// the backend, so every RDP connection shared all local disks and the
// clipboard with the remote side. The flags are now per-connection and must
// default to OFF — including for connections saved before the field existed.
//
// The ConnectionForm guard reads the component source: the form is a heavy
// SFC (no test-utils in this repo), and the wiring that matters — switches in
// the RDP section, default-off load/reset, fields carried into the saved
// config — is exactly what that source shows.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { RDP_REDIRECTION_DEFAULT, resolveRdpRedirection } from './rdpRedirection'

describe('RDP redirection defaults', () => {
  it('defaults both flags to off for a brand-new connection', () => {
    expect(RDP_REDIRECTION_DEFAULT).toEqual({
      rdpRedirectClipboard: false,
      rdpRedirectDrives: false,
    })
  })

  it('resolves a legacy connection with no redirect fields to off', () => {
    expect(resolveRdpRedirection(undefined)).toEqual({
      rdpRedirectClipboard: false,
      rdpRedirectDrives: false,
    })
    expect(resolveRdpRedirection(null)).toEqual({
      rdpRedirectClipboard: false,
      rdpRedirectDrives: false,
    })
    expect(resolveRdpRedirection({})).toEqual({
      rdpRedirectClipboard: false,
      rdpRedirectDrives: false,
    })
  })

  it('keeps explicitly disabled flags off', () => {
    expect(
      resolveRdpRedirection({ rdpRedirectClipboard: false, rdpRedirectDrives: false })
    ).toEqual({ rdpRedirectClipboard: false, rdpRedirectDrives: false })
  })

  it('honors an explicit opt-in for each flag independently', () => {
    expect(resolveRdpRedirection({ rdpRedirectClipboard: true })).toEqual({
      rdpRedirectClipboard: true,
      rdpRedirectDrives: false,
    })
    expect(resolveRdpRedirection({ rdpRedirectDrives: true })).toEqual({
      rdpRedirectClipboard: false,
      rdpRedirectDrives: true,
    })
    expect(
      resolveRdpRedirection({ rdpRedirectClipboard: true, rdpRedirectDrives: true })
    ).toEqual({ rdpRedirectClipboard: true, rdpRedirectDrives: true })
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

  it('starts the form with both flags off and resets them to off', () => {
    expect(source).toContain('...RDP_REDIRECTION_DEFAULT')
    expect(source).toContain('Object.assign(form, RDP_REDIRECTION_DEFAULT)')
  })

  it('loads stored connections through the default-off resolver', () => {
    expect(source).toContain('resolveRdpRedirection(config)')
    expect(source).not.toContain('config.rdpRedirectClipboard ?? ')
  })

  it('never re-introduces an always-on redirect default', () => {
    expect(source).not.toMatch(/rdpRedirectClipboard:\s*true/)
    expect(source).not.toMatch(/rdpRedirectDrives:\s*true/)
  })
})
