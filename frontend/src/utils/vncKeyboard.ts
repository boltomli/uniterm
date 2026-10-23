import { REMOTE_KEY_SCANCODES } from './remoteKeyboard'
import type { NativeRdpKey } from './remoteKeyboard'

const codes = Object.fromEntries(Object.entries(REMOTE_KEY_SCANCODES).map(([code, scan]) => [scan, code]))
const special: Record<string, number> = {
  AltLeft: 0xffe9, AltRight: 0xffea, ControlLeft: 0xffe3, ControlRight: 0xffe4,
  ShiftLeft: 0xffe1, ShiftRight: 0xffe2, MetaLeft: 0xffeb, MetaRight: 0xffec,
  Tab: 0xff09, Escape: 0xff1b, Enter: 0xff0d, Backspace: 0xff08, Space: 0x20,
  ArrowUp: 0xff52, ArrowDown: 0xff54, ArrowLeft: 0xff51, ArrowRight: 0xff53,
  Home: 0xff50, End: 0xff57, Insert: 0xff63, Delete: 0xffff,
  PageUp: 0xff55, PageDown: 0xff56, ContextMenu: 0xff67, NumpadEnter: 0xff8d,
  CapsLock: 0xffe5, NumLock: 0xff7f, ScrollLock: 0xff14, PrintScreen: 0xff61,
  NumpadMultiply: 0xffaa, NumpadAdd: 0xffab, NumpadSubtract: 0xffad,
  NumpadDecimal: 0xffae, NumpadDivide: 0xffaf,
}
const punctuation: Record<string, string> = {
  Minus: '-_', Equal: '=+', BracketLeft: '[{', BracketRight: ']}',
  Backslash: '\\|', Semicolon: ';:', Quote: '\'"', Backquote: '`~',
  Comma: ',<', Period: '.>', Slash: '/?', IntlBackslash: '\\|',
}

function nativeKey(event: NativeRdpKey): { code: string; keysym: number } | null {
  const code = codes[event.scancode]
  if (!code) return null
  const vk = event.virtualKey ?? 0
  if (vk >= 0x41 && vk <= 0x5a) return { code, keysym: vk + (event.shift ? 0 : 32) }
  let keysym = special[code]
  if (keysym === undefined && /^F\d+$/.test(code)) keysym = 0xffbe + Number(code.slice(1)) - 1
  if (keysym === undefined && /^Key[A-Z]$/.test(code)) keysym = (event.shift ? code[3] : code[3].toLowerCase()).charCodeAt(0)
  if (keysym === undefined && /^Digit\d$/.test(code)) keysym = (event.shift ? ')!@#$%^&*('[Number(code[5])] : code[5]).charCodeAt(0)
  if (keysym === undefined && /^Numpad\d$/.test(code)) keysym = 0xffb0 + Number(code[6])
  if (keysym === undefined && punctuation[code]) keysym = punctuation[code].charCodeAt(event.shift ? 1 : 0)
  return { code, keysym: keysym ?? 0 }
}

export class VncNativeKeyboard {
  private pressed = new Map<number, { code: string; keysym: number }>()

  constructor(private readonly send: (keysym: number, code: string, down: boolean) => void) {}

  key(event: NativeRdpKey) {
    const key = this.pressed.get(event.scancode) ?? nativeKey(event)
    if (!key) return
    if (event.down) this.pressed.set(event.scancode, key)
    else this.pressed.delete(event.scancode)
    this.send(key.keysym, key.code, event.down)
  }

  release() {
    for (const key of Array.from(this.pressed.values()).reverse()) this.send(key.keysym, key.code, false)
    this.pressed.clear()
  }
}
