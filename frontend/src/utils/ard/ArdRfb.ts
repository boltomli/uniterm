/*
 * Apple RFB adapter for noVNC 1.7. Protocol behavior adapted from noVNC-ARD,
 * peetinc, d699293635083c9e0c30c8f9480993865653772a (MPL-2.0).
 * https://github.com/peetinc/noVNC-ARD
 * This Source Code Form is subject to the Mozilla Public License, v. 2.0.
 * If a copy was not distributed with this file, obtain it at
 * https://mozilla.org/MPL/2.0/.
 */
import RFB from '@novnc/novnc'
import legacyCrypto from '@novnc-internals/crypto/crypto.js'
import { RSACipher } from '@novnc-internals/crypto/rsa.js'
import Inflator from '@novnc-internals/inflator.js'
import Deflator from '@novnc-internals/deflator.js'
import { clientToElement } from '@novnc-internals/util/element.js'
import type { DesktopDisplay } from '../vncDisplays'
import {
  appleCredentials, appleKeysym, appleSessionCommand, appleSetDisplay, appleViewerInfo,
  buildApplePasteboard, parseAppleDisplays, parseApplePasteboard, parseAppleRsaKey,
} from './protocol'

const MAX_PAYLOAD = 16 * 1024 * 1024
const messages = RFB.messages
// AutoFBUpdate owns pacing for Apple connections. Standard VNC connections
// continue to use noVNC's usual incremental update requests.
const originalUpdateRequest = messages.fbUpdateRequest
messages.fbUpdateRequest = (sock: any, incremental: boolean, ...args: number[]) => {
  if (incremental && sock._unitermApplePush) return
  return originalUpdateRequest(sock, incremental, ...args)
}

export default class ArdRfb extends RFB {
  [key: string]: any

  _negotiateProtocolVersion() {
    if (this._sock.rQwait('ARD protocol version', 12)) return false
    const banner = new TextDecoder().decode(this._sock.rQpeekBytes(12))
    if (banner !== 'RFB 003.889\n') return this._fail('The server does not offer Apple Remote Desktop')
    this._sock.rQskipBytes(12)
    this._rfbVersion = 3.8
    this._sock._unitermApplePush = true
    this._ardDisplays = []
    this._ardSelected = null
    this._ardClipboardSession = 0
    this._ardKeyQueue = Promise.resolve()
    this._ardRsaStage = 0
    this._sock.sQpushString('RFB 003.889\n')
    this._sock.flush()
    this._rfbInitState = 'Security'
    return true
  }

  _negotiateSecurity() {
    const sock = this._sock
    if (sock.rQwait('ARD security types', 1)) return false
    const count = sock.rQpeek8()
    if (sock.rQwait('ARD security types', 1 + count)) return false
    sock.rQskipBytes(1)
    if (count === 0) {
      this._rfbInitState = 'SecurityReason'
      this._securityContext = 'ARD security'
      this._securityStatus = 1
      return true
    }
    const types: Uint8Array = sock.rQshiftBytes(count)
    const chosen = [33, 30, 2].find(type => types.includes(type))
    if (chosen === undefined) return this._fail('No supported ARD authentication method')
    this._rfbAuthScheme = chosen
    sock.sQpush8(chosen)
    // RSATunnel's first request is flushed together with its type byte.
    if (chosen !== 33) sock.flush()
    this._rfbInitState = 'Authentication'
    return true
  }

  _negotiateAuthentication() {
    return this._rfbAuthScheme === 33 ? this._negotiateRsa() : super._negotiateAuthentication()
  }

  _handleSecurityResult() {
    if (this._sock.rQwait('ARD authentication result', 4)) return false
    const status = this._sock.rQshift32()
    if (status === 0) {
      this._rfbInitState = 'ClientInitialisation'
      return true
    }
    // Apple's failure does not include the standard RFB 3.8 reason string.
    // Waiting for one would leave an incorrect password stuck on Connecting.
    this.dispatchEvent(new CustomEvent('securityfailure', { detail: { status, reason: `ARD authentication failed (${status})` } }))
    return this._fail(`ARD authentication failed (${status})`)
  }

