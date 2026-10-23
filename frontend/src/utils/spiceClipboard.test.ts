import { describe, expect, it, vi } from 'vitest'
import { createSpiceClipboard } from './spiceClipboard'

function words(...values: number[]) {
  const bytes = new Uint8Array(values.length * 4)
  values.forEach((value, i) => new DataView(bytes.buffer).setUint32(i * 4, value, true))
  return bytes
}

function frame(type: number, payload: Uint8Array = new Uint8Array(0)) {
  const bytes = new Uint8Array(20 + payload.length)
  bytes.set(words(1, type, 0, 0, payload.length))
  bytes.set(payload, 20)
  return bytes
}

function textData(text: string) {
  const utf8 = new TextEncoder().encode(text)
  const bytes = new Uint8Array(4 + utf8.length)
  bytes.set(words(1))
  bytes.set(utf8, 4)
  return bytes
}

function setup(connected = true) {
  const sent: { type: number; bytes: Uint8Array }[] = []
  const onText = vi.fn(async (_text: string) => {})
  const onReady = vi.fn()
  const onError = vi.fn()
  const canReceive = vi.fn(() => true)
  const original = vi.fn((message: { type: number; data: ArrayBuffer }) => {
    if (message.type === 107) {
      connection.agent_connected = true
      connection.announce_agent_capabilities(1)
    }
    if (message.type === 108) connection.agent_connected = false
    if (message.type === 109 && new DataView(message.data).getUint32(4, true) === 6 && new DataView(message.data).getUint32(20, true)) {
      connection.announce_agent_capabilities(0)
    }
    return true
  })
  const announce = vi.fn()
  const connection = {
    agent_connected: connected,
    process_channel_message: original,
    announce_agent_capabilities: announce,
    send_agent_message: (type: number, payload: { buffer_size(): number; to_buffer(buffer: ArrayBuffer): void }) => {
      const buffer = new ArrayBuffer(payload.buffer_size())
      payload.to_buffer(buffer)
      sent.push({ type, bytes: new Uint8Array(buffer) })
    },
  }
  const clipboard = createSpiceClipboard(connection, { onText, onReady, onError, canReceive })
  const deliver = (bytes: Uint8Array) => connection.process_channel_message({ type: 109, data: bytes.slice().buffer })
  const negotiate = (caps = 1 << 5) => deliver(frame(6, words(0, caps)))
  return { clipboard, connection, sent, onText, onReady, onError, canReceive, deliver, negotiate, original, announce }
}

