import { beforeEach, describe, expect, it, vi } from 'vitest'
import { appleSetDisplay } from './protocol'

const { updateRequest, messages } = vi.hoisted(() => {
  const updateRequest = vi.fn()
  return { updateRequest, messages: { fbUpdateRequest: updateRequest, clientEncodings: vi.fn(), pixelFormat: vi.fn() } }
})
vi.mock('@novnc/novnc', () => ({ default: class {
  static messages = messages
  _normalMsg() { return true }
  _handleRect() { return true }
} }))
vi.mock('@novnc-internals/crypto/crypto.js', () => ({ default: {} }))
vi.mock('@novnc-internals/crypto/rsa.js', () => ({ RSACipher: {} }))
vi.mock('@novnc-internals/util/element.js', () => ({ clientToElement: vi.fn() }))

import ArdRfb from './ArdRfb'

class Socket {
  input = new Uint8Array()
  output: number[] = []
  consumed = 0
  flush = vi.fn()
  feed(bytes: number[] | Uint8Array) { this.input = new Uint8Array(bytes); this.consumed = 0 }
  rQwait(_label: string, length: number) { return this.input.length - this.consumed < length }
  rQpeek8() { return this.input[this.consumed] }
  rQpeekBytes(length: number) { return this.input.slice(this.consumed, this.consumed + length) }
  rQskipBytes(length: number) { this.consumed += length }
  rQshiftBytes(length: number) { const bytes = this.rQpeekBytes(length); this.rQskipBytes(length); return bytes }
  rQshift32() { const bytes = this.rQshiftBytes(4); return new DataView(bytes.buffer).getUint32(0) }
  sQpushBytes(bytes: Uint8Array) { this.output.push(...bytes) }
  sQpushString(text: string) { this.sQpushBytes(new TextEncoder().encode(text)) }
  sQpush8(value: number) { this.output.push(value & 255) }
  sQpush16(value: number) { this.sQpush8(value >>> 8); this.sQpush8(value) }
  sQpush32(value: number) { this.sQpush16(value >>> 16); this.sQpush16(value) }
}

function client() {
  const rfb: any = Object.create(ArdRfb.prototype)
  return Object.assign(rfb, {
    _sock: new Socket(), _FBU: { rects: 0 }, _fbWidth: 0, _fbHeight: 0, _shared: true,
    _rfbConnectionState: 'connecting', _fail: vi.fn(() => false), dispatchEvent: vi.fn(),
    _updateConnectionState: vi.fn(function (this: any, state: string) { this._rfbConnectionState = state }),
    _resize: vi.fn(function (this: any, width: number, height: number) { this._fbWidth = width; this._fbHeight = height }),
    _setDesktopName: vi.fn(), _keyboard: { grab: vi.fn() },
  })
}

beforeEach(() => vi.clearAllMocks())

describe('ARD connection and display lifecycle', () => {
  it('reports incorrect credentials without waiting for a nonexistent reason string', () => {
    const rfb = client()
    rfb._sock.feed([0, 0, 0, 1])
    expect(rfb._handleSecurityResult()).toBe(false)
    expect(rfb._fail).toHaveBeenCalledWith('ARD authentication failed (1)')
    expect(rfb.dispatchEvent.mock.calls[0][0].type).toBe('securityfailure')
    expect(rfb._rfbInitState).not.toBe('SecurityReason')
  })
  it('restores control after reconnecting in view-only mode', () => {
    const rfb = client()
    rfb._rfbConnectionState = 'connected'
    rfb.ardSetControl(false)
    rfb.ardSetControl(true)
    expect(rfb._sock.output).toEqual([10, 0, 0, 0, 10, 0, 0, 1])
  })
  it('negotiates Apple mode without consuming an incomplete banner', () => {
    const rfb = client()
    rfb._sock.feed(new TextEncoder().encode('RFB 003.'))
    expect(rfb._negotiateProtocolVersion()).toBe(false)
    expect(rfb._sock.consumed).toBe(0)
    rfb._sock.feed(new TextEncoder().encode('RFB 003.889\n'))
    expect(rfb._negotiateProtocolVersion()).toBe(true)
    expect(new TextDecoder().decode(new Uint8Array(rfb._sock.output))).toBe('RFB 003.889\n')
    expect(rfb._rfbInitState).toBe('Security')
    rfb._sock.feed([3, 2, 30, 33])
    expect(rfb._negotiateSecurity()).toBe(true)
    expect(rfb._rfbAuthScheme).toBe(33)
    expect(rfb._rfbInitState).toBe('Authentication')
  })

  it('waits for complete state messages and responds to tickles after screen unlock', () => {
    const rfb = client()
    rfb._fbWidth = 1920; rfb._fbHeight = 1080
    rfb._sock.feed([0x14, 0, 0, 4, 0])
    expect(rfb._normalMsg()).toBe(false)
    expect(rfb._sock.consumed).toBe(0)
    rfb._sock.feed([0x14, 0, 0, 4, 0, 0, 0, 4])
    expect(rfb._normalMsg()).toBe(true)
    expect(rfb._sock.consumed).toBe(8)
    expect(rfb._sock.output).toEqual([9, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 7, 128, 4, 56])
  })

  it('never requests a zero-size framebuffer and keeps standard VNC update pacing', () => {
    const rfb = client()
    rfb._requestAppleUpdate()
    expect(updateRequest).not.toHaveBeenCalled()
    messages.fbUpdateRequest({ _unitermApplePush: true }, true, 0, 0, 1, 1)
    const plainSocket = {}
    messages.fbUpdateRequest(plainSocket, true, 0, 0, 1, 1)
    expect(updateRequest).toHaveBeenCalledExactlyOnceWith(plainSocket, true, 0, 0, 1, 1)
    expect(rfb._sock.output).toEqual([])
  })

  it('sends a server-side display switch and rejects unknown display IDs', () => {
    const rfb = client()
    rfb._rfbConnectionState = 'connected'; rfb._ardDisplays = [{ id: 42 }]
    rfb.ardSelectDisplay(99)
    expect(rfb._sock.output).toEqual([])
    rfb.ardSelectDisplay(42)
    expect(rfb._sock.output).toEqual([...appleSetDisplay(42)])
  })

  it('restores a saved screen before publishing the initial all-screen layout', () => {
    const rfb = client()
    rfb._rfbConnectionState = 'connected'; rfb._unitermView = { selected: 'screen:42' }
    rfb.dispatchEvent = () => { rfb._unitermView = { selected: 'all' } }
    rfb._publishDisplays([{ id: 42 }], null)
    expect(rfb._sock.output).toEqual([...appleSetDisplay(42)])
  })

  it('preserves key ordering when encryption completes asynchronously', async () => {
    const rfb = client()
    rfb._rfbConnectionState = 'connected'; rfb._ardKeyQueue = Promise.resolve()
    const payloads: Uint8Array[] = []
    rfb._ardAuthCipher = { encrypt: async (_algorithm: unknown, payload: Uint8Array) => {
      payloads.push(payload); return new Uint8Array(16).fill(payload[1])
    } }
    rfb.sendKey(0xffe3, 'ControlLeft', true)
    rfb.sendKey(0xffe3, 'ControlLeft', false)
    await rfb._ardKeyQueue
    expect(payloads.map(payload => payload[1])).toEqual([1, 0])
    expect(rfb._sock.output).toEqual([16, 0, ...new Array(16).fill(1), 16, 0, ...new Array(16).fill(0)])
  })
})