  async _negotiateARDAuthAsync(length: number, publicKey: Uint8Array, clientKey: any) {
    try {
      const shared = legacyCrypto.deriveBits({ name: 'DH', public: publicKey }, clientKey.privateKey, length * 8)
      const key = await legacyCrypto.digest('MD5', shared)
      this._ardAuthCipher = await legacyCrypto.importKey('raw', key, { name: 'AES-ECB' }, false, ['encrypt'])
      const credentials = appleCredentials(this._rfbCredentials.username, this._rfbCredentials.password)
      const encrypted = await this._ardAuthCipher.encrypt({}, credentials)
      if (this._rfbConnectionState !== 'connecting') return
      this._rfbCredentials.ardCredentials = encrypted
      this._rfbCredentials.ardPublicKey = legacyCrypto.exportKey('raw', clientKey.publicKey)
      this._resumeAuthentication()
    } catch (error) { this._fail(`ARD authentication failed: ${String(error)}`) }
  }

  _negotiateRsa(): boolean {
    const sock = this._sock
    switch (this._ardRsaStage) {
      case 0:
        sock.sQpush32(10)
        sock.sQpushBytes(new Uint8Array([1, 0, 0x52, 0x53, 0x41, 0x31, 0, 0, 0, 0]))
        sock.flush()
        this._ardRsaStage = 1
        return false
      case 1: {
        const payload = this._peekSized32('ARD RSA key')
        if (!payload) return false
        if (payload.length < 6) return this._fail('Invalid ARD RSA key response')
        const data = new DataView(payload.buffer, payload.byteOffset, payload.byteLength)
        const length = data.getUint16(4)
        if (6 + length > payload.length) return this._fail('Incomplete ARD RSA key')
        try { this._ardRsaKey = parseAppleRsaKey(payload.subarray(6, 6 + length)) }
        catch (error) { return this._fail(String(error)) }
        sock.rQskipBytes(4 + payload.length)
        this._ardRsaStage = 2
        return this._negotiateRsa()
      }
      case 2:
        if (this._rfbCredentials.username === undefined || this._rfbCredentials.password === undefined) {
          this.dispatchEvent(new CustomEvent('credentialsrequired', { detail: { types: ['username', 'password'] } }))
          return false
        }
        this._ardRsaStage = 3
        void this._prepareRsaCredentials()
        return false
      case 4:
        sock.sQpush32(138 + this._ardRsaEncryptedKey.length)
        sock.sQpushBytes(new Uint8Array([1, 0, 0x52, 0x53, 0x41, 0x31, 0, 1]))
        sock.sQpushBytes(this._ardRsaCredentials)
        sock.sQpush8(this._ardRsaEncryptedKey.length & 255)
        sock.sQpush8(this._ardRsaEncryptedKey.length >>> 8)
        sock.sQpushBytes(this._ardRsaEncryptedKey)
        sock.flush()
        this._ardRsaCredentials = null
        this._ardRsaEncryptedKey = null
        this._ardRsaStage = 5
        return false
      case 5: {
        const payload = this._peekSized32('ARD RSA response')
        if (!payload) return false
        sock.rQskipBytes(4 + payload.length)
        this._rfbInitState = 'SecurityResult'
        return true
      }
      default: return false
    }
  }

  async _prepareRsaCredentials() {
    try {
      const key = crypto.getRandomValues(new Uint8Array(16))
      this._ardAuthCipher = await legacyCrypto.importKey('raw', key, { name: 'AES-ECB' }, false, ['encrypt'])
      this._ardRsaCredentials = await this._ardAuthCipher.encrypt({}, appleCredentials(this._rfbCredentials.username, this._rfbCredentials.password))
      const rsa = await RSACipher.importKey(this._ardRsaKey, { name: 'RSA-PKCS1-v1_5' }, false, ['encrypt'])
      this._ardRsaEncryptedKey = await rsa.encrypt({}, key)
      if (this._rfbConnectionState !== 'connecting') return
      this._ardRsaStage = 4
      this._resumeAuthentication()
    } catch (error) { this._fail(`ARD authentication failed: ${String(error)}`) }
  }

