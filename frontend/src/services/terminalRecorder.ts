/**
 * terminalRecorder.ts — 前端终端录制服务（asciinema cast v2）
 *
 * 与 backend/recorder/recorder.go 通过 Wails binding 通信：
 *   - start(sessionId, width, height) → RecorderStart → 返回 cast 文件路径
 *   - writeData(data)                  → RecorderWriteData（带 elapsed 时间戳）
 *   - stop()                           → RecorderStop
 *
 * 设计要点：
 *   - 每个 BaseTerminal 实例持有一个 TerminalRecorder，sessionId 维度独立。
 *   - writeData 走独立写入队列（异步串行），不阻塞终端 rAF 渲染主路径。
 *   - 录制写入原始 session:data（未经 sanitize/highlight），保证回放忠实。
 *   - stop() 等待队列排空再调用 RecorderStop，避免丢尾数据。
 */

import {
  RecorderStart,
  RecorderWriteData,
  RecorderStop,
  RecorderIsRecording,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import { msg } from './message'

let sensitiveWarned = false

/**
 * Show the "recordings may contain sensitive information" notice once per
 * profile (persisted in localStorage). Call right before/after the first
 * successful start, from every UI entry point.
 */
export function maybeWarnSensitive(warnText: string): void {
  if (sensitiveWarned) return
  try {
    if (localStorage.getItem('record.sensitiveWarningShown')) {
      sensitiveWarned = true
      return
    }
    localStorage.setItem('record.sensitiveWarningShown', '1')
  } catch {
    return
  }
  sensitiveWarned = true
  msg.warning(warnText)
}

// Session-keyed recorder registry: state must survive component unmount
// (KeepAlive, pane drag) and stay reachable for auto-stop when the terminal
// is released, so recorders live outside the component tree.
const recorders = new Map<string, TerminalRecorder>()

/** Get (or lazily create) the recorder bound to a terminal session. */
export function getSessionRecorder(sessionId: string): TerminalRecorder {
  let rec = recorders.get(sessionId)
  if (!rec) {
    rec = new TerminalRecorder()
    recorders.set(sessionId, rec)
  }
  return rec
}

/** Stop and drop the recorder for a session (called on terminal release). */
export async function disposeSessionRecorder(sessionId: string): Promise<void> {
  const rec = recorders.get(sessionId)
  if (!rec) return
  recorders.delete(sessionId)
  await rec.stop()
}

export interface RecordingMeta {
  filePath: string
  fileName: string
  sessionId: string
  startTime: number // unix seconds
  duration: number // seconds
  size: number // bytes
}

export class TerminalRecorder {
  private sessionId: string | null = null
  private filePath: string | null = null
  private recording = false
  /** 写入队列：串行 await，避免并发 RecorderWriteData 打乱时序。 */
  private writeQueue: Promise<void> = Promise.resolve()
  /** 累计写入字节数（粗略，用于分段提示）。 */
  private bytesWritten = 0
  /** Coalesced write buffer: one IPC per flush instead of per output chunk.
   * Bulk output (cat of a large file, TUI redraws) produces hundreds of
   * chunks per second; per-chunk IPC calls would pile up unboundedly. */
  private pending: string[] = []
  private pendingBytes = 0
  private flushTimer: ReturnType<typeof setTimeout> | null = null
  private static readonly FLUSH_INTERVAL_MS = 40
  private static readonly FLUSH_MAX_BYTES = 64 * 1024

  /** Send the coalesced buffer as a single write. Cast v2 rows concatenate,
   * so one merged "o" event replays identically (40ms timing granularity). */
  private flush(): void {
    if (this.flushTimer) {
      clearTimeout(this.flushTimer)
      this.flushTimer = null
    }
    if (!this.recording || !this.filePath || this.pending.length === 0) return
    const path = this.filePath
    const batch = this.pending.join('')
    this.pending = []
    this.pendingBytes = 0
    this.writeQueue = this.writeQueue
      .then(() => RecorderWriteData(path, batch))
      .catch((err) => {
        console.warn('[terminalRecorder] writeData failed:', err)
      })
  }

  /** 当前是否正在录制。 */
  isRecording(): boolean {
    return this.recording
  }

  /** 当前录制文件路径（录制中有效）。 */
  getFilePath(): string | null {
    return this.filePath
  }

  /** 当前录制的 session id（录制中有效）。 */
  getSessionId(): string | null {
    return this.sessionId
  }

  /** 已写入字节数（粗略估算）。 */
  getBytesWritten(): number {
    return this.bytesWritten
  }

  /**
   * 开始录制。创建 cast 文件并写入 header。
   * @param sessionId 终端 session id
   * @param width     终端列数
   * @param height    终端行数
   * @param title     连接名，写入 cast header 供录制列表展示
   */
  async start(sessionId: string, width: number, height: number, title = ''): Promise<string> {
    if (this.recording) {
      // 已在录制：返回当前文件路径，不重复创建。
      return this.filePath || ''
    }
    if (!sessionId) {
      throw new Error('terminalRecorder: empty sessionId')
    }
    const path = await RecorderStart(sessionId, width || 80, height || 24, title || '')
    this.sessionId = sessionId
    this.filePath = path
    this.recording = true
    this.bytesWritten = 0
    return path
  }

  /**
   * 写入一帧终端输出数据。合并进缓冲，按间隔/大小刷出，不阻塞调用方。
   * 调用方应在 session:data hook 中同步调用，无需 await。
   */
  writeData(data: string): void {
    if (!this.recording || !this.filePath || !data) return
    // 累计字节（UTF-16 → 估长，足够分段提示用）。
    this.bytesWritten += data.length
    this.pending.push(data)
    this.pendingBytes += data.length
    if (this.pendingBytes >= TerminalRecorder.FLUSH_MAX_BYTES) {
      this.flush()
      return
    }
    if (!this.flushTimer) {
      this.flushTimer = setTimeout(() => {
        this.flushTimer = null
        this.flush()
      }, TerminalRecorder.FLUSH_INTERVAL_MS)
    }
  }

  /**
   * 停止录制。等待写入队列排空后关闭文件，返回文件路径。
   */
  async stop(): Promise<string> {
    if (!this.recording) {
      return this.filePath || ''
    }
    const path = this.filePath
    // 先冲刷合并缓冲，再等队列排空并关闭文件，避免丢尾。
    this.flush()
    try {
      await this.writeQueue
    } catch {
      // writeQueue 内已 catch，不会 reject；保险。
    }
    if (path) {
      try {
        await RecorderStop(path)
      } catch (err) {
        console.warn('[terminalRecorder] stop failed:', err)
      }
    }
    this.recording = false
    const ret = this.filePath
    this.filePath = null
    this.sessionId = null
    return ret || ''
  }

  /** 查询后端该路径是否仍在录制（调试用）。 */
  async checkBackendRecording(): Promise<boolean> {
    if (!this.filePath) return false
    try {
      return await RecorderIsRecording(this.filePath)
    } catch {
      return false
    }
  }
}

/**
 * 解析 cast v2 文件内容为可回放的事件序列。
 *
 * @returns { header, events } header 为 cast header 对象；events 为
 *   [elapsed, "o"|"i", data] 三元组数组，按 elapsed 升序。
 */
export interface ParsedCastHeader {
  version: number
  width: number
  height: number
  timestamp: number
  env?: Record<string, string>
}

export interface CastEvent {
  time: number
  type: 'o' | 'i' | string
  data: string
}

export interface ParsedCast {
  header: ParsedCastHeader | null
  events: CastEvent[]
  /** 最后一帧的 elapsed 时间（秒），用于进度条总长。 */
  duration: number
}

export function parseCastContent(content: string): ParsedCast {
  const lines = content.split('\n')
  let header: ParsedCastHeader | null = null
  const events: CastEvent[] = []
  let duration = 0
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim()
    if (!line) continue
    if (i === 0 || (header === null && line.startsWith('{'))) {
      // header 行：JSON object
      try {
        header = JSON.parse(line) as ParsedCastHeader
      } catch {
        // 非 JSON header，跳过
      }
      continue
    }
    // 数据行：[elapsed, "o", "data"]
    try {
      const arr = JSON.parse(line) as [number, string, string]
      if (Array.isArray(arr) && arr.length >= 3 && typeof arr[0] === 'number') {
        events.push({ time: arr[0], type: arr[1], data: arr[2] })
        if (arr[0] > duration) duration = arr[0]
      }
    } catch {
      // 跳过无法解析的行
    }
  }
  return { header, events, duration }
}