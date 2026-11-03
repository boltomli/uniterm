// F2: 批量多主机执行 — 状态管理
//
// 维护每台主机的执行状态（pending/running/success/failed）、输出缓冲、
// 耗时以及全局配置（并发数/超时/失败策略）。batchExecute.ts 在执行过程
// 中调用 actions 更新状态，BatchExecuteDialog.vue 通过 computed 响应式
// 渲染结果表格。
//
// 设计要点：
// - sessions 用 ref<BatchSession[]> 而非 Map，便于 el-table 直接绑定。
// - cancelRequested 是一个 ref 标志，batchExecute 在每轮并发获取下一个
//   任务前检查该标志，实现"停止"失败策略 + 用户主动取消。
import { defineStore } from 'pinia'
import { ref } from 'vue'

export type BatchSessionStatus = 'pending' | 'running' | 'success' | 'failed'

// Why a host ended up failed — drives the status column label.
export type BatchFailKind = 'timeout' | 'exit' | 'connection' | 'cancelled' | 'skipped'

export interface BatchSession {
  // ConnectionConfig.id，唯一标识一台主机
  hostId: string
  // 显示名（ConnectionConfig.name）
  hostName: string
  // host:port 副标题，用于结果区区分同名主机
  hostSubtitle: string
  status: BatchSessionStatus
  // Failure category, only meaningful on failed rows
  failKind?: BatchFailKind
  // Accumulated remote output (ANSI preserved, stripped on export),
  // tail-capped at MAX_OUTPUT_CHARS
  output: string
  // Cached table preview (ANSI stripped, tail-truncated to PREVIEW_CHARS)
  // so rendering never recomputes it
  preview: string
  // Set once output exceeded the cap and only the tail is kept
  truncated?: boolean
  // 失败时的错误信息（异常/超时）
  error?: string
  startTime?: number
  endTime?: number
}

export interface BatchConfig {
  // Max concurrent hosts, ≥1
  concurrency: number
  // Per-host timeout in seconds, ≤0 means no timeout
  timeout: number
  // Failure policy: continue runs the remaining hosts; stop cancels all
  // hosts that have not started yet
  onFailure: 'continue' | 'stop'
}

export const DEFAULT_BATCH_CONFIG: BatchConfig = {
  concurrency: 5,
  timeout: 30,
  onFailure: 'continue',
}

// ── Output handling constants ──

// Output kept per host (chars); anything before the tail is dropped
const MAX_OUTPUT_CHARS = 1_000_000
// How often buffered chunks are merged into the reactive store (ms)
const FLUSH_INTERVAL_MS = 150
// Tail window scanned to rebuild the cached preview after each flush
const PREVIEW_WINDOW_CHARS = 4096
// Preview length shown in the results table
const PREVIEW_CHARS = 200

