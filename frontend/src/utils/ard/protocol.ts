/*
 * Apple RFB extension wire helpers. Based on noVNC-ARD by peetinc,
 * revision d699293635083c9e0c30c8f9480993865653772a (MPL-2.0).
 * https://github.com/peetinc/noVNC-ARD
 * This Source Code Form is subject to the Mozilla Public License, v. 2.0.
 * If a copy was not distributed with this file, obtain it at
 * https://mozilla.org/MPL/2.0/.
 */
import type { DesktopDisplay } from '../vncDisplays'
import keysyms from '@novnc-internals/input/keysymdef.js'

const unicodeKeysyms = new Map<number, number>()
for (let point = 0x100; point <= 0xffff; point++) {
  const keysym = keysyms.lookup(point)
  if (keysym < 0x01000000) unicodeKeysyms.set(keysym, 0x01000000 | point)
}

export function appleKeysym(keysym: number): number {
  return unicodeKeysyms.get(keysym) ?? keysym
}

export function parseAppleDisplays(bytes: Uint8Array) {
  if (bytes.length < 20) throw new Error('Incomplete ARD display header')
  const data = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  const count = data.getUint16(18)
  if (bytes.length < 20 + count * 56) throw new Error('Incomplete ARD display list')
  const displays: DesktopDisplay[] = Array.from({ length: count }, (_, i) => {
    const at = 20 + i * 56
    const x = data.getInt16(at + 30), y = data.getInt16(at + 28)
    return {
      id: data.getUint32(at + 16), primary: !!(data.getUint32(at + 36) & 1),
      x, y, width: data.getInt16(at + 34) - x, height: data.getInt16(at + 32) - y,
    }
  })
  return {
    width: data.getUint16(6), height: data.getUint16(8),
    selected: data.getUint32(10) === 0xffffffff ? null : data.getUint32(10), displays,
  }
}

export function appleSetDisplay(id: number | null): Uint8Array {
  const bytes = new Uint8Array(8)
  bytes[0] = 0x0d
  if (id === null) bytes[1] = 1
  else new DataView(bytes.buffer).setUint32(4, id)
  return bytes
}

export function appleCredentials(username: string, password: string): Uint8Array {
  const bytes = crypto.getRandomValues(new Uint8Array(128))
  const encoder = new TextEncoder()
  for (const [offset, text] of [[0, username], [64, password]] as const) {
    const value = encoder.encode(text)
    if (value.length > 63) throw new Error('ARD credentials must fit in 63 UTF-8 bytes')
    bytes.set(value, offset)
    bytes[offset + value.length] = 0
  }
  return bytes
}

export function appleSessionCommand(consoleUser: string, allowed: number): Uint8Array {
  const bytes = new Uint8Array(74)
  const data = new DataView(bytes.buffer)
  data.setUint16(0, 72)
  data.setUint16(2, 1)
  bytes[8] = !consoleUser && !(allowed & 2) ? 0 : 1
  bytes.set(new TextEncoder().encode(consoleUser).subarray(0, 63), 10)
  return bytes
}

export function appleViewerInfo(): Uint8Array {
  const bytes = new Uint8Array(66)
  const data = new DataView(bytes.buffer)
  bytes[0] = 0x21
  data.setUint16(2, 62)
  data.setUint16(4, 1)
  data.setUint32(6, 2)
  data.setUint32(10, 6)
  data.setUint32(14, 1)
  data.setUint32(22, 15)
  const bitmap = bytes.subarray(34)
  bitmap[0] = 0xb0; bitmap[2] = 0x0c; bitmap[3] = 0x03; bitmap[4] = 0x90; bitmap[10] = 0x40
  return bytes
}

export function buildApplePasteboard(text: string): Uint8Array {
  const encoder = new TextEncoder()
  const name = encoder.encode('public.utf8-plain-text'), value = encoder.encode(text)
  const bytes = new Uint8Array(20 + name.length + value.length)
  const data = new DataView(bytes.buffer)
  data.setUint32(0, 1)
  data.setUint32(4, name.length)
  bytes.set(name, 8)
  data.setUint32(16 + name.length, value.length)
  bytes.set(value, 20 + name.length)
  return bytes
}

export function parseApplePasteboard(bytes: Uint8Array): string | null {
  const data = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  let at = 0
  const number = () => {
    if (at + 4 > bytes.length) throw new Error('Invalid ARD pasteboard')
    const value = data.getUint32(at); at += 4; return value
  }
  const take = (length: number) => {
    if (at + length > bytes.length) throw new Error('Invalid ARD pasteboard')
    const value = bytes.subarray(at, at + length); at += length; return value
  }
  const decoder = new TextDecoder()
  const count = number()
  for (let i = 0; i < count; i++) {
    const name = decoder.decode(take(number()))
    number() // flags
    const properties = number()
    for (let p = 0; p < properties; p++) { take(number()); take(number()) }
    const value = take(number())
    if (name === 'public.utf8-plain-text') return decoder.decode(value)
  }
  return null
}

// PKCS#1 RSAPublicKey, as returned by Apple's security type 33.
export function parseAppleRsaKey(bytes: Uint8Array): { n: Uint8Array; e: Uint8Array } {
  let at = 0
  function element(tag: number): Uint8Array {
    if (bytes[at++] !== tag) throw new Error('Invalid ARD RSA key')
    let size = bytes[at++]!
    if (size & 0x80) {
      const length = size & 0x7f
      if (length === 0 || length > 4) throw new Error('Invalid ARD RSA key length')
      size = 0
      for (let i = 0; i < length; i++) size = size * 256 + bytes[at++]!
    }
    if (!Number.isSafeInteger(size) || size < 1 || at + size > bytes.length) throw new Error('Invalid ARD RSA key length')
    const result = bytes.subarray(at, at + size)
    if (tag !== 0x30) at += size
    return result
  }
  const sequence = element(0x30)
  const end = at + sequence.length
  let n = element(0x02), e = element(0x02)
  if (at !== end) throw new Error('Invalid ARD RSA key fields')
  if (n[0] === 0) n = n.subarray(1)
  if (e[0] === 0) e = e.subarray(1)
  if (n.length < 128 || n.length > 1024 || e.length > n.length) throw new Error('Unsupported ARD RSA key size')
  const padded = new Uint8Array(n.length)
  padded.set(e, n.length - e.length)
  return { n, e: padded }
}