  _peekSized32(label: string): Uint8Array | null {
    if (this._sock.rQwait(label, 4)) return null
    const header: Uint8Array = this._sock.rQpeekBytes(4)
    const length = new DataView(header.buffer, header.byteOffset, 4).getUint32(0)
    if (length > MAX_PAYLOAD) { this._fail(`${label} exceeds size limit`); return null }
    if (this._sock.rQwait(label, 4 + length)) return null
    return this._sock.rQpeekBytes(4 + length).subarray(4)
  }

  _initMsg() {
    if (this._rfbInitState === 'ClientInitialisation') {
      this._sock.sQpush8(0xc0 | (this._shared ? 1 : 0))
      this._sock.flush()
      this._rfbInitState = 'ServerInitialisation'
      return true
    }
    if (this._rfbInitState === 'ArdSessionInfo' || this._rfbInitState === 'ArdSessionResult') return this._sessionSelect()
    return super._initMsg()
  }

  _negotiateServerInit() {
    const sock = this._sock
    if (sock.rQwait('ARD ServerInit', 24)) return false
    const header: Uint8Array = sock.rQpeekBytes(24)
    const data = new DataView(header.buffer, header.byteOffset, 24)
    const length = data.getUint32(20)
    if (length > MAX_PAYLOAD) return this._fail('ARD desktop name exceeds size limit')
    if (sock.rQwait('ARD ServerInit', 24 + length)) return false
    sock.rQskipBytes(24)
    const name: Uint8Array = sock.rQshiftBytes(length)
    let sessionSelect = false
    let desktopName = name
    if (name.length >= 22 && name[0] === 0) {
      const flags = new DataView(name.buffer, name.byteOffset, name.byteLength).getUint32(2)
      sessionSelect = !!(flags & 4)
      desktopName = name.subarray(name.lastIndexOf(0) + 1)
    }
    this._setDesktopName(new TextDecoder().decode(desktopName))
    this._resize(data.getUint16(0), data.getUint16(2))
    this._fbDepth = 24
    if (!this._viewOnly) this._keyboard.grab()
    if (sessionSelect) this._rfbInitState = 'ArdSessionInfo'
    else this._startDesktop()
    return true
  }

  _sessionSelect() {
    const sock = this._sock
    if (sock.rQwait('ARD session header', 2)) return false
    const header: Uint8Array = sock.rQpeekBytes(2)
    const size = (header[0]! << 8) | header[1]!
    if (sock.rQwait('ARD session', size + 2)) return false
    sock.rQskipBytes(2)
    const bytes: Uint8Array = sock.rQshiftBytes(size)
    const data = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
    if (this._rfbInitState === 'ArdSessionInfo') {
      if (size < 10) return this._fail('Invalid ARD session information')
      const username = new TextDecoder().decode(bytes.subarray(10)).split('\0')[0]!
      sock.sQpushBytes(appleSessionCommand(username, data.getUint32(2)))
      sock.flush()
      this._rfbInitState = 'ArdSessionResult'
    } else {
      if (size < 6) return this._fail('Invalid ARD session result')
      const status = data.getUint32(2)
      if (status === 0 || status === 4) this._startDesktop()
      else if (status !== 2 && status !== 3) return this._fail(`ARD session denied (${status})`)
    }
    return true
  }

  _startDesktop() {
    const sock = this._sock
    sock.sQpushBytes(appleViewerInfo())
    sock.sQpushBytes(new Uint8Array([0x0a, 0, 0, this._viewOnly ? 0 : 1]))
    sock.sQpushBytes(appleSetDisplay(null))
    sock.sQpushBytes(new Uint8Array([0x15, 0, 0, 1]))
    this._sendEncodings()
    messages.pixelFormat(sock, 24, true)
    this._sendEncodings()
    this._updateConnectionState('connected')
    this._requestAppleUpdate()
  }

