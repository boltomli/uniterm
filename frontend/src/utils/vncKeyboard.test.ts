import { describe, expect, it, vi } from 'vitest'
import { VncNativeKeyboard } from './vncKeyboard'
import type { NativeRdpKey } from './remoteKeyboard'

function key(scancode: number, down: boolean, extra: Partial<NativeRdpKey> = {}): NativeRdpKey {
  return { captureID: 'vnc', scancode, down, ctrl: false, shift: false, meta: false, repeat: false, reset: false, ...extra }
}

describe('VNC native shortcut translation', () => {
  it('sends Alt+Tab and both Windows keys using RFB keysyms', () => {
    const send = vi.fn()
    const keyboard = new VncNativeKeyboard(send)
    for (const scan of [0x38, 0x0f, 0xe05b, 0xe05c]) keyboard.key(key(scan, true))
    expect(send.mock.calls).toEqual([
      [0xffe9, 'AltLeft', true], [0xff09, 'Tab', true],
      [0xffeb, 'MetaLeft', true], [0xffec, 'MetaRight', true],
    ])
  })

  it('keeps repeated keys and their releases paired across modifier changes', () => {
    const send = vi.fn()
    const keyboard = new VncNativeKeyboard(send)
    keyboard.key(key(0xe05b, true))
    keyboard.key(key(0x12, true, { virtualKey: 0x45, shift: true }))
    keyboard.key(key(0x12, true, { virtualKey: 0x45, repeat: true }))
    keyboard.key(key(0xe05b, false))
    keyboard.key(key(0x12, false))
    expect(send.mock.calls.slice(1)).toEqual([
      [0x45, 'KeyE', true], [0x45, 'KeyE', true], [0xffeb, 'MetaLeft', false], [0x45, 'KeyE', false],
    ])
  })

  it('releases held remote shortcut keys once when focus leaves', () => {
    const send = vi.fn()
    const keyboard = new VncNativeKeyboard(send)
    keyboard.key(key(0x38, true))
    keyboard.key(key(0x3e, true))
    send.mockClear()
    keyboard.release()
    keyboard.release()
    expect(send.mock.calls).toEqual([[0xffc1, 'F4', false], [0xffe9, 'AltLeft', false]])
  })

  it('ignores unsupported scan codes', () => {
    const send = vi.fn()
    const keyboard = new VncNativeKeyboard(send)
    keyboard.key(key(0, true))
    keyboard.release()
    expect(send).not.toHaveBeenCalled()
  })
})
