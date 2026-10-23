import { describe, expect, it, vi } from 'vitest'
import { SpiceKeyboard } from './spiceKeyboard'

function setup() {
  const packets: number[][] = []
  const send = vi.fn((packet: ArrayBuffer) => { packets.push(Array.from(new Uint8Array(packet))); return true })
  return { keyboard: new SpiceKeyboard(send), packets, send }
}

describe('SPICE keyboard packets', () => {
  it('encodes Alt+Tab make and break codes', () => {
    const { keyboard, packets } = setup()
    keyboard.key(0x38, true)
    keyboard.key(0x0f, true)
    keyboard.key(0x0f, false)
    keyboard.key(0x38, false)
    expect(packets).toEqual([
      [101, 0, 4, 0, 0, 0, 0x38, 0, 0, 0],
      [101, 0, 4, 0, 0, 0, 0x0f, 0, 0, 0],
      [102, 0, 4, 0, 0, 0, 0x8f, 0, 0, 0],
      [102, 0, 4, 0, 0, 0, 0xb8, 0, 0, 0],
    ])
  })

  it.each([0x5b, 0x5c, 0x1d, 0x38, 0x48, 0x53])('encodes E0-prefixed key %s in wire byte order', code => {
    const { keyboard, packets } = setup()
    keyboard.key(0xe000 | code, true)
    keyboard.key(0xe000 | code, false)
    expect(packets).toEqual([
      [101, 0, 4, 0, 0, 0, 0xe0, code, 0, 0],
      [102, 0, 4, 0, 0, 0, 0xe0, code | 0x80, 0, 0],
    ])
  })

  it('preserves repeat presses and releases held keys once in reverse order', () => {
    const { keyboard, packets } = setup()
    keyboard.key(0xe05b, true)
    keyboard.key(0x12, true)
    keyboard.key(0x12, true)
    expect(packets[1]).toEqual(packets[2])
    packets.length = 0
    keyboard.release()
    keyboard.release()
    expect(packets).toEqual([
      [102, 0, 4, 0, 0, 0, 0x92, 0, 0, 0],
      [102, 0, 4, 0, 0, 0, 0xe0, 0xdb, 0, 0],
    ])
  })

  it('ignores F12, unknown scans and releases from an old focus owner', () => {
    const { keyboard, send } = setup()
    expect(keyboard.key(0x58, true)).toBe(false)
    expect(keyboard.key(0xffff, true)).toBe(false)
    keyboard.key(0xe05b, false)
    keyboard.release()
    expect(send).not.toHaveBeenCalled()
  })

  it('does not release keys that could not be sent before the inputs channel was ready', () => {
    const { keyboard, send } = setup()
    send.mockReturnValue(false)
    keyboard.key(0x38, true)
    keyboard.release()
    expect(send).toHaveBeenCalledOnce()
    send.mockReturnValue(true)
    keyboard.key(0x38, true)
    keyboard.release()
    expect(send).toHaveBeenCalledTimes(3)
  })

  it('pastes without releasing a Control key that the user is still holding', () => {
    const { keyboard, packets } = setup()
    keyboard.key(0xe01d, true)
    packets.length = 0
    keyboard.paste()
    expect(packets).toEqual([
      [101, 0, 4, 0, 0, 0, 0x2f, 0, 0, 0],
      [102, 0, 4, 0, 0, 0, 0xaf, 0, 0, 0],
    ])
    keyboard.release()
    expect(packets.at(-1)).toEqual([102, 0, 4, 0, 0, 0, 0xe0, 0x9d, 0, 0])
  })
})