// ANSI escape stripping, shared by the preview, the full-output dialog and
// the CSV/JSON export helpers
const ANSI_RE = /\x1b\[[0-9;]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b[()][AB012]|\r/g

export function stripAnsi(s: string): string {
  return s.replace(ANSI_RE, '')
}

export const useBatchStore = defineStore('batch', () => {
  const sessions = ref<BatchSession[]>([])
  const isExecuting = ref(false)
  const config = ref<BatchConfig>({ ...DEFAULT_BATCH_CONFIG })
  // 用户主动取消或 onFailure=stop 触发的取消标志
  const cancelRequested = ref(false)

  // ── Buffered output plumbing ──
  // Chunks land in plain (non-reactive) arrays and are merged into the
  // reactive `output` on a timer. Writing each chunk directly would
  // re-render the results table (and recompute previews) for every event —
  // hundreds of times a second for chatty commands. `output` keeps only the
  // last MAX_OUTPUT_CHARS characters so a runaway command cannot blow up
  // memory or the full-output dialog.
  const pendingOutputs = new Map<string, string[]>()
  let flushTimer: ReturnType<typeof setInterval> | null = null

  function stopFlushTimer() {
    if (flushTimer) {
      clearInterval(flushTimer)
      flushTimer = null
    }
  }

  function flushOutputs() {
    for (const [hostId, chunks] of pendingOutputs) {
      if (chunks.length === 0) continue
      const joined = chunks.join('')
      chunks.length = 0
      const s = sessions.value.find(s => s.hostId === hostId)
      if (!s) continue
      let next = s.output + joined
      if (next.length > MAX_OUTPUT_CHARS) {
        next = next.slice(next.length - MAX_OUTPUT_CHARS)
        s.truncated = true
      }
      s.output = next
      s.preview = buildPreview(next)
    }
  }

  function buildPreview(output: string): string {
    // The preview only shows the tail; scanning the full string on every
    // flush would burn CPU proportional to the cap for every host
    const raw = output.length > PREVIEW_WINDOW_CHARS ? output.slice(-PREVIEW_WINDOW_CHARS) : output
    const tail = stripAnsi(raw).trim()
    if (tail.length <= PREVIEW_CHARS) return tail
    return '...' + tail.slice(-PREVIEW_CHARS)
  }

  /** 初始化一批待执行的 session（清空旧结果） */
  function startBatch(hosts: { hostId: string; hostName: string; hostSubtitle: string }[], cfg: BatchConfig) {
    sessions.value = hosts.map(h => ({
      hostId: h.hostId,
      hostName: h.hostName,
      hostSubtitle: h.hostSubtitle,
      status: 'pending' as BatchSessionStatus,
      output: '',
      preview: '',
    }))
    config.value = { ...cfg }
    isExecuting.value = true
    cancelRequested.value = false
    pendingOutputs.clear()
    stopFlushTimer()
    flushTimer = setInterval(flushOutputs, FLUSH_INTERVAL_MS)
  }

  /** 更新指定主机的状态 */
  function updateSessionStatus(hostId: string, status: BatchSessionStatus, error?: string, failKind?: BatchFailKind) {
    const s = sessions.value.find(s => s.hostId === hostId)
    if (!s) return
    s.status = status
    if (failKind !== undefined) {
      s.failKind = failKind
    }
    if (status === 'running' && !s.startTime) {
      s.startTime = Date.now()
    }
    if (status === 'success' || status === 'failed') {
      s.endTime = Date.now()
    }
    if (error !== undefined) {
      s.error = error
    }
  }

  // 追加输出到指定主机（只攒进非响应式缓冲，由定时器合并进 store）
  function appendOutput(hostId: string, chunk: string) {
    let list = pendingOutputs.get(hostId)
    if (!list) {
      list = []
      pendingOutputs.set(hostId, list)
    }
    list.push(chunk)
  }

  /** 请求取消（用户点击取消按钮或 onFailure=stop 触发） */
  function requestCancel() {
    cancelRequested.value = true
  }

  /** 标记批量执行结束 */
  function finishBatch() {
    flushOutputs()
    stopFlushTimer()
    pendingOutputs.clear()
    isExecuting.value = false
    // 将仍处于 pending/running 的 session 标记为 failed（被取消）
    for (const s of sessions.value) {
      if (s.status === 'pending' || s.status === 'running') {
        s.status = 'failed'
        s.failKind = 'cancelled'
        s.error = s.error || 'Cancelled'
        s.endTime = Date.now()
      }
    }
  }

  /** 清空结果（保留配置） */
  function clearResults() {
    stopFlushTimer()
    pendingOutputs.clear()
    sessions.value = []
    isExecuting.value = false
    cancelRequested.value = false
  }

  /** 重置配置为默认值 */
  function resetConfig() {
    config.value = { ...DEFAULT_BATCH_CONFIG }
  }

  // ── 派生统计 ──
  function countByStatus(status: BatchSessionStatus): number {
    return sessions.value.filter(s => s.status === status).length
  }

  /** 已完成数（success + failed），用于进度显示 */
  function completedCount(): number {
    return countByStatus('success') + countByStatus('failed')
  }

  return {
    sessions,
    isExecuting,
    config,
    cancelRequested,
    startBatch,
    updateSessionStatus,
    appendOutput,
    requestCancel,
    finishBatch,
    clearResults,
    resetConfig,
    countByStatus,
    completedCount,
  }
})
