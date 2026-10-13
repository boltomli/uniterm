// Regression tests for the identity/proxy edit dialogs under secret
// redaction (security.md, "LoadIdentities/LoadProxies 向渲染层下发明文"):
//   - the password/passphrase/key-text inputs show the "saved, leave blank to
//     keep" placeholder when the redacted entity carries the matching flag;
//   - the stored key text is fetched on demand by ID (ResolveIdentitySecret)
//     when the user asks to see it — never shipped by LoadIdentities;
//   - a blank secret round-trips the flag so SaveIdentities/SaveProxies keep
//     the stored value.
// The repo has no component-mount tests (node env, no jsdom), so the wiring
// is asserted on the SFC source (same pattern as vncSpice.reconnect.test.ts).

import { describe, it, expect } from 'vitest'
import { scriptBody, readSrc } from '../testUtils/sfc'

describe('IdentityEditDialog.vue under identity redaction', () => {
  const script = scriptBody(readSrc('components/IdentityEditDialog.vue'))
  const template = readSrc('components/IdentityEditDialog.vue')

  it('imports the on-demand secret resolver', () => {
    expect(script).toContain('ResolveIdentitySecret')
  })

  it('fetches the stored key text by ID when the user reveals it', () => {
    // The reveal button must call revealKeyText, which resolves the plaintext
    // by ID only when the stored flag says a key is saved.
    expect(template).toMatch(/@click="revealKeyText"/)
    expect(script).toContain('async function revealKeyText()')
    const idx = script.indexOf('async function revealKeyText()')
    const body = script.slice(idx, idx + 600)
    expect(body).toContain('ResolveIdentitySecret(props.identity.id)')
    // Guard: no fetch when the key is already in the form or nothing stored.
    expect(body).toContain('if (form.keyContent || !props.identity?.hasKeyContent) return')
  })

  it('shows the saved-secret placeholder on the password input', () => {
    expect(template).toMatch(/v-model="passwordInput"[^>]*:placeholder="savedPasswordPlaceholder"/)
  })

  it('shows the saved-secret placeholder on both passphrase inputs', () => {
    const matches = template.match(/:placeholder="savedPasswordPlaceholder \|\| t\('conn\.keyPassphrasePlaceholder'\)"/g)
    expect(matches).toHaveLength(2)
  })

  it('derives placeholders from the redacted flags on open', () => {
    expect(script).toContain("keepPassword ? t('conn.savedSecretPlaceholder') : ''")
    expect(script).toContain("keepKeyContent ? t('conn.savedKeyContentPlaceholder') : ''")
  })

  it('keeps the keep-blank flags on save and clears them on authType switch', () => {
    // entity spreads form, which carries the flags from the redacted load.
    expect(script).toMatch(/const entity: Identity = \{\s*\.\.\.form,/)
    // Switching authType invalidates the stored secret; the flags must be
    // dropped so a blank save actually clears instead of restoring.
    expect(script).toContain('if (props.identity && form.authType !== originalAuthType.value)')
    expect(script).toMatch(/entity\.hasPassword = false\s*\n\s*entity\.hasKeyContent = false/)
  })
})

describe('ProxyEditDialog.vue under proxy redaction', () => {
  const src = readSrc('components/ProxyEditDialog.vue')
  const script = scriptBody(src)

  it('shows the saved-secret placeholder when hasPass is set', () => {
    expect(src).toMatch(/:placeholder="savedPassPlaceholder"/)
    expect(script).toContain("props.proxy?.hasPass ? t('conn.savedSecretPlaceholder') : ''")
  })

  it('round-trips the hasPass flag through save (spread of the redacted entity)', () => {
    expect(script).toMatch(/const entity = \{ \.\.\.form \}/)
  })
})
