// F2: 批量多主机执行 — 执行服务
//
// executeBatch 对一批 SSH/local 主机并发执行同一命令，核心流程：
//   1. 用信号量（Semaphore）限制并发数，避免一次创建大量 SSH session 耗尽资源
//   2. 为每台主机调用 CreateSession + SessionStart 建立临时 session
//   3. SessionWrite 写入命令，并在命令后追加一个唯一完成标记
//      `echo __BATCH_DONE_<id>__$?`，监听输出中出现该标记即命令完成
//   4. 从标记中提取 exit code，非 0 视为失败
//   5. 超时/连接失败/取消 则标记 failed
//   6. CloseSession 关闭临时 session
//   7. 失败策略：onFailure=stop 时，首次失败即取消所有未开始的主机
//
// 复用 localRisk.ts 对命令做危险确认（assessLocalRisk），由调用方
// （BatchExecuteDialog）在执行前调用 confirmIfRisky，本服务只负责执行。
//
// 不改现有单连接逻辑：本服务独立创建/关闭 session，不经过 panelStore/
// tabStore，不影响 UI 中的 tab/panel 状态。
import { CreateSession, CloseSession, SessionStart, SessionWrite } from '../../bindings/github.com/ys-ll/uniterm/app'
import { Events } from '@wailsio/runtime'
import type { ConnectionConfig } from '../types/session'
import { useBatchStore, stripAnsi, type BatchConfig, type BatchFailKind } from '../stores/batchStore'

// ── 信号量：限制并发 SSH session 创建数 ──
class Semaphore {
  private permits: number
  private waiters: (() => void)[] = []
  constructor(permits: number) {
    this.permits = Math.max(1, permits)
  }
  async acquire(): Promise<void> {
    if (this.permits > 0) {
      this.permits--
      return
    }
    await new Promise<void>(resolve => this.waiters.push(resolve))
  }
  release(): void {
    if (this.waiters.length > 0) {
      const w = this.waiters.shift()!
      w()
    } else {
      this.permits++
    }
  }
}

