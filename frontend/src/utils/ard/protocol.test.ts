import { describe, expect, it } from 'vitest'
import { appleCredentials, appleKeysym, appleSessionCommand, appleSetDisplay, appleViewerInfo, buildApplePasteboard, parseAppleDisplays, parseApplePasteboard, parseAppleRsaKey } from './protocol'

export function displayInfo() {
  const bytes = new Uint8Array(132), data = new DataView(bytes.buffer)
  data.setUint16(0, 2); data.setUint16(6, 4480); data.setUint16(8, 1440)
  data.setUint32(10, 0xffffffff); data.setUint16(18, 2)
  for (const [i, id, left, right, bottom, primary] of [[0, 9, 0, 1920, 1080, 1], [1, 42, 1920, 4480, 1440, 0]]) {
    const at = 20 + i * 56
    data.setFloat64(at, i === 0 ? 2 : 1)
    data.setUint32(at + 16, id)
    data.setInt16(at + 30, left); data.setInt16(at + 34, right); data.setInt16(at + 32, bottom)
    data.setUint32(at + 36, primary)
  }
  return bytes
}

describe('Apple RFB wire format', () => {
  it('converts legacy Cyrillic/Greek characters without changing shortcuts', () => {
    expect(appleKeysym(0x6d0)).toBe(0x0100043f)
    expect(appleKeysym(0x7c3)).toBe(0x01000393)
    expect(appleKeysym(0xffe3)).toBe(0xffe3)
    expect(appleKeysym(0x61)).toBe(0x61)
  })
  it('reads framebuffer pixel bounds independently of Retina scale', () => {
    const info = parseAppleDisplays(displayInfo())
    expect(info.selected).toBeNull()
    expect(info.displays).toEqual([
      { id: 9, primary: true, x: 0, y: 0, width: 1920, height: 1080 },
      { id: 42, primary: false, x: 1920, y: 0, width: 2560, height: 1440 },
    ])
    expect(() => parseAppleDisplays(displayInfo().subarray(0, 131))).toThrow('Incomplete')
  })
  it('encodes display IDs as big-endian and all displays as a separate flag', () => {
    expect([...appleSetDisplay(0x12345678)]).toEqual([13, 0, 0, 0, 0x12, 0x34, 0x56, 0x78])
    expect([...appleSetDisplay(null)]).toEqual([13, 1, 0, 0, 0, 0, 0, 0])
    expect(appleViewerInfo()).toHaveLength(66)
  })
  it('shares the console and requests it when no console is logged in', () => {
    const loggedIn = appleSessionCommand('alice', 2)
    expect(loggedIn[8]).toBe(1)
    expect(new TextDecoder().decode(loggedIn.subarray(10, 15))).toBe('alice')
    expect(appleSessionCommand('', 0)[8]).toBe(0)
    expect(appleSessionCommand('', 2)[8]).toBe(1)
  })
  it('round-trips Unicode and empty clipboards and rejects truncated data', () => {
    for (const text of ['', 'hello', '中文 👋\nsecond line']) {
      expect(parseApplePasteboard(buildApplePasteboard(text))).toBe(text)
    }
    expect(() => parseApplePasteboard(buildApplePasteboard('hello').subarray(0, 30))).toThrow()
  })
  it('uses separate terminated credential fields and rejects UTF-8 overflow', () => {
    const credentials = appleCredentials('用户', 'pass')
    expect(credentials).toHaveLength(128)
    expect(new TextDecoder().decode(credentials.subarray(0, 6))).toBe('用户')
    expect(credentials[6]).toBe(0)
    expect(new TextDecoder().decode(credentials.subarray(64, 68))).toBe('pass')
    expect(credentials[68]).toBe(0)
    expect(() => appleCredentials('中'.repeat(22), 'pass')).toThrow('63 UTF-8 bytes')
  })
  it.each([[], [0x30, 0x80], [0x30, 2, 2, 99], [0x30, 0x84, 255, 255, 255, 255]].map(bytes => ({ bytes })))('rejects malformed RSA keys $bytes', ({ bytes }) => {
    expect(() => parseAppleRsaKey(new Uint8Array(bytes))).toThrow()
  })
})
