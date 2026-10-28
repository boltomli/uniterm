import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { effectScope } from 'vue'
import { useConnectionStore } from '../stores/connectionStore'
import { useRemoteDesktopPreferences } from './useRemoteDesktopPreferences'
import type { ConnectionConfig } from '../types/session'

const disk = vi.hoisted(() => ({ data: { connections: [], groups: [] } as any, writes: 0 }))
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  SaveConnections: vi.fn(async (data: unknown) => { disk.data = JSON.parse(JSON.stringify(data)); disk.writes++ }),
  LoadConnections: vi.fn(async () => structuredClone(disk.data)),
}))
vi.mock('@wailsio/runtime', () => ({ Events: { On: vi.fn(() => () => {}) } }))

function connection(type: ConnectionConfig['type'], id = 'desktop'): ConnectionConfig {
  return { id, type, name: id, host: 'host', port: 5900, user: '', authType: 'password' }
}

function open(config: ConnectionConfig | null) {
  const scope = effectScope()
  const preferences = scope.run(() => useRemoteDesktopPreferences(() => config))!
  return { scope, ...preferences }
}

beforeEach(() => {
  setActivePinia(createPinia())
  disk.data = { connections: [], groups: [] }
  disk.writes = 0
})

describe('remote desktop status bar preferences', () => {
  it.each(['vnc', 'rdp', 'spice'] as const)('restores %s options after closing and loading the saved connection', async type => {
    const store = useConnectionStore()
    const config = connection(type)
    await store.add(config)
    await store.add(connection(type, 'other'))
    const first = open(config)
    expect(first.scaleViewport.value).toBe(false)
    first.scaleViewport.value = true
    if (type === 'vnc') {
      first.viewOnly.value = true
      first.showDotCursor.value = true
    }
    // Closing immediately after a toggle must retain the change.
    first.scope.stop()
    await store.load()
    const restored = open(store.connections[0]!)
    expect(restored.scaleViewport.value).toBe(true)
    expect(restored.viewOnly.value).toBe(type === 'vnc')
    expect(restored.showDotCursor.value).toBe(type === 'vnc')
    expect(store.connections[1]!.desktopPreferences).toBeUndefined()

    restored.scaleViewport.value = false
    restored.viewOnly.value = false
    restored.showDotCursor.value = false
    restored.scope.stop()
    await store.load()
    const disabled = open(store.connections[0]!)
    expect(disabled.scaleViewport.value).toBe(false)
    expect(disabled.viewOnly.value).toBe(false)
    expect(disabled.showDotCursor.value).toBe(false)
    disabled.scope.stop()
  })

  it('keeps connect-only preferences on the panel without creating a saved connection', () => {
    const config = connection('vnc')
    const first = open(config)
    first.scaleViewport.value = true
    first.scope.stop()
    const remounted = open(config)
    expect(remounted.scaleViewport.value).toBe(true)
    expect(useConnectionStore().connections).toHaveLength(0)
    expect(disk.writes).toBe(0)
    remounted.scope.stop()
  })

  it('skips the disk write when a toggle lands on the already persisted values', async () => {
    const store = useConnectionStore()
    const config = connection('vnc')
    await store.add(config)
    // Two panels bound to the same connection, both opened before any
    // change — like a status bar and a fullscreen toolbar.
    const panel = open(config)
    const twin = open(config)
    const baseline = disk.writes
    panel.scaleViewport.value = true
    await Promise.resolve()
    expect(disk.writes).toBe(baseline + 1)

    // The twin still holds the stale default; flipping it to the value the
    // first panel already persisted must not serialise the list again.
    twin.scaleViewport.value = true
    twin.viewOnly.value = false
    twin.showDotCursor.value = false
    await Promise.resolve()
    expect(disk.writes).toBe(baseline + 1)

    // A real change still persists.
    twin.viewOnly.value = true
    await Promise.resolve()
    expect(disk.writes).toBe(baseline + 2)
    panel.scope.stop()
    twin.scope.stop()
  })

  it('keeps legacy defaults and does not write on initialization', () => {
    const defaults = open(connection('vnc'))
    expect(defaults.scaleViewport.value).toBe(false)
    expect(defaults.viewOnly.value).toBe(false)
    expect(defaults.showDotCursor.value).toBe(false)
    expect(disk.writes).toBe(0)
    defaults.scope.stop()
  })
})
