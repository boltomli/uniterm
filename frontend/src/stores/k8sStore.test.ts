import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { watch } from 'vue'
import { readSrc } from '../testUtils/sfc'
import type { K8sWatchEvent } from '../types/k8s'

// mock k8sClient 底层调用，避免真的走 Wails
const requestJSON = vi.fn()
const startWatch = vi.fn()
const stopWatch = vi.fn()

vi.mock('../services/k8sClient', () => ({
  requestJSON: (...a: any[]) => requestJSON(...a),
  startWatch: (...a: any[]) => startWatch(...a),
}))

import { useK8sStore } from './k8sStore'

function mkPod(name: string, ns = 'default', uid = name) {
  return {
    metadata: { name, namespace: ns, uid, resourceVersion: '1', creationTimestamp: new Date().toISOString() },
    status: { phase: 'Running', containerStatuses: [{ ready: true, restartCount: 0 }] },
    spec: { nodeName: 'n1' },
  }
}

describe('k8sStore.subscribe (generic)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    requestJSON.mockReset()
    startWatch.mockReset()
    stopWatch.mockReset()
    startWatch.mockResolvedValue({ id: 'w1', stop: stopWatch })
  })

  it('subscribe(pods, default) lists then getItems returns items', async () => {
    requestJSON.mockResolvedValue({
      status: 200,
      data: { items: [mkPod('p1'), mkPod('p2')], metadata: { resourceVersion: '99' } },
      raw: '',
    })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')
    const items = s.getItems('c1', 'pods', 'default')
    expect(items.map((i: any) => i.metadata.name).sort()).toEqual(['p1', 'p2'])
    expect(requestJSON).toHaveBeenCalledWith('c1', 'GET', '/api/v1/namespaces/default/pods?limit=500')
  })

  it('list HTTP error writes to getError', async () => {
    requestJSON.mockResolvedValue({ status: 403, data: null, raw: 'Forbidden' })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')
    expect(s.getError('c1', 'pods', 'default')).toContain('403')
  })

  it('unsubscribe with refCount 0 stops watch and clears items', async () => {
    requestJSON.mockResolvedValue({ status: 200, data: { items: [mkPod('p1')], metadata: { resourceVersion: '1' } }, raw: '' })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')
    expect(s.getItems('c1', 'pods', 'default').length).toBe(1)
    s.unsubscribe('c1', 'pods', 'default')
    expect(s.getItems('c1', 'pods', 'default').length).toBe(0)
    expect(stopWatch).toHaveBeenCalled()
  })

  it('subscribing different (resource, ns) pairs keeps independent state', async () => {
    requestJSON.mockResolvedValue({ status: 200, data: { items: [], metadata: { resourceVersion: '1' } }, raw: '' })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')
    await s.subscribe('c1', 'pods', 'kube-system')
    expect(s.getError('c1', 'pods', 'default')).toBe('')
    expect(s.getError('c1', 'pods', 'kube-system')).toBe('')
  })

  it('unknown resourceKey writes error and does not throw', async () => {
    const s = useK8sStore()
    await s.subscribe('c1', 'no-such-thing', 'default')
    expect(s.getError('c1', 'no-such-thing', 'default')).toMatch(/unknown resource/i)
    expect(requestJSON).not.toHaveBeenCalled()
  })

  it('cluster-scoped resource (nodes) stores state under empty ns regardless of passed ns', async () => {
    requestJSON.mockResolvedValue({ status: 200, data: { items: [], metadata: { resourceVersion: '1' } }, raw: '' })
    const s = useK8sStore()
    await s.subscribe('c1', 'nodes', 'default')
    // 传 'default'，但 nodes 是集群级：state 挂在空 ns 下
    expect(s.getItems('c1', 'nodes', 'anything-else')).toEqual([])
    // 请求 URL 里没有 namespaces/
    expect(requestJSON).toHaveBeenCalledWith('c1', 'GET', '/api/v1/nodes?limit=500')
  })

  it('subscribe survives unsubscribe mid-list (no NPE, no orphan watch)', async () => {
    // requestJSON never resolves until we let it — simulate a slow list call.
    let resolveList: (v: any) => void = () => {}
    requestJSON.mockImplementation(() => new Promise(r => { resolveList = r }))

    const s = useK8sStore()
    const subP = s.subscribe('c1', 'pods', 'default')  // hangs on await
    // Unsubscribe before list resolves.
    s.unsubscribe('c1', 'pods', 'default')
    // Now let the list finish; subscribe should notice state is gone and bail.
    resolveList({ status: 200, data: { items: [{ metadata: { uid: 'p1', name: 'p1' } }], metadata: { resourceVersion: '1' } }, raw: '' })
    await subP
    // No items visible (state was torn down).
    expect(s.getItems('c1', 'pods', 'default')).toEqual([])
    // Watch never started (guarded by the post-list identity check).
    expect(startWatch).not.toHaveBeenCalled()
  })

  it('subscribe survives unsubscribe mid-startWatch (calls stop on the orphan handle)', async () => {
    requestJSON.mockResolvedValue({ status: 200, data: { items: [], metadata: { resourceVersion: '1' } }, raw: '' })
    // Slow startWatch.
    let resolveWatch: (v: any) => void = () => {}
    const orphanStop = vi.fn()
    startWatch.mockImplementation(() => new Promise(r => { resolveWatch = r }))

    const s = useK8sStore()
    const subP = s.subscribe('c1', 'pods', 'default')
    // Let list finish, but startWatch hangs — unsubscribe now.
    await Promise.resolve()  // yield so list resolves
    await Promise.resolve()  // extra tick for chained then
    s.unsubscribe('c1', 'pods', 'default')
    resolveWatch({ id: 'w-orphan', stop: orphanStop })
    await subP
    // Orphan watch handle's stop() was called.
    expect(orphanStop).toHaveBeenCalled()
  })
})

