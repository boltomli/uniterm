import { describe, expect, it, vi } from 'vitest'
import { RdpClipboardSync, RdpInputQueue, describeText, sanitizeClipboardText } from './rdpClipboard'
import type { RdpClipboardContent } from './rdpClipboard'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function fixture() {
  let local: RdpClipboardContent | null = { kind: 'text', text: 'local' }
  const bridge = {
    read: vi.fn(async () => local),
    publish: vi.fn(async (_content: RdpClipboardContent) => {}),
    writeText: vi.fn(async (text: string) => { local = { kind: 'text', text }; return true }),
  }
  return { bridge, sync: new RdpClipboardSync(bridge), set: (content: RdpClipboardContent | null) => { local = content } }
}

describe('RDP clipboard synchronization', () => {
  it('reclaims unchanged local text after another peer takes clipboard ownership', async () => {
    const f = fixture()
    await f.sync.requestFormatList()
    f.bridge.publish.mockClear()
    const read = deferred<RdpClipboardContent | null>()
    f.bridge.read.mockReturnValueOnce(read.promise)
    const stale = f.sync.sync()
    await Promise.resolve()
    f.sync.invalidateLocal()
    read.resolve({ kind: 'text', text: 'stale local copy' })
    await stale
    expect(f.bridge.publish).not.toHaveBeenCalled()
    await f.sync.sync()
    expect(f.bridge.publish).toHaveBeenCalledWith({ kind: 'text', text: 'local' })
  })

  it('can synchronize VNC text without waiting for an RDP handshake', async () => {
    const bridge = {
      read: vi.fn(async () => ({ kind: 'text' as const, text: 'local VNC text' })),
      publish: vi.fn(async () => {}),
      writeText: vi.fn(async () => true),
    }
    const sync = new RdpClipboardSync(bridge, true)
    await sync.sync()
    expect(bridge.publish).toHaveBeenCalledWith({ kind: 'text', text: 'local VNC text' })
    bridge.publish.mockClear()
    await sync.receiveText('remote VNC text')
    bridge.read.mockResolvedValue({ kind: 'text', text: 'remote VNC text' })
    await sync.sync()
    expect(bridge.publish).not.toHaveBeenCalled()
  })
  it('waits for MonitorReady and initializes even with an empty clipboard', async () => {
    const f = fixture()
    await f.sync.sync()
    expect(f.bridge.publish).not.toHaveBeenCalled()
    f.set(null)
    await f.sync.requestFormatList()
    expect(f.bridge.publish).toHaveBeenCalledWith({ kind: 'text', text: '' })
  })

  it('retries failed advertisements instead of caching them as successful', async () => {
    const f = fixture()
    f.bridge.publish.mockRejectedValueOnce(new Error('rejected'))
    await expect(f.sync.requestFormatList()).rejects.toThrow('rejected')
    await f.sync.sync()
    await f.sync.sync()
    expect(f.bridge.publish).toHaveBeenCalledTimes(2)
  })

  it('completes initialization before advertising an initial file copy', async () => {
    const f = fixture()
    const content = { kind: 'files' as const, files: [{ id: 'file', name: 'a.txt', size: 1, lastModified: 0 }] }
    f.set(content)
    await f.sync.requestFormatList()
    expect(f.bridge.publish.mock.calls.map(([value]) => value)).toEqual([{ kind: 'text', text: '' }, content])
  })

  it('does not echo remote text, including CRLF conversion and empty text', async () => {
    const f = fixture()
    await f.sync.requestFormatList()
    f.bridge.publish.mockClear()
    await f.sync.receiveText('远程\r\n🙂')
    f.set({ kind: 'text', text: '远程\n🙂' })
    await f.sync.sync()
    await f.sync.receiveText('')
    await f.sync.sync()
    expect(f.bridge.publish).not.toHaveBeenCalled()
    expect(f.bridge.writeText).toHaveBeenLastCalledWith('')
  })

  it('invalidates an in-flight local read when remote copy arrives', async () => {
    const f = fixture()
    await f.sync.requestFormatList()
    f.bridge.publish.mockClear()
    const reading = deferred<RdpClipboardContent | null>()
    f.bridge.read.mockReturnValueOnce(reading.promise)
    const polling = f.sync.sync()
    await Promise.resolve()
    const remote = f.sync.receiveText('new remote copy')
    reading.resolve({ kind: 'text', text: 'stale local copy' })
    await Promise.all([polling, remote])
    expect(f.bridge.publish).not.toHaveBeenCalled()
  })

  it('serializes polling and paste, waiting for the same format acknowledgement', async () => {
    const f = fixture()
    await f.sync.requestFormatList()
    const ack = deferred<void>()
    f.set({ kind: 'text', text: 'new local copy' })
    f.bridge.publish.mockReturnValueOnce(ack.promise)
    const poll = f.sync.sync()
    const paste = f.sync.sync()
    await vi.waitFor(() => expect(f.bridge.publish).toHaveBeenCalledTimes(2))
    ack.resolve()
    await Promise.all([poll, paste])
    expect(f.bridge.publish).toHaveBeenCalledTimes(2)
  })

  it('does not echo downloaded files and detects switching back to text', async () => {
    const f = fixture()
    await f.sync.requestFormatList()
    f.bridge.publish.mockClear()
    const files = [{ id: 'token', name: '报告.txt', size: 12, lastModified: 1 }]
    await f.sync.receiveFiles(async () => { f.set({ kind: 'files', files }); return files })
    await f.sync.sync()
    expect(f.bridge.publish).not.toHaveBeenCalled()
    f.set({ kind: 'text', text: 'local' })
    await f.sync.sync()
    expect(f.bridge.publish).toHaveBeenCalledWith({ kind: 'text', text: 'local' })
  })

  it('drops queued work on session disposal', async () => {
    const f = fixture()
    const read = deferred<RdpClipboardContent | null>()
    f.bridge.read.mockReturnValueOnce(read.promise)
    const init = f.sync.requestFormatList()
    await Promise.resolve()
    f.sync.dispose()
    read.resolve({ kind: 'text', text: 'old session' })
    await init
    await f.sync.receiveText('old remote')
    expect(f.bridge.publish).not.toHaveBeenCalled()
    expect(f.bridge.writeText).not.toHaveBeenCalled()
  })
})

