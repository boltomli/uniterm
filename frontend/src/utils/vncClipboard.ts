type ClipboardRfb = {
  clipboardPasteFrom(text: string): void
  _clipboardServerCapabilitiesFormats: Record<number, boolean>
  _clipboardServerCapabilitiesActions: Record<number, boolean>
  _sock: { rQpeekBytes(length: number, copy?: boolean): Uint8Array }
  _handleServerCutText(): boolean
}

export function createVncClipboard(
  rfb: ClipboardRfb,
  encoding: string | undefined,
  encodeText: (text: string, charset: string) => Promise<string>,
  canPublish: () => boolean,
) {
  const charset = encoding && ['gbk', 'gb2312', 'gb18030', 'big5', 'shift-jis', 'euc-jp', 'euc-kr', 'utf-8'].includes(encoding) ? encoding : 'latin1'
  const original = rfb._handleServerCutText
  let legacyIncoming = false
  let disposed = false
  const handler = function () {
    const header = rfb._sock.rQpeekBytes(7, false)
    legacyIncoming = header.length === 7 && (header[3]! & 0x80) === 0
    try {
      return original.call(rfb)
    } finally {
      legacyIncoming = false
    }
  }
  rfb._handleServerCutText = handler

  const usesUnicodeExtension = () => rfb._clipboardServerCapabilitiesFormats[1] &&
    rfb._clipboardServerCapabilitiesActions[1 << 27]

  return {
    async publish(text: string) {
      if (disposed || !canPublish()) return
      if (charset === 'latin1' || usesUnicodeExtension()) {
        rfb.clipboardPasteFrom(text)
        return
      }
      let binary: string
      if (charset !== 'utf-8') {
        binary = atob(await encodeText(text, charset))
      } else {
        const bytes = new TextEncoder().encode(text)
        const chunks: string[] = []
        for (let i = 0; i < bytes.length; i += 0x2000) {
          chunks.push(String.fromCharCode(...bytes.subarray(i, i + 0x2000)))
        }
        binary = chunks.join('')
      }
      if (!disposed && canPublish()) rfb.clipboardPasteFrom(usesUnicodeExtension() ? text : binary)
    },
    decode(text: string): string {
      if (!legacyIncoming || charset === 'latin1') return text
      const bytes = Uint8Array.from(text, char => char.charCodeAt(0))
      return new TextDecoder(charset, { fatal: true }).decode(bytes)
    },
    dispose() {
      disposed = true
      if (rfb._handleServerCutText === handler) rfb._handleServerCutText = original
    },
  }
}