describe('SPICE text clipboard', () => {
  it('negotiates clipboard capabilities without opting into selection or file formats', () => {
    const ctx = setup()
    expect(ctx.sent[0]).toEqual({ type: 6, bytes: words(1, 47) })
    expect(ctx.clipboard.ready).toBe(false)
    ctx.deliver(frame(6, words(1, (1 << 5) | (1 << 6))))
    expect(ctx.sent.at(-1)).toEqual({ type: 6, bytes: words(0, 47) })
    expect(ctx.clipboard.ready).toBe(true)
    expect(ctx.onReady).toHaveBeenCalledOnce()
  })

  it('advertises UTF-8 text and serves Chinese, emoji and line endings on demand', () => {
    const ctx = setup()
    ctx.negotiate()
    const text = '中文😀\nsecond line'
    expect(ctx.clipboard.publish(text)).toBe(true)
    expect(ctx.sent.at(-1)).toEqual({ type: 7, bytes: words(1) })
    ctx.deliver(frame(8, words(1)))
    expect(ctx.sent.at(-1)).toEqual({ type: 4, bytes: textData(text) })
    expect(ctx.onText).not.toHaveBeenCalled()
  })

  it('requests only UTF-8 text from a remote grab and receives it', () => {
    const ctx = setup()
    ctx.negotiate()
    ctx.deliver(frame(7, words(2, 1, 3)))
    expect(ctx.sent.at(-1)).toEqual({ type: 8, bytes: words(1) })
    expect(ctx.clipboard.ready).toBe(false)
    ctx.deliver(frame(4, textData('繁體中文、日本語、한글😀')))
    expect(ctx.onText).toHaveBeenCalledWith('繁體中文、日本語、한글😀')
    expect(ctx.clipboard.ready).toBe(true)
  })

  it('keeps a requested remote copy when focus leaves before its data arrives', () => {
    const ctx = setup()
    ctx.negotiate()
    ctx.deliver(frame(7, words(1)))
    ctx.canReceive.mockReturnValue(false)
    ctx.deliver(frame(4, textData('copied before blur')))
    expect(ctx.onText).toHaveBeenCalledWith('copied before blur')
  })

  it('does not request or overwrite local clipboard text for an inactive desktop', () => {
    const ctx = setup()
    ctx.negotiate()
    ctx.canReceive.mockReturnValue(false)
    const count = ctx.sent.length
    ctx.deliver(frame(7, words(1)))
    ctx.deliver(frame(4, textData('background update')))
    expect(ctx.sent).toHaveLength(count)
    expect(ctx.onText).not.toHaveBeenCalled()
  })

  it('reassembles a large UTF-8 clipboard across fragmented headers and bodies', () => {
    const ctx = setup()
    ctx.negotiate()
    const text = '中文😀\n'.repeat(5000)
    const bytes = frame(4, textData(text))
    for (let i = 0; i < bytes.length; i += 13) ctx.deliver(bytes.slice(i, i + 13))
    expect(ctx.onText).toHaveBeenCalledOnce()
    expect(ctx.onText).toHaveBeenCalledWith(text)
    expect(ctx.onError).not.toHaveBeenCalled()
  })

  it('handles coalesced messages and preserves unknown agent messages', () => {
    const ctx = setup()
    ctx.negotiate()
    const first = frame(7, words(1)), second = frame(4, textData('merged'))
    const merged = new Uint8Array(first.length + second.length)
    merged.set(first)
    merged.set(second, first.length)
    ctx.deliver(merged)
    expect(ctx.onText).toHaveBeenCalledWith('merged')
    const unknown = frame(99, words(42))
    ctx.deliver(unknown)
    expect(ctx.original).toHaveBeenLastCalledWith({ type: 109, data: unknown.buffer })
  })

  it('supports legacy agents and guest CRLF preferences', () => {
    const ctx = setup()
    ctx.negotiate((1 << 3) | (1 << 9))
    ctx.clipboard.publish('first\nsecond\rthird\r\nfourth')
    expect(ctx.sent.at(-1)).toEqual({ type: 4, bytes: textData('first\r\nsecond\r\nthird\r\nfourth') })
    ctx.deliver(frame(4, textData('remote text')))
    expect(ctx.onText).toHaveBeenCalledWith('remote text')
  })

  it('never requests images or replies with unsupported clipboard data', () => {
    const ctx = setup()
    ctx.negotiate()
    const count = ctx.sent.length
    ctx.deliver(frame(7, words(2, 3, 4, 5)))
    expect(ctx.sent).toHaveLength(count)
    ctx.deliver(frame(8, words(2)))
    expect(ctx.sent.at(-1)).toEqual({ type: 4, bytes: words(0) })
    expect(ctx.onText).not.toHaveBeenCalled()
  })

  it('releases an empty local clipboard and does not release a grab superseded by the remote', () => {
    const ctx = setup()
    ctx.negotiate()
    ctx.clipboard.publish('')
    expect(ctx.sent.at(-1)).toEqual({ type: 9, bytes: new Uint8Array(0) })
    ctx.clipboard.publish('local')
    ctx.deliver(frame(7, words(1)))
    ctx.deliver(frame(4, textData('remote')))
    const count = ctx.sent.length
    ctx.clipboard.dispose()
    expect(ctx.sent).toHaveLength(count)
  })

  it('resets on agent disconnect and renegotiates after reconnect', () => {
    const ctx = setup()
    ctx.negotiate()
    ctx.connection.process_channel_message({ type: 108, data: new ArrayBuffer(0) })
    expect(ctx.clipboard.publish('offline')).toBe(false)
    ctx.deliver(frame(4, textData('stale text')))
    expect(ctx.onText).not.toHaveBeenCalled()
    ctx.connection.process_channel_message({ type: 107, data: new ArrayBuffer(0) })
    ctx.negotiate()
    expect(ctx.clipboard.ready).toBe(true)
    expect(ctx.onReady).toHaveBeenCalledTimes(2)
  })

  it('rejects malformed lengths and recovers for subsequent valid messages', () => {
    const ctx = setup()
    ctx.negotiate()
    const oversized = words(1, 4, 0, 0, 0xffffffff)
    ctx.deliver(oversized)
    expect(ctx.onError).toHaveBeenCalledOnce()
    ctx.deliver(frame(4, textData('valid after error')))
    expect(ctx.onText).toHaveBeenCalledWith('valid after error')
  })

  it('restores the cached connection methods and releases its local clipboard ownership', () => {
    const ctx = setup()
    ctx.negotiate()
    ctx.clipboard.publish('local')
    ctx.clipboard.dispose()
    ctx.clipboard.dispose()
    expect(ctx.sent.at(-1)).toEqual({ type: 9, bytes: new Uint8Array(0) })
    expect(ctx.connection.process_channel_message).toBe(ctx.original)
    expect(ctx.connection.announce_agent_capabilities).toBe(ctx.announce)
    expect(ctx.clipboard.publish('disposed')).toBe(false)
  })
})
