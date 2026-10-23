import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadConnections: vi.fn().mockResolvedValue({ groups: [], connections: [] }),
  SaveConnections: vi.fn().mockResolvedValue(undefined),
}))
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

import { needsCredentialCheck, useTunnelCredentials } from './useTunnelCredentials'
import { useConnectionStore } from '../stores/connectionStore'
import type { ConnectionConfig } from '../types/session'
import type { CredentialResult } from '../components/CredentialPrompt.vue'

// Signature of the credential dialog, mirroring useTunnelCredentials' own
// ShowCredentialDialog type.
type ShowCredentialDialog = (
  title: string,
  subtitle: string,
  fields: ('user' | 'password')[],
  initialUser?: string,
  initialPassword?: string
) => Promise<CredentialResult | null>

function conn(over: Partial<ConnectionConfig>): ConnectionConfig {
  return {
    id: 'c1', name: 'prod', host: 'h1', port: 22, user: 'u',
    type: 'ssh', authType: 'password',
    ...over,
  }
}

describe('needsCredentialCheck with redacted secrets', () => {
  it('does not prompt when the backend reports a saved password', () => {
    // The password itself never reaches the renderer any more; the
    // hasPassword flag is what tells the credential check to stay quiet.
    expect(needsCredentialCheck(conn({ password: '', hasPassword: true }))).toBe(false)
  })

  it('prompts when no password is stored and none was typed', () => {
    expect(needsCredentialCheck(conn({ password: '', hasPassword: false }))).toBe(true)
    expect(needsCredentialCheck(conn({}))).toBe(true)
  })

  it('still skips key/identity auth', () => {
    expect(needsCredentialCheck(conn({ authType: 'key', password: '' }))).toBe(false)
    expect(needsCredentialCheck(conn({ authType: 'identity', password: '' }))).toBe(false)
  })
})

describe('needsCredentialCheck for RDP', () => {
  // RDP joins the credential scope ONLY where NLA/CredSSP needs the account
  // before the desktop appears: an empty password there is not a silent
  // fallback, it is a server-side 登录错误 with no way to correct it.
  const rdp = (over: Partial<ConnectionConfig>) =>
    conn({ type: 'rdp', port: 3389, authType: 'password', ...over })

  it('prompts when NLA is on and no password is stored', () => {
    expect(needsCredentialCheck(rdp({ rdpEnableNLA: true, password: '', hasPassword: false }))).toBe(true)
    expect(needsCredentialCheck(rdp({ rdpEnableNLA: true, password: '' }))).toBe(true)
  })

  it('stays quiet for a stored NLA password (the redacted list)', () => {
    expect(needsCredentialCheck(rdp({ rdpEnableNLA: true, password: '', hasPassword: true }))).toBe(false)
  })

  it('never blocks a password-less connect when NLA is off', () => {
    // The Windows logon screen accepts an empty password; cancelling the
    // prompt would abort a connect that would otherwise succeed.
    expect(needsCredentialCheck(rdp({ rdpEnableNLA: false, password: '' }))).toBe(false)
    expect(needsCredentialCheck(rdp({ rdpEnableNLA: undefined, password: '' }))).toBe(false)
  })
})

describe('useTunnelCredentials with redacted secrets', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('ensureConnectionCredentials passes a stored-password config through without a dialog', async () => {
    const showCredentialDialog = vi.fn<ShowCredentialDialog>()
    const { ensureConnectionCredentials } = useTunnelCredentials(showCredentialDialog)
    const config = conn({ password: '', hasPassword: true })
    const out = await ensureConnectionCredentials(config)
    expect(out).toBe(config)
    expect(showCredentialDialog).not.toHaveBeenCalled()
  })

  it('resolveTunnelCredentials resolves a stored tunnel without a dialog and sends no plaintext', async () => {
    const store = useConnectionStore()
    store.connections.push(conn({
      id: 'tun1', name: 'bastion', password: '', hasPassword: true, user: 'jump',
    }))
    const showCredentialDialog = vi.fn<ShowCredentialDialog>()
    const { resolveTunnelCredentials } = useTunnelCredentials(showCredentialDialog)
    const creds = await resolveTunnelCredentials('tun1')
    expect(showCredentialDialog).not.toHaveBeenCalled()
    expect(creds).toEqual({ user: 'jump', password: '' })
    // The backend resolves the tunnel password Go-side by connection ID
    // (setupJumpHostTunnel), so an empty inline password is correct here.
  })

  it('still prompts when the tunnel has no stored credentials', async () => {
    const store = useConnectionStore()
    store.connections.push(conn({ id: 'tun2', name: 'bastion', user: '', password: '' }))
    const showCredentialDialog = vi.fn<ShowCredentialDialog>().mockResolvedValue({
      user: 'u2', password: 'p2', action: 'connect',
    })
    const { resolveTunnelCredentials } = useTunnelCredentials(showCredentialDialog)
    const creds = await resolveTunnelCredentials('tun2')
    expect(showCredentialDialog).toHaveBeenCalled()
    expect(creds).toEqual({ user: 'u2', password: 'p2' })
  })

  it('prompts for an NLA RDP connection and persists on save_and_connect', async () => {
    const store = useConnectionStore()
    const config = conn({
      id: 'rdp1', name: 'win', type: 'rdp', port: 3389,
      rdpEnableNLA: true, user: 'admin', password: '', hasPassword: false,
    })
    store.connections.push(config)
    const showCredentialDialog = vi.fn<ShowCredentialDialog>().mockResolvedValue({
      user: 'admin', password: 'pw', action: 'save_and_connect',
    })
    const { ensureConnectionCredentials } = useTunnelCredentials(showCredentialDialog)
    const out = await ensureConnectionCredentials(config)
    expect(showCredentialDialog).toHaveBeenCalled()
    // Inline for THIS connect (the WASM handshake reads it) …
    expect(out?.password).toBe('pw')
    // … and persisted so the next connect from the list skips the dialog.
    expect(store.connections.find(c => c.id === 'rdp1')?.password).toBe('pw')
  })

  it('lets an NLA RDP connect through untouched when the password is saved', async () => {
    const store = useConnectionStore()
    const config = conn({
      id: 'rdp2', name: 'win2', type: 'rdp', port: 3389,
      rdpEnableNLA: true, user: 'admin', password: '', hasPassword: true,
    })
    store.connections.push(config)
    const showCredentialDialog = vi.fn<ShowCredentialDialog>()
    const { ensureConnectionCredentials } = useTunnelCredentials(showCredentialDialog)
    const out = await ensureConnectionCredentials(config)
    expect(out).toBe(config)
    expect(showCredentialDialog).not.toHaveBeenCalled()
  })
})
