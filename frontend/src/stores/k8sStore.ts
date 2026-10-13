import { defineStore } from 'pinia'
import { ref, shallowRef } from 'vue'
import type { K8sWatchEvent } from '../types/k8s'
import * as client from '../services/k8sClient'
import { getResource } from '../services/k8sResources'

interface ResourceListState {
  items: Map<string, any>          // uid → object
  resourceVersion: string
  watch: client.WatchHandle | null
  refCount: number
  error: string
}

// 单资源 list 分页上限：跟随 metadata.continue 取全量而不是停在 500 条，
// 同时防止异常服务端无限续传；超过上限写入明确错误（静默截断才算 bug）。
const MAX_LIST_PAGES = 20

export const useK8sStore = defineStore('k8s', () => {
  // key = `${connID}::${resourceKey}::${ns}` （ns='' 表示 all / 集群级）
  // shallowRef：值不做深代理（items 里的对象保持 raw，无 per-item proxy），
  // 槽位 Map 只在显式通知时整体换一份（见 notify/notifySoon）。
  const states = shallowRef<Map<string, ResourceListState>>(new Map())

  // 初始 list 进行中的 key 集合；驱动列表遮罩。watch 事件不算 loading。
  const loadingKeys = shallowRef<Set<string>>(new Set())
  function setLoading(key: string, v: boolean) {
    const next = new Set(loadingKeys.value)
    if (v) next.add(key)
    else next.delete(key)
    loadingKeys.value = next
  }
  function isLoading(connID: string, resourceKey: string, ns: string): boolean {
    const desc = getResource(resourceKey)
    const effectiveNs = desc && !desc.namespaced ? '' : ns
    return loadingKeys.value.has(k(connID, resourceKey, effectiveNs))
  }

  function k(connID: string, resourceKey: string, ns: string) {
    return `${connID}::${resourceKey}::${ns}`
  }

  // shallowRef 的 Map 突变不会触发响应式；换一份槽位 Map 才会。槽位对象本身
  // 不换（identity 稳定，竞态守卫只在 delete/重建时才变）。
  function setSlot(key: string, st: ResourceListState) {
    const next = new Map(states.value)
    next.set(key, st)
    states.value = next
  }
  function deleteSlot(key: string) {
    const next = new Map(states.value)
    next.delete(key)
    states.value = next
  }

  // watch 事件高频（一次 rollout 可能一 tick 内几十个）：handleEvent 就地改
  // items（读方立刻可见），通知按微任务合并——每 tick 至多换一次槽位 Map，
  // 而不是每个事件复制一遍整个 items Map + 触发一轮响应式。
  let notifyQueued = false
  function notifySoon() {
    if (notifyQueued) return
    notifyQueued = true
    queueMicrotask(() => {
      notifyQueued = false
      states.value = new Map(states.value)
    })
  }

  async function subscribe(connID: string, resourceKey: string, ns: string) {
    const desc = getResource(resourceKey)
    const effectiveNs = desc && !desc.namespaced ? '' : ns
    const key = k(connID, resourceKey, effectiveNs)
    if (!desc) {
      // 未知资源：写错误，不发请求
      setSlot(key, {
        items: new Map(), resourceVersion: '', watch: null, refCount: 1,
        error: `unknown resource ${resourceKey}`,
      })
      return
    }

    let st = states.value.get(key)
    if (st) {
      st.refCount++
      return
    }
    st = { items: new Map(), resourceVersion: '', watch: null, refCount: 1, error: '' }
    setSlot(key, st)

    // 初始 list：跟随 metadata.continue 分页取全量（每页沿用 listPath 的 limit）
    setLoading(key, true)
    const listPath = desc.listPath(effectiveNs)
    let listError = ''
    let resourceVersion = ''
    let continueToken = ''
    let pages = 0
    for (;;) {
      const url = pages === 0
        ? listPath
        : `${listPath}${listPath.includes('?') ? '&' : '?'}continue=${encodeURIComponent(continueToken)}`
      const { status, data, raw } = await client.requestJSON(connID, 'GET', url)
      // 竞态守卫：每页 await 后 state 可能已被 unsubscribe（或换订阅）就丢弃结果
      if (states.value.get(key) !== st) { setLoading(key, false); return }
      if (status !== 200 || !data) {
        listError = `list ${desc.kind} HTTP ${status}: ${raw?.slice(0, 400) || ''}`
        break
      }
      for (const item of data.items || []) {
        st.items.set(item.metadata?.uid || item.metadata?.name, item)
      }
      resourceVersion = data.metadata?.resourceVersion || resourceVersion
      continueToken = data.metadata?.continue || ''
      pages++
      if (!continueToken) break
      if (pages >= MAX_LIST_PAGES) {
        listError = `list ${desc.kind}: aborted after ${MAX_LIST_PAGES} pages (server kept sending continue token)`
        break
      }
    }
    if (listError) {
      st.error = listError
      setLoading(key, false)
      states.value = new Map(states.value)  // 通知错误与已取到的分页项
      return
    }
    st.resourceVersion = resourceVersion
    states.value = new Map(states.value)    // 通知 list 结果

    // watch
    const watchPath = desc.watchPath(effectiveNs, st.resourceVersion)
    const handle = await client.startWatch(connID, watchPath, (ev) => handleEvent(key, ev))
    // 竞态守卫：startWatch 期间 state 已消失/被替换，停掉孤儿 watch
    if (states.value.get(key) !== st) {
      handle.stop(); setLoading(key, false)
      return
    }
    st.watch = handle
    setLoading(key, false)
  }

  function handleEvent(key: string, ev: K8sWatchEvent) {
    const st = states.value.get(key)
    if (!st || !ev.object) return
    const uid = ev.object.metadata?.uid || ev.object.metadata?.name
    if (!uid) return
    const rv = ev.object.metadata?.resourceVersion
    switch (ev.type) {
      case 'ADDED':
      case 'MODIFIED':
        st.items.set(uid, ev.object)
        if (rv) st.resourceVersion = rv
        notifySoon()
        break
      case 'DELETED':
        st.items.delete(uid)
        if (rv) st.resourceVersion = rv
        notifySoon()
        break
      case 'BOOKMARK':
        // 只推进 resourceVersion（非响应式读取：下次 subscribe 才用），不打扰渲染
        if (rv) st.resourceVersion = rv
        break
    }
  }

  function unsubscribe(connID: string, resourceKey: string, ns: string) {
    const desc = getResource(resourceKey)
    const effectiveNs = desc && !desc.namespaced ? '' : ns
    const key = k(connID, resourceKey, effectiveNs)
    const st = states.value.get(key)
    if (!st) return
    st.refCount--
    if (st.refCount <= 0) {
      st.watch?.stop()
      deleteSlot(key)
    }
  }

  function getItems(connID: string, resourceKey: string, ns: string): any[] {
    const desc = getResource(resourceKey)
    const effectiveNs = desc && !desc.namespaced ? '' : ns
    const st = states.value.get(k(connID, resourceKey, effectiveNs))
    if (!st) return []
    return Array.from(st.items.values())
  }

  function getError(connID: string, resourceKey: string, ns: string): string {
    const desc = getResource(resourceKey)
    const effectiveNs = desc && !desc.namespaced ? '' : ns
    return states.value.get(k(connID, resourceKey, effectiveNs))?.error || ''
  }

  // Per-connection connect status, surfaced on the k8s tab so a failed/absent
  // connection isn't silent. Keyed by the ConnectionConfig.id the tab was opened for.
  const connStatus = ref<Map<string, 'connecting' | 'connected' | 'error'>>(new Map())
  function setConnStatus(connectionId: string, status: 'connecting' | 'connected' | 'error') {
    connStatus.value.set(connectionId, status)
    connStatus.value = new Map(connStatus.value)
  }
  function getConnStatus(connectionId: string): '' | 'connecting' | 'connected' | 'error' {
    return connStatus.value.get(connectionId) || ''
  }

  return { subscribe, unsubscribe, getItems, getError, isLoading, connStatus, setConnStatus, getConnStatus }
})
