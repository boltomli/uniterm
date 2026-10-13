import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// The connection store module pulls in the Wails bindings and the runtime
// event bus at load time; stub both so the store can run under vitest.
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadConnections: vi.fn(),
  SaveConnections: vi.fn(),
}))
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

import { useConnectionStore } from './connectionStore'
import { LoadConnections, SaveConnections } from '../../bindings/github.com/ys-ll/uniterm/app'

// Shape of the redacted backend payload: secrets are blank, presence travels
// as flags. These tests pin the store-level contract the Go side relies on
// for its "leave blank to keep" restore in SaveConnections.
interface RedactedConn {
  id: string
  name: string
  host?: string
  port?: number
  user?: string
  type?: string
  authType?: string
  password?: string
  hasPassword?: boolean
}
interface RedactedPayload {
  groups: unknown[]
  connections: RedactedConn[]
}

const redactedPayload: RedactedPayload = {
  groups: [],
  connections: [
    {
      id: 'c1', name: 'prod', host: 'h1', port: 22, user: 'u', type: 'ssh', authType: 'password',
      password: '',
      hasPassword: true,
    },
    {
      id: 'c2', name: 'bare', host: 'h2', port: 22, user: 'u', type: 'ssh', authType: 'password',
      password: '',
      hasPassword: false,
    },
  ],
}

function savedPayload(): RedactedPayload {
  const calls = vi.mocked(SaveConnections).mock.calls
  return calls[calls.length - 1][0] as RedactedPayload
}

describe('connectionStore with redacted LoadConnections payload', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('load keeps the redacted shape and its flags', async () => {
    vi.mocked(LoadConnections).mockResolvedValue(redactedPayload)
    const store = useConnectionStore()
    await store.load()
    expect(store.connections).toHaveLength(2)
    expect(store.connections[0].password).toBe('')
    expect(store.connections[0].hasPassword).toBe(true)
    expect(store.connections[1].hasPassword).toBe(false)
  })

  it('save round-trips the flags so the backend can restore blanked secrets', async () => {
    vi.mocked(LoadConnections).mockResolvedValue(redactedPayload)
    const store = useConnectionStore()
    await store.load()
    await store.save()
    expect(SaveConnections).toHaveBeenCalledTimes(1)
    const payload = savedPayload()
    expect(payload.connections[0].hasPassword).toBe(true)
    expect(payload.connections[0].password).toBe('')
  })

  it('updating with a blank password keeps the hasPassword flag (keep-blank semantics)', async () => {
    vi.mocked(LoadConnections).mockResolvedValue(redactedPayload)
    const store = useConnectionStore()
    await store.load()
    // Editing a connection and saving without touching the password field
    // merges an empty password over the stored object — the flag must
    // survive so the backend restores the stored secret instead of wiping it.
    await store.update('c1', { name: 'renamed', password: '' })
    const payload = savedPayload()
    const c1 = payload.connections.find((c) => c.id === 'c1')
    expect(c1?.name).toBe('renamed')
    expect(c1?.password).toBe('')
    expect(c1?.hasPassword).toBe(true)
  })

  it('typing a new password sends it through (override semantics)', async () => {
    vi.mocked(LoadConnections).mockResolvedValue(redactedPayload)
    const store = useConnectionStore()
    await store.load()
    await store.update('c1', { password: 'typed-new' })
    const payload = savedPayload()
    const c1 = payload.connections.find((c) => c.id === 'c1')
    expect(c1?.password).toBe('typed-new')
  })
})
