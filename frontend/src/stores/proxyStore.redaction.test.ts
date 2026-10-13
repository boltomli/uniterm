import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// The proxy store module pulls in the Wails bindings and the runtime event
// bus at load time; stub both so the store can run under vitest.
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadProxies: vi.fn(),
  SaveProxies: vi.fn(),
}))
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

import { useProxyStore } from './proxyStore'
import { LoadProxies, SaveProxies } from '../../bindings/github.com/ys-ll/uniterm/app'
import type { Proxy, ProxyStoreData } from '../types/proxy'

// redactedPayload returns a fresh copy per test: mockResolvedValue hands the
// same object to every load(), and store.update replaces entries in place.
function redactedPayload(): ProxyStoreData {
  return {
    proxies: [
      {
        id: 'p1', name: 'vpn', kind: 'socks5', host: '127.0.0.1', port: 1080,
        user: 'u', pass: '', hasPass: true, enabled: true,
      },
      {
        id: 'p2', name: 'bare', kind: 'http', host: '10.0.0.1', port: 8080,
        pass: '', hasPass: false,
      },
    ],
  }
}

function savedPayload(): ProxyStoreData {
  const calls = vi.mocked(SaveProxies).mock.calls
  const last = calls[calls.length - 1][0]
  return last as ProxyStoreData
}

describe('proxyStore with redacted LoadProxies payload', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('load keeps the redacted shape and its flags', async () => {
    vi.mocked(LoadProxies).mockResolvedValue(redactedPayload())
    const store = useProxyStore()
    await store.load()
    expect(store.proxies).toHaveLength(2)
    expect(store.proxies[0].pass).toBe('')
    expect(store.proxies[0].hasPass).toBe(true)
    expect(store.proxies[0].user).toBe('u')
    expect(store.proxies[1].hasPass).toBe(false)
  })

  it('save round-trips the flags so the backend can restore blanked secrets', async () => {
    vi.mocked(LoadProxies).mockResolvedValue(redactedPayload())
    const store = useProxyStore()
    await store.load()
    await store.save()
    expect(SaveProxies).toHaveBeenCalledTimes(1)
    const payload = savedPayload()
    expect(payload.proxies[0].hasPass).toBe(true)
    expect(payload.proxies[0].pass).toBe('')
  })

  it('updating with a blank pass keeps the hasPass flag (keep-blank semantics)', async () => {
    vi.mocked(LoadProxies).mockResolvedValue(redactedPayload())
    const store = useProxyStore()
    await store.load()
    // The edit dialog spreads the redacted entity (flag included) and only
    // overwrites fields the user touched; the enable toggle in SettingsTab
    // does the same. A blank pass must not drop the flag.
    const edited: Proxy = { ...store.proxies[0], enabled: false, pass: '' }
    await store.update(edited)
    const payload = savedPayload()
    const p1 = payload.proxies.find((p) => p.id === 'p1')
    expect(p1?.enabled).toBe(false)
    expect(p1?.pass).toBe('')
    expect(p1?.hasPass).toBe(true)
  })

  it('typing a new pass sends it through (override semantics)', async () => {
    vi.mocked(LoadProxies).mockResolvedValue(redactedPayload())
    const store = useProxyStore()
    await store.load()
    await store.update({ ...store.proxies[0], pass: 'typed-new' })
    const payload = savedPayload()
    const p1 = payload.proxies.find((p) => p.id === 'p1')
    expect(p1?.pass).toBe('typed-new')
  })
})
