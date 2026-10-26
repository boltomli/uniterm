export type ClipboardFile = { id?: string; name: string; size: number; lastModified: number }
export type RdpClipboardContent =
  | { kind: 'text'; text: string }
  | { kind: 'files'; files: ClipboardFile[] }

type ClipboardBridge = {
  read(): Promise<RdpClipboardContent | null>
  publish(content: RdpClipboardContent): Promise<void>
  writeText(text: string): Promise<boolean>
  /** Platform clipboard read-back, used to detect a mangled local write. */
  readText?(): Promise<string>
  /** Second-chance writer (system clipboard only), tried when the write-back
   * does not match what the remote sent. */
  rewriteText?(text: string): Promise<boolean>
  log?(message: string): void
}

function contentKey(content: RdpClipboardContent): string {
  // Windows converts LF to CRLF when serving CF_UNICODETEXT.
  return content.kind === 'text'
    ? `text:${content.text.replace(/\r\n/g, '\n')}`
    : `files:${JSON.stringify(content.files)}`
}

// A lone surrogate cannot be stored in a Windows clipboard string: the
// platform either drops it or writes two unrelated code units, which then
// paste as unrelated characters. CF_UNICODETEXT is also NUL terminated, so an
// embedded NUL truncates the text mid-way on some targets.
const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g

export function sanitizeClipboardText(text: string): string {
  return text.replace(/\u0000/g, '').replace(LONE_SURROGATE, '\uFFFD')
}

/** Opt-in content diagnostics: `localStorage.setItem('uniterm.debug.clipboard', '1')`.
 * Codepoints of the received text are only written to the log when asked for,
 * because clipboard payloads are user data. */
export function clipboardDebugEnabled(): boolean {
  try {
    return globalThis.localStorage?.getItem('uniterm.debug.clipboard') === '1'
  } catch {
    return false
  }
}

/** Compact code-point dump: length plus the first code units as escapes, so a
 * garbled clipboard text can be told apart from a garbled local write in the
 * app log without dumping whole documents. */
export function describeText(text: string, limit = 24): string {
  const points: string[] = []
  let truncated = false
  for (const character of text) {
    if (points.length >= limit) {
      truncated = true
      break
    }
    points.push(`\\u${character.codePointAt(0)!.toString(16).padStart(4, '0')}`)
  }
  return `len=${text.length} ${points.join('')}${truncated ? '…' : ''}`
}

function normalizeNewlines(text: string): string {
  return text.replace(/\r\n/g, '\n')
}

/** One clipboard owner per session. Remote writes must never be echoed back. */
export class RdpClipboardSync {
  private ready = false
  private disposed = false
  private revision = 0
  private lastKey: string | undefined
  private tail: Promise<void> = Promise.resolve()

  constructor(private readonly bridge: ClipboardBridge, initiallyReady = false) {
    this.ready = initiallyReady
  }

  private log(message: string) {
    this.bridge.log?.(message)
  }

  /** A mangled remote write must be visible in the log AND repaired when a
   * second writer is available: the renderer clipboard write and the system
   * clipboard write can disagree, and only one of them is what other local
   * applications paste. */
  private async verifyLocalWrite(text: string): Promise<void> {
    const read = this.bridge.readText
    if (!read || this.disposed) return
    let actual: string
    try {
      actual = await read()
    } catch {
      return
    }
    if (this.disposed || normalizeNewlines(actual) === normalizeNewlines(text)) return
    this.log(`remote clipboard text differs after write: sent=${describeText(text)} local=${describeText(actual)}`)
    const rewrite = this.bridge.rewriteText
    if (!rewrite) return
    try {
      if (!await rewrite(text)) {
        this.log('remote clipboard text rewrite failed')
        return
      }
      const repaired = await read()
      if (this.disposed) return
      if (normalizeNewlines(repaired) === normalizeNewlines(text)) this.log('remote clipboard text repaired on rewrite')
      else this.log(`remote clipboard text still differs after rewrite: local=${describeText(repaired)}`)
    } catch (error) {
      this.log(`remote clipboard text rewrite error: ${String(error)}`)
    }
  }

