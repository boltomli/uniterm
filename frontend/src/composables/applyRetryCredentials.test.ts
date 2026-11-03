import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { ConnectionConfig } from '../types/session'

vi.mock('@wailsio/runtime', () => ({ Events: { On: vi.fn(() => () => {}) } }))
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({ SaveConnections: vi.fn(), LoadConnections: vi.fn() }))

import { LoadConnections, SaveConnections } from '../../bindings/github.com/ys-ll/uniterm/app'
import { useConnectionStore } from '../stores/connectionStore'
import { applyRetryCredentials } from './applyRetryCredentials'

const original: ConnectionConfig = {
  id: 'ssh-server', name: 'server', type: 'ssh', host: 'server', port: 22,
  authType: 'password', user: 'root', password: 'old-password',
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  let persisted = { groups: [], connections: [{ ...original }] }
  vi.mocked(SaveConnections).mockImplementation(async (data: unknown) => {
    persisted = JSON.parse(JSON.stringify(data))
  })
  vi.mocked(LoadConnections).mockImplementation(async () => JSON.parse(JSON.stringify(persisted)))
})

describe('credentials entered after authentication failure', () => {
  it('persists Save & Connect credentials for a fresh store after restart', async () => {
    const connections = useConnectionStore()
    await connections.load()
    const updated = await applyRetryCredentials(connections.connections[0], {
      action: 'save_and_connect', user: 'deploy', password: 'new-password',
    })
    expect(updated).toMatchObject({ user: 'deploy', password: 'new-password' })
    expect(SaveConnections).toHaveBeenCalledTimes(1)

    setActivePinia(createPinia())
    const restarted = useConnectionStore()
    await restarted.load()
    expect(restarted.connections[0]).toEqual({ ...original, user: 'deploy', password: 'new-password' })
  })

  it('keeps Connect credentials session-only even when the panel shares the saved config', async () => {
    const connections = useConnectionStore()
    await connections.load()
    const shared = connections.connections[0]
    const updated = await applyRetryCredentials(shared, {
      action: 'connect', user: '', password: 'temporary-password',
    })
    expect(updated).toEqual({ ...original, password: 'temporary-password' })
    expect(shared.password).toBe('old-password')
    expect(SaveConnections).not.toHaveBeenCalled()
    // An unrelated later save must not persist the temporary password either.
    await connections.save()
    setActivePinia(createPinia())
    const restarted = useConnectionStore()
    await restarted.load()
    expect(restarted.connections[0]).toEqual(original)
  })

  it('retries an unsaved connection without adding a saved profile', async () => {
    const updated = await applyRetryCredentials({ ...original, id: 'temporary' }, {
      action: 'save_and_connect', user: '', password: 'new-password',
    })
    expect(updated.password).toBe('new-password')
    expect(useConnectionStore().connections).toEqual([])
    expect(SaveConnections).not.toHaveBeenCalled()
  })
})