  _sendEncodings() {
    // Standard lossless decoders plus Apple display metadata. Advertising only
    // implemented encodings avoids pulling in Apple's MVS/video transports.
    messages.clientEncodings(this._sock, [16, 6, 0, 1, 5, 2, 1101, 1105, -223, -224, -239])
  }

  _requestAppleUpdate() {
    if (this._fbWidth > 0 && this._fbHeight > 0) {
      messages.fbUpdateRequest(this._sock, false, 0, 0, this._fbWidth, this._fbHeight)
      this._autoUpdates()
    }
  }

  _autoUpdates() {
    if (this._fbWidth <= 0 || this._fbHeight <= 0) return
    const sock = this._sock
    sock.sQpushBytes(new Uint8Array([9, 0, 0, 1, 0, 0, 0, 0]))
    sock.sQpush16(0); sock.sQpush16(0)
    sock.sQpush16(this._fbWidth); sock.sQpush16(this._fbHeight)
    sock.flush()
  }

  ardSelectDisplay(id: number | null) {
    if (this._rfbConnectionState !== 'connected') return
    if (id !== null && !this._ardDisplays.some((d: DesktopDisplay) => d.id === id)) return
    this._ardRequested = id
    this._sock.sQpushBytes(appleSetDisplay(id))
    this._sock.flush()
  }

  ardSetControl(enabled: boolean) {
    if (this._rfbConnectionState !== 'connected') return
    this._sock.sQpushBytes(new Uint8Array([0x0a, 0, 0, enabled ? 1 : 0]))
    this._sock.flush()
  }

  _publishDisplays(displays: DesktopDisplay[], selected: number | null) {
    const preferred = this._unitermView?.selected
    if (displays.length) this._ardDisplays = displays
    this._ardSelected = selected
    this.dispatchEvent(new CustomEvent('arddisplayinfo', { detail: { displays: this._ardDisplays, selected } }))
    // Saved IDs are a preference only; layouts can change across unlocks.
    if (!this._ardInitialSelection && this._ardDisplays.length > 0) {
      this._ardInitialSelection = true
      if (preferred?.startsWith('screen:')) this.ardSelectDisplay(Number(preferred.slice(7)))
    }
    if (this._ardRequested !== undefined && selected !== this._ardRequested) {
      if (this._ardRequested !== null && !this._ardDisplays.some((d: DesktopDisplay) => d.id === this._ardRequested)) this.ardSelectDisplay(null)
    } else this._ardRequested = undefined
  }

  _appleDisplayInfo2(rectWidth = 0, rectHeight = 0) {
    const sock = this._sock
    if (sock.rQwait('ARD display header', 2)) return false
    const header: Uint8Array = sock.rQpeekBytes(2)
    const size = (header[0]! << 8) | header[1]!
    if (sock.rQwait('ARD display list', 2 + size)) return false
    sock.rQskipBytes(2)
    const bytes: Uint8Array = sock.rQshiftBytes(size)
    try {
      const info = parseAppleDisplays(bytes)
      const width = rectWidth || info.width, height = rectHeight || info.height
      if (width > 0 && height > 0) this._resize(width, height)
      this._publishDisplays(info.displays, info.selected)
      this._requestAppleUpdate()
    } catch (error) { return this._fail(String(error)) }
    return true
  }