  private enqueue(action: () => Promise<void>): Promise<void> {
    const result = this.tail.then(action)
    // A failed read/write/advertise must not poison later retries.
    this.tail = result.catch(() => {})
    return result
  }

  requestFormatList(): Promise<void> {
    // MonitorReady is the protocol's initialization trigger. Do not send
    // capabilities/format lists from polling before it arrives.
    this.ready = true
    return this.sync(true)
  }

  invalidateLocal(): void {
    ++this.revision
    this.lastKey = undefined
  }

  sync(force = false, text?: string): Promise<void> {
    const revision = this.revision
    return this.enqueue(async () => {
      if (!this.ready || this.disposed || (!force && revision !== this.revision)) return
      let content: RdpClipboardContent | null = text === undefined
        ? await this.bridge.read()
        : { kind: 'text', text }
      if (this.disposed) return
      if (revision !== this.revision) {
        if (!force) return
        content = { kind: 'text', text: '' }
      }
      // Even an empty initial clipboard must complete the handshake.
      if (!content && force) content = { kind: 'text', text: '' }
      if (!content) return
      const key = contentKey(content)
      if (!force && key === this.lastKey) return
      // initiate_file_copy requires Ready. Only a text/empty format list
      // drives the client's initial Capabilities + TempDir + FormatList.
      if (force && content.kind === 'files') await this.bridge.publish({ kind: 'text', text: '' })
      if (this.disposed) return
      await this.bridge.publish(content)
      if (!this.disposed && revision === this.revision) this.lastKey = key
    })
  }

  receiveText(rawText: string): Promise<void> {
    const text = sanitizeClipboardText(rawText)
    // A length-only line proves whether text crossed the bridge at all (an empty
    // delivery looks exactly like a broken read); the payload itself stays out
    // of the log unless content diagnostics are enabled.
    this.log(`remote clipboard text received: len=${rawText.length}`)
    if (clipboardDebugEnabled()) this.log(`remote clipboard text content: ${describeText(rawText)}`)
    if (text !== rawText) this.log(`remote clipboard text sanitized: received=${describeText(rawText)}`)
    ++this.revision // Invalidate any local read already in flight.
    return this.enqueue(async () => {
      if (this.disposed) return
      if (!await this.bridge.writeText(text)) throw new Error('System clipboard write failed')
      if (this.disposed) return
      this.lastKey = contentKey({ kind: 'text', text })
      await this.verifyLocalWrite(text)
    })
  }

  receiveFiles(download: () => Promise<ClipboardFile[]>): Promise<void> {
    const revision = ++this.revision
    return this.enqueue(async () => {
      if (this.disposed || revision !== this.revision) return
      const files = await download()
      if (!this.disposed && revision === this.revision) this.lastKey = contentKey({ kind: 'files', files })
    })
  }

  dispose() {
    this.disposed = true
    ++this.revision
  }
}

/** Keep all key transitions ordered while clipboard I/O delays a paste. */
export class RdpInputQueue {
  private tasks: Array<() => void | Promise<void>> = []
  private busy = false
  private generation = 0

  constructor(private readonly onError: (error: unknown) => void) {}

  enqueue(action: () => void | Promise<void>) {
    this.tasks.push(action)
    this.drain()
  }

  clear() {
    ++this.generation
    this.tasks = []
    this.busy = false
  }

  private drain() {
    if (this.busy) return
    while (this.tasks.length) {
      const action = this.tasks.shift()!
      try {
        const result = action()
        if (result) {
          this.busy = true
          const generation = this.generation
          Promise.resolve(result).catch(this.onError).then(() => {
            if (generation !== this.generation) return
            this.busy = false
            this.drain()
          })
          return
        }
      } catch (error) {
        this.onError(error)
      }
    }
  }
}
