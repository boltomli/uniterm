// Debounced persistence of the local-state store: slider drags fire update()
// on every step, so writes coalesce into a single trailing save while the
// in-memory state stays synchronously visible.

import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadLocalState: vi.fn().mockResolvedValue({}),
  SaveLocalState: vi.fn().mockResolvedValue(undefined),
}))

import { useLocalStateStore } from './localStateStore'
import { SaveLocalState } from '../../bindings/github.com/ys-ll/uniterm/app'

const saveMock = vi.mocked(SaveLocalState)

describe('localStateStore.update debounced save', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    saveMock.mockReset()
    saveMock.mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('coalesces rapid updates into a single trailing save of the merged state', async () => {
    const store = useLocalStateStore()

    const promises: Promise<void>[] = []
    for (let i = 1; i <= 10; i++) {
      promises.push(store.update({ backgroundOpacity: i }))
      // patch is visible synchronously, before any save runs
      expect(store.state.backgroundOpacity).toBe(i)
    }
    expect(saveMock).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(300)
    await Promise.all(promises)

    expect(saveMock).toHaveBeenCalledTimes(1)
    const saved = saveMock.mock.calls[0][0] as Record<string, unknown>
    expect(saved.backgroundOpacity).toBe(10)
    expect(saved).toEqual({ ...store.state })
  })

  it('resolves the update promise only after the save has completed', async () => {
    const store = useLocalStateStore()

    let resolved = false
    const p = store.update({ backgroundOpacity: 55 }).then(() => {
      resolved = true
    })

    expect(store.state.backgroundOpacity).toBe(55)
    expect(resolved).toBe(false)

    await vi.advanceTimersByTimeAsync(300)
    await p

    expect(resolved).toBe(true)
    expect(saveMock).toHaveBeenCalledTimes(1)
  })

  it('serializes saves: a queued save waits for the in-flight one', async () => {
    const store = useLocalStateStore()

    let releaseFirst!: () => void
    saveMock.mockImplementationOnce(() => new Promise<void>((res) => { releaseFirst = res }))

    const p1 = store.update({ backgroundOpacity: 10 })
    await vi.advanceTimersByTimeAsync(300)
    expect(saveMock).toHaveBeenCalledTimes(1)

    const p2 = store.update({ backgroundOpacity: 20 })
    await vi.advanceTimersByTimeAsync(300)
    // second save is queued behind the still-running first one
    expect(saveMock).toHaveBeenCalledTimes(1)

    releaseFirst()
    await p1
    await p2

    expect(saveMock).toHaveBeenCalledTimes(2)
    const second = saveMock.mock.calls[1][0] as Record<string, unknown>
    expect(second.backgroundOpacity).toBe(20)
  })

  it('debounced write flushes after 300ms of quiet', async () => {
    const store = useLocalStateStore()

    store.update({ backgroundOpacity: 70 })
    await vi.advanceTimersByTimeAsync(299)
    expect(saveMock).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    expect(saveMock).toHaveBeenCalledTimes(1)
  })
})