  _handleRect() {
    if (this._FBU.encoding === 1105) return this._appleDisplayInfo2(this._FBU.width, this._FBU.height)
    if (this._FBU.encoding === 1101) {
      const sock = this._sock
      if (sock.rQwait('ARD display information', 8)) return false
      const header: Uint8Array = sock.rQpeekBytes(8)
      const data = new DataView(header.buffer, header.byteOffset, 8)
      const count = data.getUint16(4)
      if (sock.rQwait('ARD display information', 8 + count * 28)) return false
      sock.rQskipBytes(8)
      const displays: DesktopDisplay[] = []
      for (let i = 0; i < count; i++) {
        const id = sock.rQshift32(), width = sock.rQshift16(), height = sock.rQshift16()
        sock.rQskipBytes(20)
        displays.push({ id, width, height, x: 0, y: 0 })
      }
      if (data.getUint16(0) > 0 && data.getUint16(2) > 0) this._resize(data.getUint16(0), data.getUint16(2))
      if (!this._ardDisplays.length) this._publishDisplays(displays, null)
      this._requestAppleUpdate()
      return true
    }
    return super._handleRect()
  }

  _normalMsg() {
    const sock = this._sock
    // Leave a partial message untouched until its entire payload is available.
    if (this._FBU.rects !== 0) return super._normalMsg()
    if (sock.rQwait('ARD message', 1)) return false
    const type = sock.rQpeek8()
    if (type === 4 || type === 7) { sock.rQskipBytes(1); return true }
    if (type === 0x51) {
      if (sock.rQwait('ARD display message', 3)) return false
      const header: Uint8Array = sock.rQpeekBytes(3)
      const length = (header[1]! << 8) | header[2]!
      if (sock.rQwait('ARD display message', 3 + length)) return false
      sock.rQskipBytes(1)
      return this._appleDisplayInfo2()
    }
    if (type === 0x14) {
      if (sock.rQwait('ARD state', 4)) return false
      const header: Uint8Array = sock.rQpeekBytes(4)
      const size = (header[2]! << 8) | header[3]!
      if (sock.rQwait('ARD state', 4 + size)) return false
      sock.rQskipBytes(4)
      const bytes: Uint8Array = sock.rQshiftBytes(size)
      if (size < 4) return this._fail('Invalid ARD state message')
      const status = new DataView(bytes.buffer, bytes.byteOffset, size).getUint16(2)
      if (status === 1) this.disconnect()
      else if (status === 2 && !this._viewOnly) this._requestClipboard()
      else if (status === 3 && this._ardClipboardText !== undefined) this.clipboardPasteFrom(this._ardClipboardText)
      else if (status === 4) this._autoUpdates()
      return true
    }
    if (type === 0x1f) return this._receiveClipboard()
    if (type === 0x20) {
      if (sock.rQwait('ARD drag', 9)) return false
      const header: Uint8Array = sock.rQpeekBytes(9)
      const size = new DataView(header.buffer, header.byteOffset, 9).getUint32(5)
      if (size > MAX_PAYLOAD) return this._fail('ARD drag message exceeds size limit')
      if (sock.rQwait('ARD drag', 9 + size)) return false
      sock.rQskipBytes(9 + size)
      return true
    }
    return super._normalMsg()
  }

  sendKey(keysym: number, code: string, down?: boolean) {
    if (this._rfbConnectionState !== 'connected' || this._viewOnly) return
    if (down === undefined) { this.sendKey(keysym, code, true); this.sendKey(keysym, code, false); return }
    keysym = appleKeysym(keysym)
    if (!this._ardAuthCipher) return super.sendKey(keysym, code, down)
    const payload = new Uint8Array(16)
    payload[0] = 255; payload[1] = down ? 1 : 0
    const data = new DataView(payload.buffer)
    data.setUint32(2, keysym)
    data.setUint32(6, (performance.now() * 1000) >>> 0)
    this._ardKeyQueue = this._ardKeyQueue.then(async () => {
      const encrypted = await this._ardAuthCipher.encrypt({}, payload)
      if (this._rfbConnectionState !== 'connected') return
      this._sock.sQpushBytes(new Uint8Array([0x10, 0]))
      this._sock.sQpushBytes(encrypted)
      this._sock.flush()
    }).catch((error: unknown) => this._fail(`ARD key event failed: ${String(error)}`))
  }

  _sendMouse(x: number, y: number, mask: number) {
    return super._sendMouse(x, y, (mask & ~6) | ((mask & 4) >> 1) | ((mask & 2) << 1))
  }

