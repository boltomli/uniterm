import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// The identity store module pulls in the Wails bindings and the runtime event
// bus at load time; stub both so the store can run under vitest.
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadIdentities: vi.fn(),
  SaveIdentities: vi.fn(),
  ResolveIdentitySecret: vi.fn(),
}))
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

import { useIdentityStore } from './identityStore'
import { LoadIdentities, SaveIdentities } from '../../bindings/github.com/ys-ll/uniterm/app'
import type { Identity, IdentityStoreData } from '../types/identity'

// Shape of the redacted backend payload: secrets are blank, presence travels
// as flags. These tests pin the store-level contract the Go side relies on
// for its "leave blank to keep" restore in SaveIdentities.
interface RedactedIdentity extends Identity {
  password?: string
  hasPassword?: boolean
  hasKeyContent?: boolean
}

// redactedPayload returns a fresh copy per test: mockResolvedValue hands the
// same object to every load(), and store.update replaces entries in place.
function redactedPayload(): IdentityStoreData {
  return {
    identities: [
      {
        id: 'i1', name: 'prod', username: 'root', authType: 'password',
        password: '', hasPassword: true,
      },
      {
        id: 'i2', name: 'key', username: 'root', authType: 'keyText',
        password: '', keyPath: '/home/u/.ssh/id_ed25519', keyContent: '',
        hasPassword: true, hasKeyContent: true,
      },
      {
        id: 'i3', name: 'bare', username: 'root', authType: 'password',
        password: '', hasPassword: false,
      },
    ],
  }
}

function savedPayload(): IdentityStoreData {
  const calls = vi.mocked(SaveIdentities).mock.calls
  return calls[calls.length - 1][0] as IdentityStoreData
}

describe('identityStore with redacted LoadIdentities payload', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('load keeps the redacted shape and its flags', async () => {
    vi.mocked(LoadIdentities).mockResolvedValue(redactedPayload())
    const store = useIdentityStore()
    await store.load()
    expect(store.identities).toHaveLength(3)
    expect(store.identities[0].password).toBe('')
    expect(store.identities[0].hasPassword).toBe(true)
    expect(store.identities[1].hasKeyContent).toBe(true)
    expect(store.identities[2].hasPassword).toBe(false)
  })

  it('save round-trips the flags so the backend can restore blanked secrets', async () => {
    vi.mocked(LoadIdentities).mockResolvedValue(redactedPayload())
    const store = useIdentityStore()
    await store.load()
    await store.save()
    expect(SaveIdentities).toHaveBeenCalledTimes(1)
    const payload = savedPayload()
    expect(payload.identities[0].hasPassword).toBe(true)
    expect(payload.identities[0].password).toBe('')
    expect(payload.identities[1].hasKeyContent).toBe(true)
    expect(payload.identities[1].keyContent).toBe('')
  })

  it('updating with a blank password keeps the hasPassword flag (keep-blank semantics)', async () => {
    vi.mocked(LoadIdentities).mockResolvedValue(redactedPayload())
    const store = useIdentityStore()
    await store.load()
    // The edit dialog spreads the redacted entity (flags included) and only
    // overwrites the fields the user touched; a blank password must not drop
    // the flag or the backend would wipe the stored secret.
    const edited: RedactedIdentity = { ...store.identities[0], name: 'renamed', password: '' }
    await store.update(edited as Identity)
    const payload = savedPayload()
    const i1 = payload.identities.find((i) => i.id === 'i1')
    expect(i1?.name).toBe('renamed')
    expect(i1?.password).toBe('')
    expect(i1?.hasPassword).toBe(true)
  })

  it('typing a new password sends it through (override semantics)', async () => {
    vi.mocked(LoadIdentities).mockResolvedValue(redactedPayload())
    const store = useIdentityStore()
    await store.load()
    await store.update({ ...store.identities[0], password: 'typed-new' })
    const payload = savedPayload()
    const i1 = payload.identities.find((i) => i.id === 'i1')
    expect(i1?.password).toBe('typed-new')
  })

  it('never ships a plaintext secret on load (no enc:v1: ciphertext either)', async () => {
    vi.mocked(LoadIdentities).mockResolvedValue(redactedPayload())
    const store = useIdentityStore()
    await store.load()
    for (const id of store.identities) {
      expect(id.password ?? '').toBe('')
      expect(id.keyContent ?? '').toBe('')
    }
  })
})