// ── 单主机执行 ──
// 在命令后追加唯一完成标记，监听输出中是否出现该标记以判断命令结束。
// 从标记中提取 exit code：`__BATCH_DONE_<id>__N`，N 为 exit code。
async function executeOnHost(
  conn: ConnectionConfig,
  command: string,
  timeoutMs: number,
  onOutput: (chunk: string) => void,
  isCancelled: () => boolean,
): Promise<{ ok: boolean; error?: string }> {
  const store = useBatchStore()
  let sessionId: string | null = null
  let dataUnsub: (() => void) | null = null
  let statusUnsub: (() => void) | null = null
  let timeoutHandle: ReturnType<typeof setTimeout> | null = null
  let pollHandle: ReturnType<typeof setInterval> | null = null
  let settled = false
  // Whether the backend has emitted session:status 'connected'. Writes only
  // reach the remote PTY once the shell channel is up; before that they are
  // either rejected ("not connected") or swallowed as auth input.
  let connected = false
  // Resolver for the in-flight wait-for-connected gate, so cleanup can
  // release run() if settle happens first.
  let connectedResolve: (() => void) | null = null
  // F2: 一旦 settle（timeout/cancel/error/完成标记）即置 true。
  //     任何在途 await（CreateSession/SessionStart/SessionWrite）返回后都必须
  //     检查它：若已 disposed，说明本次执行早已结束、无人接管新 session，
  //     必须立即 CloseSession，否则会永久泄漏一个后端 SSH session。
  let disposed = false

  // 唯一完成标记（避免与命令输出冲突）
  const doneMarker = `__BATCH_DONE_${conn.id.replace(/[^a-zA-Z0-9]/g, '')}__`
  // 累积输出，用于检测完成标记
  let outputBuffer = ''
  // 完成标记的正则：匹配 doneMarker 后跟一个数字（exit code）
  const doneRe = new RegExp(doneMarker + '(\\d+)')

  // 准备 config：批量执行不需要初始尺寸
  const config: ConnectionConfig = {
    ...conn,
    initialCols: 0,
    initialRows: 0,
  }

  // 标记 running
  store.updateSessionStatus(conn.id, 'running')

  // F2: 关闭当前已创建的 session（幂等）。先同步把 sessionId 置空再 await
  //     CloseSession，保证并发/重复调用绝不会二次关闭同一个 session。
  const closeCurrentSession = async () => {
    if (!sessionId) return
    const sid = sessionId
    sessionId = null
    try { await CloseSession(sid) } catch { /* ignore */ }
  }

  const cleanup = async () => {
    if (timeoutHandle) clearTimeout(timeoutHandle)
    if (pollHandle) clearInterval(pollHandle)
    if (dataUnsub) dataUnsub()
    if (statusUnsub) statusUnsub()
    // Release a pending wait-for-connected so run() resumes, sees disposed
    // and exits instead of hanging forever on an already-settled execution.
    if (connectedResolve) {
      connectedResolve()
      connectedResolve = null
    }
    await closeCurrentSession()
  }

  return new Promise<{ ok: boolean; error?: string }>((resolve) => {
    // settle 终结本次执行：幂等标记 settled/disposed，释放全部资源
    // （含关闭已创建的 session），并保证只 resolve 一次。disposed 供在途
    // await 返回后做泄漏兜底检查。
    const settle = async (ok: boolean, error?: string, kind?: BatchFailKind) => {
      if (settled) return
      settled = true
      disposed = true
      await cleanup()
      store.updateSessionStatus(conn.id, ok ? 'success' : 'failed', error, kind)
      resolve({ ok, error })
    }

    // 超时
    if (timeoutMs > 0) {
      timeoutHandle = setTimeout(() => {
        settle(false, `Timeout after ${Math.round(timeoutMs / 1000)}s`, 'timeout')
      }, timeoutMs)
    }

    // 监听 session:data — 只处理本 session 的数据
    dataUnsub = Events.On('session:data', (ev: any) => {
      const payload = ev?.data
      if (!payload || payload.id !== sessionId) return
      const chunk: string = payload.data || ''
      if (!chunk) return
      onOutput(chunk)
      // Keep only a small tail for marker detection: the marker is printed
      // at the very end, and an unbounded buffer would make every match
      // scan O(total output).
      outputBuffer += chunk
      if (outputBuffer.length > 8192) {
        outputBuffer = outputBuffer.slice(-4096)
      }
      // 检测完成标记
      const m = outputBuffer.match(doneRe)
      if (m) {
        const exitCode = parseInt(m[1], 10)
        // 给一点缓冲时间让剩余输出到达
        setTimeout(() => settle(exitCode === 0, exitCode === 0 ? undefined : `Exit code: ${exitCode}`, exitCode === 0 ? undefined : 'exit'), 100)
      }
    })

    // 监听 session:status — connected 后才允许写入命令；连接失败/断开时结束
    statusUnsub = Events.On('session:status', (ev: any) => {
      const payload = ev?.data
      if (!payload || payload.id !== sessionId) return
      const status: string = payload.status || ''
      if (status === 'connected') {
        connected = true
        if (connectedResolve) {
          connectedResolve()
          connectedResolve = null
        }
      } else if (status === 'error') {
        setTimeout(() => settle(false, 'Connection error', 'connection'), 200)
      }
    })

    // F2: 异步流程写在独立 run() 中，避免 async Promise executor 反模式
    //     （executor 无法取消、异常会被吞）。每个关键 await 返回后立即检查
    //     disposed：若 settle 已先行（timeout/cancel/error/完成），则关闭
    //     「创建晚于 settle」的 session 并退出，杜绝后端 SSH session 泄漏。
    const run = async () => {
      try {
        // 1. 创建 session
        const info = await CreateSession(conn.type, config)
        sessionId = info?.id || null
        if (disposed) {
          // CreateSession 返回晚于 timeout/cancel：此 session 无人接管，立即关闭
          await closeCurrentSession()
          return
        }
        if (!sessionId) {
          await settle(false, 'Failed to create session', 'connection')
          return
        }

        if (isCancelled()) {
          await settle(false, 'Cancelled', 'cancelled')
          return
        }

        // 2. 启动 session（建立 SSH 连接）
        try {
          await SessionStart(sessionId, config)
        } catch (e: any) {
          if (disposed) {
            await closeCurrentSession()
            return
          }
          await settle(false, `Connection failed: ${e?.message || e}`, 'connection')
          return
        }
        if (disposed) {
          await closeCurrentSession()
          return
        }

        if (isCancelled()) {
          await settle(false, 'Cancelled', 'cancelled')
          return
        }

        // 2.5. Wait for the shell channel before writing. SessionStart only
        //      launches the backend connect goroutine and returns immediately;
        //      writing earlier either fails with "not connected" (stdin not
        //      wired yet) or is silently swallowed as auth input by the
        //      backend's buffered authAnswerCh. The overall timeout and
        //      status-error listeners settle the run if the shell never comes
        //      up (e.g. interactive password prompts have no saved secret).
        await new Promise<void>((resolve) => {
          if (connected) {
            resolve()
            return
          }
          connectedResolve = resolve
        })
        if (disposed) {
          await closeCurrentSession()
          return
        }

        // 3. 写入命令 + 完成标记 + 换行
        //    用 ; echo 确保即使命令失败也能输出标记
        //    用 printf 避免换行干扰 exit code 解析
        const fullCommand = `${command}\nprintf '${doneMarker}%d' $?\n`
        try {
          await SessionWrite(sessionId, fullCommand)
        } catch (e: any) {
          if (disposed) {
            await closeCurrentSession()
            return
          }
          await settle(false, `Failed to write command: ${e?.message || e}`, 'connection')
          return
        }
        if (disposed) {
          await closeCurrentSession()
          return
        }

        // 4. 轮询取消标志（每 500ms）
        pollHandle = setInterval(() => {
          if (isCancelled()) {
            settle(false, 'Cancelled', 'cancelled')
          }
        }, 500)

        // 命令完成由 doneMarker 检测触发 settle；超时由 timeoutHandle 触发
        // 若 timeoutMs <= 0 且无完成标记，则一直等到取消
      } catch (e: any) {
        if (disposed) {
          await closeCurrentSession()
          return
        }
        await settle(false, `Unexpected error: ${e?.message || e}`)
      }
    }

    void run()
  })
}