  _handleWheel(event: WheelEvent) {
    if (this._rfbConnectionState !== 'connected' || this._viewOnly) return
    event.preventDefault(); event.stopPropagation()
    const position = clientToElement(event.clientX, event.clientY, this._canvas)
    const unit = event.deltaMode === 1 ? 19 : event.deltaMode === 2 ? 456 : 1
    const x = event.deltaX * unit / 45, y = event.deltaY * unit / 45
    const bytes = new Uint8Array(36), data = new DataView(bytes.buffer)
    bytes[0] = 0x17
    data.setUint16(2, 32); data.setUint16(4, 2); data.setUint16(6, 8)
    data.setFloat32(8, x); data.setFloat32(12, y); data.setUint32(20, 1)
    data.setUint16(32, this._display.absX(position.x)); data.setUint16(34, this._display.absY(position.y))
    this._sock.sQpushBytes(bytes)
    // Apple viewers also send the detailed wheel event, for servers that do
    // not consume the compact gesture alone.
    const detailed = new Uint8Array(58), wheel = new DataView(detailed.buffer)
    detailed[0] = 0x17
    wheel.setUint16(2, 54); wheel.setUint16(4, 1); wheel.setUint16(6, 11)
    const step = (value: number) => value === 0 ? 0 : Math.sign(value) * Math.max(1, Math.round(Math.abs(value)))
    wheel.setInt16(8, step(x)); wheel.setInt16(10, step(y))
    wheel.setInt32(14, Math.round(x * 65536)); wheel.setInt32(18, Math.round(y * 65536))
    wheel.setInt32(26, Math.round(event.deltaX * unit)); wheel.setInt32(30, Math.round(event.deltaY * unit))
    wheel.setUint16(54, this._display.absX(position.x)); wheel.setUint16(56, this._display.absY(position.y))
    this._sock.sQpushBytes(detailed)
    this._sock.flush()
  }

  clipboardPasteFrom(text: string) {
    if (this._rfbConnectionState !== 'connected' || this._viewOnly) return
    const bytes = buildApplePasteboard(text)
    if (bytes.length > MAX_PAYLOAD) return
    this._ardClipboardText = text
    const compressed = new Deflator().deflate(bytes)
    const sock = this._sock
    sock.sQpushBytes(new Uint8Array([0x1f, 0, 0, 0]))
    sock.sQpush32(this._ardClipboardSession)
    sock.sQpush32(bytes.length); sock.sQpush32(compressed.length)
    sock.sQpushBytes(compressed)
    sock.flush()
  }

  _requestClipboard() {
    this._sock.sQpushBytes(new Uint8Array([0x0b, 0, 0, 0]))
    this._sock.sQpush32(this._ardClipboardSession)
    this._sock.flush()
  }

  _receiveClipboard() {
    const sock = this._sock
    if (sock.rQwait('ARD clipboard', 16)) return false
    const bytes: Uint8Array = sock.rQpeekBytes(16)
    const data = new DataView(bytes.buffer, bytes.byteOffset, 16)
    const uncompressed = data.getUint32(8), compressed = data.getUint32(12)
    if (uncompressed > MAX_PAYLOAD || compressed > MAX_PAYLOAD) return this._fail('ARD clipboard exceeds size limit')
    if (sock.rQwait('ARD clipboard', 16 + compressed)) return false
    this._ardClipboardSession = data.getUint32(4)
    sock.rQskipBytes(16)
    const payload: Uint8Array = sock.rQshiftBytes(compressed)
    if (compressed === 0 || this._viewOnly) return true
    try {
      const inflator = new Inflator()
      inflator.setInput(payload)
      const text = parseApplePasteboard(inflator.inflate(uncompressed))
      if (text !== null) this.dispatchEvent(new CustomEvent('clipboard', { detail: { text } }))
    } catch (error) { console.warn('Invalid ARD clipboard', error) }
    return true
  }
}
