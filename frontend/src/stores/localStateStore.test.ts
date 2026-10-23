// Debounced persistence of the local-state store: slider drags fire update()
// on every step, so writes coalesce into a single trailing save while the
// in-memory state stays synchronously visible.

import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({ LoadLocalState: vi.fn(), SaveLocalState: vi.fn() }))

import { useLocalStateStore } from './localStateStore'
import { LoadLocalState, SaveLocalState } from '../../bindings/github.com/ys-ll/uniterm/app'

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
    // The very first update awaits init(); later patches stay synchronous.
    await store.init()

    const promises: Promise<boolean>[] = []
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
    await store.init()

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
    await store.init()

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
    await store.init()

    store.update({ backgroundOpacity: 70 })
    await vi.advanceTimersByTimeAsync(299)
    expect(saveMock).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    expect(saveMock).toHaveBeenCalledTimes(1)
  })
})


// ── local AI preferences (main) ──

describe('local AI preferences', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.resetAllMocks()
    vi.mocked(LoadLocalState).mockResolvedValue({ sidebarVisible: false } as any)
    vi.mocked(SaveLocalState).mockResolvedValue(undefined)
  })

  it('migrates the legacy model once while preserving existing local fields', async () => {
    const store = useLocalStateStore()
    await store.ensureAISelection(['a', 'b'], 'b')
    expect(store.state.ai?.activeModelId).toBe('b')
    expect(store.state.sidebarVisible).toBe(false)
    await store.ensureAISelection(['a', 'b'], 'a')
    expect(store.state.ai?.activeModelId).toBe('b')
    expect(SaveLocalState).toHaveBeenCalledTimes(1)
  })

  it('keeps the local choice ahead of a stale settings selection', async () => {
    vi.mocked(LoadLocalState).mockResolvedValue({ ai: { activeModelId: 'a', modelPreferences: {} } } as any)
    const store = useLocalStateStore()
    await store.ensureAISelection(['a', 'b'], 'b')
    expect(store.state.ai?.activeModelId).toBe('a')
    expect(SaveLocalState).not.toHaveBeenCalled()
    await store.ensureAISelection(['b'], 'a')
    expect(store.state.ai?.activeModelId).toBe('b')
  })

  it('remembers independent model choices after restart', async () => {
    const store = useLocalStateStore()
    await store.ensureAISelection(['a', 'b'])
    await Promise.all([
      store.setAIReasoning('a', { reasoningEffort: 'high' }),
      store.setAIReasoning('b', { reasoningEffort: 'default' }),
    ])
    await store.setActiveAIModel('b')
    const saved = vi.mocked(SaveLocalState).mock.calls.at(-1)![0]
    vi.mocked(LoadLocalState).mockResolvedValue(saved)
    setActivePinia(createPinia())
    const restarted = useLocalStateStore()
    await restarted.init()
    expect(restarted.state.ai).toEqual({ activeModelId: 'b', modelPreferences: {
      a: { reasoningEffort: 'high' },
      b: { reasoningEffort: 'default' },
    } })
  })

  it('serializes writes so an old completion cannot overwrite a newer choice', async () => {
    // Two saves must not coalesce here (the store debounces 300ms), so drive
    // the debounce timer with fake timers between the two writes.
    vi.useFakeTimers()
    try {
      const store = useLocalStateStore()
      await store.init()
      let finishFirst!: () => void
      vi.mocked(SaveLocalState).mockImplementationOnce(() => new Promise(resolve => { finishFirst = () => resolve(undefined) }))
      const first = store.setActiveAIModel('a')
      await vi.waitFor(() => expect(SaveLocalState).toHaveBeenCalledTimes(1))
      const second = store.setActiveAIModel('b')
      finishFirst()
      await vi.waitFor(() => expect(SaveLocalState).toHaveBeenCalledTimes(2))
      await Promise.all([first, second])
      expect(vi.mocked(SaveLocalState).mock.calls.map((call: unknown[]) => (call[0] as any).ai.activeModelId)).toEqual(['a', 'b'])
      expect(store.state.ai?.activeModelId).toBe('b')
    } finally {
      vi.useRealTimers()
    }
  })

  it('reports migration failure instead of allowing legacy selection deletion', async () => {
    vi.mocked(SaveLocalState).mockRejectedValueOnce(new Error('disk full'))
    await expect(useLocalStateStore().ensureAISelection(['a'], 'a')).rejects.toThrow('Could not save')
    await useLocalStateStore().ensureAISelection(['a'], 'a')
    expect(SaveLocalState).toHaveBeenCalledTimes(2)
  })

  it('removes obsolete thinking switches while retaining model and effort choices', async () => {
    vi.mocked(LoadLocalState).mockResolvedValue({ ai: { activeModelId: 'a', modelPreferences: {
      a: { thinkingMode: 'disabled', reasoningEffort: 'high' },
      b: { thinkingMode: 'enabled', reasoningEffort: 'default' },
    } } } as any)
    const store = useLocalStateStore()
    await store.ensureAISelection(['a', 'b'])
    expect(store.state.ai).toEqual({ activeModelId: 'a', modelPreferences: {
      a: { reasoningEffort: 'high' },
      b: { reasoningEffort: 'default' },
    } })
    expect(vi.mocked(SaveLocalState).mock.calls.at(-1)![0].ai).toEqual(store.state.ai)
    await store.setAIReasoning('a', { thinkingMode: 'disabled', reasoningEffort: 'low' } as any)
    expect(vi.mocked(SaveLocalState).mock.calls.at(-1)![0].ai.modelPreferences.a).toEqual({ reasoningEffort: 'low' })
  })
})
