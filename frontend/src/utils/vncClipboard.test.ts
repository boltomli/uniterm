import { describe, expect, it, vi } from 'vitest'
import { createVncClipboard } from './vncClipboard'
import { RdpClipboardSync } from './rdpClipboard'

const text = '中文测似乎'
const legacy = 'ÖÐÎÄ²âËÆºõ'

function setup(encoding = 'gbk') {
  let incoming = ''
  let decoded = ''
  let header = new Uint8Array(7)
  const rfb = {
    clipboardPasteFrom: vi.fn(),
    _clipboardServerCapabilitiesFormats: {} as Record<number, boolean>,
    _clipboardServerCapabilitiesActions: {} as Record<number, boolean>,
    _sock: { rQpeekBytes: () => header },
    _handleServerCutText: () => {
      if (header.length < 7) return false
      decoded = clipboard.decode(incoming)
      return true
    },
  }
  const original = rfb._handleServerCutText
  const encode = vi.fn(async () => btoa(legacy))
  const active = vi.fn(() => true)
  const clipboard = createVncClipboard(rfb, encoding, encode, active)
  function receive(value: string, extended = false) {
    incoming = value
    header = new Uint8Array([0, 0, 0, extended ? 0xff : 0, 0, 0, 0])
    rfb._handleServerCutText()
    return decoded
  }
  function unicode() {
    rfb._clipboardServerCapabilitiesFormats[1] = true
    rfb._clipboardServerCapabilitiesActions[1 << 27] = true
  }
  return { rfb, clipboard, encode, active, receive, unicode, original, partial: () => { header = new Uint8Array(3) } }
}

describe('VNC clipboard encodings', () => {
  it('repairs the reported Windows GBK sample in both directions', async () => {
    const ctx = setup()
    expect(ctx.receive(legacy)).toBe(text)
    await ctx.clipboard.publish(text)
    expect(ctx.rfb.clipboardPasteFrom).toHaveBeenCalledWith(legacy)
    expect(ctx.encode).toHaveBeenCalledWith(text, 'gbk')
  })

  it.each([
    ['gb2312', '中文', '\xd6\xd0\xce\xc4'],
    ['gb18030', '中文😀', '\xd6\xd0\xce\xc4\x94\x39\xfc\x36'],
    ['big5', '中文', '\xa4\xa4\xa4\xe5'],
    ['shift-jis', '日本語', '\x93\xfa\x96\x7b\x8c\xea'],
    ['euc-jp', '日本語', '\xc6\xfc\xcb\xdc\xb8\xec'],
    ['euc-kr', '한글', '\xc7\xd1\xb1\xdb'],
  ])('supports %s clipboard bytes in both directions', async (charset, value, binary) => {
    const ctx = setup(charset)
    ctx.encode.mockResolvedValue(btoa(binary))
    expect(ctx.receive(binary)).toBe(value)
    await ctx.clipboard.publish(value)
    expect(ctx.encode).toHaveBeenCalledWith(value, charset)
    expect(ctx.rfb.clipboardPasteFrom).toHaveBeenCalledWith(binary)
  })

  it.each(['gbk', 'gb2312', 'gb18030', 'big5', 'shift-jis', 'euc-jp', 'euc-kr'])('leaves Unicode extension messages intact when %s is configured', async charset => {
    const ctx = setup(charset)
    ctx.unicode()
    expect(ctx.receive('中文😀', true)).toBe('中文😀')
    await ctx.clipboard.publish('中文😀')
    expect(ctx.rfb.clipboardPasteFrom).toHaveBeenCalledWith('中文😀')
    expect(ctx.encode).not.toHaveBeenCalled()
  })

  it.each(['auto', 'latin1', '', 'unknown'])('preserves the default noVNC path for %j', async encoding => {
    const ctx = setup(encoding)
    expect(ctx.receive('café')).toBe('café')
    await ctx.clipboard.publish('café')
    expect(ctx.rfb.clipboardPasteFrom).toHaveBeenCalledWith('café')
    expect(ctx.encode).not.toHaveBeenCalled()
  })

  it('automatically uses Unicode when negotiated and keeps Latin-1 as the fallback', async () => {
    const ctx = setup('auto')
    ctx.unicode()
    expect(ctx.receive('中文😀', true)).toBe('中文😀')
    await ctx.clipboard.publish('中文😀')
    expect(ctx.rfb.clipboardPasteFrom).toHaveBeenCalledWith('中文😀')
    expect(ctx.encode).not.toHaveBeenCalled()
    expect(ctx.receive('café')).toBe('café')
  })

  it('supports UTF-8 bytes on legacy servers configured for UTF-8', async () => {
    const ctx = setup('utf-8')
    const value = '中文😀\ntext'
    const binary = String.fromCharCode(...new TextEncoder().encode(value))
    expect(ctx.receive(binary)).toBe(value)
    await ctx.clipboard.publish(value)
    expect(ctx.rfb.clipboardPasteFrom).toHaveBeenCalledWith(binary)
    expect(ctx.encode).not.toHaveBeenCalled()
  })

  it('handles large UTF-8 clipboard contents without overflowing the call stack', async () => {
    const ctx = setup('utf-8')
    const value = '中文😀'.repeat(30000)
    await ctx.clipboard.publish(value)
    const sent = ctx.rfb.clipboardPasteFrom.mock.calls[0]![0] as string
    expect(ctx.receive(sent)).toBe(value)
  })

  it('uses Unicode if negotiation completes during GBK conversion', async () => {
    const ctx = setup()
    const sending = ctx.clipboard.publish(text)
    ctx.unicode()
    await sending
    expect(ctx.rfb.clipboardPasteFrom).toHaveBeenCalledWith(text)
  })

  it.each(['blur', 'dispose'])('drops pending conversions after %s', async action => {
    const ctx = setup()
    const sending = ctx.clipboard.publish(text)
    if (action === 'blur') ctx.active.mockReturnValue(false)
    else ctx.clipboard.dispose()
    await sending
    expect(ctx.rfb.clipboardPasteFrom).not.toHaveBeenCalled()
  })

  it('preserves partial message handling and restores the cached receiver', () => {
    const ctx = setup()
    ctx.partial()
    expect(ctx.rfb._handleServerCutText()).toBe(false)
    expect(ctx.receive(legacy)).toBe(text)
    ctx.clipboard.dispose()
    expect(ctx.rfb._handleServerCutText).toBe(ctx.original)
  })

  it('does not echo decoded remote text into the legacy channel', async () => {
    const ctx = setup()
    const write = vi.fn(async () => true)
    const sync = new RdpClipboardSync({
      read: async () => ({ kind: 'text', text }),
      publish: async content => { if (content.kind === 'text') await ctx.clipboard.publish(content.text) },
      writeText: write,
    }, true)
    await sync.receiveText(ctx.receive(legacy))
    await sync.sync()
    expect(write).toHaveBeenCalledWith(text)
    expect(ctx.rfb.clipboardPasteFrom).not.toHaveBeenCalled()
  })

  it('rejects invalid byte sequences without changing the clipboard or receive hook', () => {
    const ctx = setup('utf-8')
    expect(() => ctx.receive('\xff')).toThrow()
    expect(ctx.receive('valid')).toBe('valid')
    expect(ctx.clipboard.decode('中文')).toBe('中文')
  })
})