describe('k8sStore list pagination (security.md #9)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    requestJSON.mockReset()
    startWatch.mockReset()
    stopWatch.mockReset()
    startWatch.mockResolvedValue({ id: 'w1', stop: stopWatch })
  })

  it('follows metadata.continue and merges every page', async () => {
    requestJSON
      .mockResolvedValueOnce({
        status: 200,
        data: { items: [mkPod('p1'), mkPod('p2')], metadata: { resourceVersion: '99', continue: 'tok1' } },
        raw: '',
      })
      .mockResolvedValueOnce({
        status: 200,
        data: { items: [mkPod('p3')], metadata: { resourceVersion: '100', continue: '' } },
        raw: '',
      })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')
    expect(requestJSON).toHaveBeenCalledTimes(2)
    expect(requestJSON).toHaveBeenNthCalledWith(1, 'c1', 'GET', '/api/v1/namespaces/default/pods?limit=500')
    expect(requestJSON).toHaveBeenNthCalledWith(2, 'c1', 'GET', '/api/v1/namespaces/default/pods?limit=500&continue=tok1')
    expect(s.getItems('c1', 'pods', 'default').map(i => i.metadata.name).sort()).toEqual(['p1', 'p2', 'p3'])
    expect(s.getError('c1', 'pods', 'default')).toBe('')
    // watch 从最后一页的 resourceVersion 续订
    expect(startWatch).toHaveBeenCalledWith('c1', expect.stringContaining('resourceVersion=100'), expect.any(Function))
  })

  it('aborts after 20 pages when the server never ends the continue chain', async () => {
    let calls = 0
    requestJSON.mockImplementation(() => {
      calls += 1
      return Promise.resolve({
        status: 200,
        data: { items: [mkPod(`p${calls}`)], metadata: { resourceVersion: String(calls), continue: `t${calls}` } },
        raw: '',
      })
    })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')
    expect(calls).toBe(20)
    expect(s.getError('c1', 'pods', 'default')).toMatch(/aborted after 20 pages/)
    expect(startWatch).not.toHaveBeenCalled()
  })
})

describe('k8sStore item identity (security.md #8)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    requestJSON.mockReset()
    startWatch.mockReset()
    stopWatch.mockReset()
    startWatch.mockResolvedValue({ id: 'w1', stop: stopWatch })
  })

  it('returns the raw item objects (no deep proxy per item)', async () => {
    const pod = mkPod('p1')
    requestJSON.mockResolvedValue({ status: 200, data: { items: [pod], metadata: { resourceVersion: '1' } }, raw: '' })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')
    expect(s.getItems('c1', 'pods', 'default')[0]).toBe(pod)
  })
})

describe('k8sStore watch event batching (security.md #8)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    requestJSON.mockReset()
    startWatch.mockReset()
    stopWatch.mockReset()
  })

  it('applies an event burst with a single reactive flush', async () => {
    requestJSON.mockResolvedValue({ status: 200, data: { items: [mkPod('p0')], metadata: { resourceVersion: '1' } }, raw: '' })
    let onEvent: (ev: K8sWatchEvent) => void = () => {}
    startWatch.mockImplementation(async (_conn: string, _path: string, cb: (ev: K8sWatchEvent) => void) => {
      onEvent = cb
      return { id: 'w1', stop: stopWatch }
    })
    const s = useK8sStore()
    await s.subscribe('c1', 'pods', 'default')

    const lengths: number[] = []
    watch(() => s.getItems('c1', 'pods', 'default'), v => lengths.push(v.length), { flush: 'sync' })

    for (let i = 1; i <= 5; i++) onEvent({ type: 'ADDED', object: mkPod(`p${i}`) })
    // 事件就地写入 items，微任务里统一通知一次：同步阶段不应逐事件触发
    expect(lengths).toEqual([])
    await Promise.resolve()
    expect(lengths).toEqual([6])
  })
})

describe('k8sStore source wiring (shallowRef + microtask batch)', () => {
  const src = readSrc('stores/k8sStore.ts')

  it('holds list state in a shallowRef so items stay raw', () => {
    expect(src).toMatch(/const states = shallowRef<Map<string, ResourceListState>>/)
  })

  it('notifies watch events on a microtask instead of per event', () => {
    expect(src).toMatch(/queueMicrotask\(/)
    expect(src).not.toMatch(/new Map\(st\.items\)/)
  })
})
