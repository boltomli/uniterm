import { REMOTE_KEY_SCANCODES } from './remoteKeyboard'

const supported = new Set(Object.values(REMOTE_KEY_SCANCODES))

export class SpiceKeyboard {
  private pressed = new Set<number>()

  constructor(private readonly send: (packet: ArrayBuffer) => boolean) {}

  key(scancode: number, down: boolean): boolean {
    if (!supported.has(scancode) || scancode === REMOTE_KEY_SCANCODES.F12) return false
    if (!down && !this.pressed.has(scancode)) return true
    const code = (scancode & 0x7f) | (down ? 0 : 0x80)
    const packed = (scancode & 0xff00) === 0xe000 ? 0xe0 | (code << 8) : code
    const packet = new ArrayBuffer(10)
    const view = new DataView(packet)
    view.setUint16(0, down ? 101 : 102, true)
    view.setUint32(2, 4, true)
    view.setUint32(6, packed, true)
    if (this.send(packet)) {
      if (down) this.pressed.add(scancode)
      else this.pressed.delete(scancode)
    }
    return true
  }

  release() {
    for (const scancode of Array.from(this.pressed).reverse()) this.key(scancode, false)
    this.pressed.clear()
  }

  paste() {
    const ctrl = this.pressed.has(0x1d) || this.pressed.has(0xe01d)
    if (!ctrl) this.key(0x1d, true)
    this.key(0x2f, true)
    this.key(0x2f, false)
    if (!ctrl) this.key(0x1d, false)
  }
}