describe('RDP remote clipboard text repair', () => {
  function repairFixture(localText: () => string) {
    const log = vi.fn()
    const written: string[] = []
    const bridge = {
      read: vi.fn(async () => null),
      publish: vi.fn(async (_content: RdpClipboardContent) => {}),
      writeText: vi.fn(async (text: string) => { written.push(text); return true }),
      readText: vi.fn(async () => localText()),
      rewriteText: vi.fn(async (text: string) => { written.push(text); return true }),
      log,
    }
    return { bridge, sync: new RdpClipboardSync(bridge, true), log, written }
  }

  it('strips NULs and lone surrogates the system clipboard cannot hold', () => {
    expect(sanitizeClipboardText('远\u0000程\uD800测试')).toBe('远程\uFFFD测试')
    expect(sanitizeClipboardText('正常文本\r\n🙂')).toBe('正常文本\r\n🙂')
  })

  it('describes text as code units for the log instead of dumping it', () => {
    expect(describeText('远程')).toBe('len=2 \\u8fdc\\u7a0b')
    expect(describeText('abcdef', 3)).toBe('len=6 \\u0061\\u0062\\u0063…')
  })

  it('accepts a matching read-back without logging a mismatch', async () => {
    const local = '远程文本'
    const f = repairFixture(() => local)
    await f.sync.receiveText(local)
    expect(f.bridge.rewriteText).not.toHaveBeenCalled()
    expect(f.log).toHaveBeenCalledWith('remote clipboard text received: len=4')
    expect(f.log).not.toHaveBeenCalledWith(expect.stringContaining('differs after write'))
  })

  it('repairs a mangled local write through the system clipboard writer', async () => {
    let local = 'very different local content'
    const f = repairFixture(() => local)
    f.bridge.rewriteText.mockImplementation(async (text: string) => { local = text; return true })
    await f.sync.receiveText('远程\r\n文本')
    expect(f.bridge.rewriteText).toHaveBeenCalledWith('远程\r\n文本')
    expect(f.log).toHaveBeenCalledWith(expect.stringContaining('differs after write'))
    expect(f.log).toHaveBeenCalledWith('remote clipboard text repaired on rewrite')
  })

  it('reports a write that stays mangled but keeps the session alive', async () => {
    const f = repairFixture(() => 'garbled')
    await expect(f.sync.receiveText('远程文本')).resolves.toBeUndefined()
    expect(f.log).toHaveBeenCalledWith(expect.stringContaining('still differs after rewrite'))
  })
})

describe('RDP paste input ordering', () => {
  it('keeps V-down/V-up before Ctrl-up after asynchronous clipboard sync', async () => {
    const queue = new RdpInputQueue(vi.fn())
    const events: string[] = []
    const ack = deferred<void>()
    queue.enqueue(() => { events.push('ctrl-down') })
    queue.enqueue(() => ack.promise)
    for (const key of ['v-down', 'v-up', 'ctrl-up']) queue.enqueue(() => { events.push(key) })
    expect(events).toEqual(['ctrl-down'])
    ack.resolve()
    await vi.waitFor(() => expect(events).toEqual(['ctrl-down', 'v-down', 'v-up', 'ctrl-up']))
  })

  it('releases modifiers even when clipboard sync fails', async () => {
    const error = vi.fn()
    const queue = new RdpInputQueue(error)
    const release = vi.fn()
    queue.enqueue(() => Promise.reject(new Error('clipboard busy')))
    queue.enqueue(release)
    await vi.waitFor(() => expect(release).toHaveBeenCalledOnce())
    expect(error).toHaveBeenCalledOnce()
  })

  it('cancels pending paste when focus/session changes', async () => {
    const queue = new RdpInputQueue(vi.fn())
    const ack = deferred<void>()
    const oldKey = vi.fn()
    const newKey = vi.fn()
    queue.enqueue(() => ack.promise)
    queue.enqueue(oldKey)
    queue.clear()
    queue.enqueue(newKey)
    ack.resolve()
    await Promise.resolve()
    expect(oldKey).not.toHaveBeenCalled()
    expect(newKey).toHaveBeenCalledOnce()
  })
})