// ── 批量执行主入口 ──
export interface BatchExecuteOptions {
  /** 要执行的主机连接配置列表 */
  hosts: ConnectionConfig[]
  /** 要执行的命令（不含换行） */
  command: string
  /** 批量配置 */
  config: BatchConfig
  /** 输出回调（可选，用于实时展示） */
  onHostOutput?: (hostId: string, chunk: string) => void
  /** 单台主机完成回调 */
  onHostComplete?: (hostId: string, ok: boolean) => void
  /** 取消信号函数（返回 true 表示请求取消） */
  isCancelled?: () => boolean
}

export async function executeBatch(opts: BatchExecuteOptions): Promise<void> {
  const { hosts, command, config, onHostOutput, onHostComplete } = opts
  const store = useBatchStore()
  const isCancelled = opts.isCancelled || (() => store.cancelRequested)

  // 初始化 store
  store.startBatch(
    hosts.map(h => ({
      hostId: h.id,
      hostName: h.name,
      hostSubtitle: `${h.host}:${h.port}`,
    })),
    config,
  )

  const sem = new Semaphore(config.concurrency)
  let hasFailed = false

  // 并发调度：每个主机一个 task，由信号量限制同时运行的数量
  const tasks = hosts.map(async (conn) => {
    // 取消检查：在拿到 permit 之前就可能被取消
    if (isCancelled()) {
      store.updateSessionStatus(conn.id, 'failed', 'Cancelled', 'cancelled')
      onHostComplete?.(conn.id, false)
      return
    }

    // onFailure=stop 且已有失败：跳过未开始的主机
    if (config.onFailure === 'stop' && hasFailed) {
      store.updateSessionStatus(conn.id, 'failed', 'Skipped (onFailure=stop)', 'skipped')
      onHostComplete?.(conn.id, false)
      return
    }

    await sem.acquire()
    try {
      // 再次检查取消（等待 permit 期间可能被取消）
      if (isCancelled()) {
        store.updateSessionStatus(conn.id, 'failed', 'Cancelled', 'cancelled')
        onHostComplete?.(conn.id, false)
        return
      }
      if (config.onFailure === 'stop' && hasFailed) {
        store.updateSessionStatus(conn.id, 'failed', 'Skipped (onFailure=stop)', 'skipped')
        onHostComplete?.(conn.id, false)
        return
      }

      const result = await executeOnHost(
        conn,
        command,
        config.timeout > 0 ? config.timeout * 1000 : 0,
        (chunk) => {
          store.appendOutput(conn.id, chunk)
          onHostOutput?.(conn.id, chunk)
        },
        isCancelled,
      )

      if (!result.ok) {
        hasFailed = true
      }
      onHostComplete?.(conn.id, result.ok)
    } finally {
      sem.release()
    }
  })

  await Promise.allSettled(tasks)
  store.finishBatch()
}

// ── 导出结果为 CSV ──
export function exportCsv(sessions: { hostName: string; hostSubtitle: string; status: string; duration?: number; output: string; error?: string }[]): string {
  const header = 'Host,Subtitle,Status,Duration(ms),Error,Output'
  const rows = sessions.map(s => {
    const duration = s.duration ?? ''
    const error = csvEscape(s.error || '')
    const output = csvEscape(stripAnsi(s.output))
    return csvEscape(s.hostName) + ',' + csvEscape(s.hostSubtitle) + ',' + s.status + ',' + duration + ',' + error + ',' + output
  })
  return [header, ...rows].join('\n')
}

function csvEscape(s: string): string {
  if (s == null) return ''
  // 含逗号/引号/换行则用双引号包裹，内部双引号转义为两个双引号
  if (/[",\n\r]/.test(s)) {
    return '"' + s.replace(/"/g, '""') + '"'
  }
  return s
}

// ── 导出结果为 JSON ──
export function exportJson(sessions: { hostName: string; hostSubtitle: string; status: string; startTime?: number; endTime?: number; output: string; error?: string; truncated?: boolean }[]): string {
  return JSON.stringify(
    sessions.map(s => ({
      host: s.hostName,
      subtitle: s.hostSubtitle,
      status: s.status,
      startTime: s.startTime,
      endTime: s.endTime,
      durationMs: s.startTime && s.endTime ? s.endTime - s.startTime : undefined,
      error: s.error || undefined,
      truncated: s.truncated || undefined,
      output: stripAnsi(s.output),
    })),
    null,
    2,
  )
}
