import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick, watch, type WatchStopHandle } from 'vue'

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  DisableSessionOutputLog: vi.fn(async () => {}),
  RegisterSessionForPanel: vi.fn(async () => {}),
  UnregisterSession: vi.fn(async () => {}),
}))

import { usePanelStore } from './panelStore'

const watchers: WatchStopHandle[] = []

beforeEach(() => {
  setActivePinia(createPinia())
  usePanelStore().proxyAddrs.clear()
})

afterEach(() => {
  watchers.splice(0).forEach(stop => stop())
})

describe('remote desktop proxy readiness', () => {
  it('starts a waiting RDP client when the proxy address arrives after mount', async () => {
    const store = usePanelStore()
    const startClient = vi.fn()
    watchers.push(watch(() => store.getProxyAddr('rdp-panel'), address => {
      if (address) startClient(address)
    }, { immediate: true }))
    await nextTick()
    expect(startClient).not.toHaveBeenCalled()

    store.setProxyAddr('rdp-panel', 'ws://127.0.0.1:41000/rdp-token')
    await nextTick()
    expect(startClient).toHaveBeenCalledExactlyOnceWith('ws://127.0.0.1:41000/rdp-token')
  })

  it('updates only the owning panel when a proxy is replaced or removed', async () => {
    const store = usePanelStore()
    store.setProxyAddr('rdp-panel', 'ws://127.0.0.1:41000/old-token')
    const changed = vi.fn()
    watchers.push(watch(() => store.getProxyAddr('rdp-panel'), changed))

    store.setProxyAddr('other-panel', 'ws://127.0.0.1:42000/other-token')
    await nextTick()
    expect(changed).not.toHaveBeenCalled()

    store.setProxyAddr('rdp-panel', 'ws://127.0.0.1:43000/new-token')
    await nextTick()
    expect(changed).toHaveBeenCalledTimes(1)
    expect(changed.mock.calls[0]?.[0]).toBe('ws://127.0.0.1:43000/new-token')

    store.removeProxyAddr('rdp-panel')
    await nextTick()
    expect(changed).toHaveBeenCalledTimes(2)
    expect(changed.mock.calls[1]?.[0]).toBeUndefined()
  })
})