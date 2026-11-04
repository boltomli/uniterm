import { describe, it, expect, vi, afterEach } from 'vitest'
import { createImeKeystrokeReorder, createKeystrokeReorder } from './imeKeystrokeOrder'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

/** Applies the emitted stream to a line the way a shell would. */
function apply(line: string, chunks: string[]): string {
  for (const chunk of chunks) {
    for (const ch of chunk) {
      line = ch === '\x7f' ? line.slice(0, -1) : line + ch
    }
  }
  return line
}

function reorder() {
  vi.useFakeTimers()
  const out: string[] = []
  return { out, r: createKeystrokeReorder(d => out.push(d)) }
}

describe('createKeystrokeReorder', () => {
  it('passes plain ASCII typing through untouched and without delay', () => {
    const { out, r } = reorder()
    for (const ch of 'ls -la') r.push(ch)
    // No timers run: latency on the common path would be the real regression.
    expect(out.join('')).toBe('ls -la')
  })

  it('passes a lone backspace through immediately', () => {
    const { out, r } = reorder()
    r.push('a')
    r.push('\x7f')
    expect(out).toEqual(['a', '\x7f'])
  })

  // The captured VietTelex trace: the replacement lands before the delete that
  // was meant for the character it replaces.
  it('puts a late delete back in front of the replacement', () => {
    const { out, r } = reorder()
    r.push('ê')
    r.push('\x7f')
    vi.runAllTimers()
    expect(apply('tie', out)).toBe('tiê')
  })

  // Same trace, the tone-mark keypress: two deletes arrive early, the third
  // trails the replacement text.
  it('reassembles a split delete run around the replacement', () => {
    const { out, r } = reorder()
    let line = 'tiêng'
    r.push('\x7f')
    r.push('\x7f')
    line = apply(line, out.splice(0))
    expect(line).toBe('tiê')
    for (const chunk of ['ế', 'n', 'g', '\x7f']) r.push(chunk)
    vi.runAllTimers()
    expect(apply(line, out)).toBe('tiếng')
  })

  it('handles the whole phrase', () => {
    const { out, r } = reorder()
    // The captured VietTelex trace for "tieengs vieejt", grouped by the
    // keypress that produced each run. Within a group the events arrive back
    // to back; between groups there is a human pause.
    const byKeypress = [
      ['t'], ['i'], ['e'],
      ['ê', '\x7f'],                              // the second "e" -> ê
      ['n'], ['g'],
      ['\x7f', '\x7f', 'ế', 'n', 'g', '\x7f'],    // "s" -> tone mark on ê
      [' '], ['v'], ['i'], ['e'],
      ['ê', '\x7f'],                              // the second "e" -> ê
      ['\x7f', 'ệ'],                              // "j" -> tone mark on ê
      ['t'],
    ]
    for (const group of byKeypress) {
      for (const chunk of group) r.push(chunk)
      vi.advanceTimersByTime(100)
    }
    expect(apply('', out)).toBe('tiếng việt')
  })

  // XKey delivers the replacement as one multi-character chunk with its
  // deletes already in front; the reorder must leave that alone.
  it('leaves an already-ordered multi-character rewrite intact', () => {
    const { out, r } = reorder()
    let line = 'tiêng'
    for (const chunk of ['\x7f', '\x7f', '\x7f']) r.push(chunk)
    line = apply(line, out.splice(0))
    expect(line).toBe('ti')
    r.push('ếng')
    vi.runAllTimers()
    expect(apply(line, out)).toBe('tiếng')
  })

  // Telex restores a mistyped word when the tone key is pressed twice: "hi" +
  // "s" gives "hí", and a second "s" puts "his" back. That rewrite inserts
  // pure ASCII, so an accented character alone cannot be what opens the burst
  // — the late delete would otherwise land on "híis" and leave "híi".
  it('reorders an all-ASCII rewrite that undoes a previous one', () => {
    const { out, r } = reorder()
    let line = 'hi'

    // First "s": hi -> hí, the delete trailing the replacement.
    r.push('í')
    r.push('\x7f')
    vi.advanceTimersByTime(100)
    line = apply(line, out.splice(0))
    expect(line).toBe('hí')

    // Second "s": hí -> his, all ASCII this time.
    for (const chunk of ['i', 's', '\x7f']) r.push(chunk)
    vi.advanceTimersByTime(100)
    expect(apply(line, out)).toBe('his')
  })

  it('stops buffering once the Vietnamese run goes quiet', () => {
    const { out, r } = reorder()
    r.push('í')
    vi.advanceTimersByTime(100)
    out.length = 0
    // Long after the last accented character, ASCII typing is immediate again.
    vi.advanceTimersByTime(5000)
    r.push('l')
    r.push('s')
    expect(out).toEqual(['l', 's'])
  })

  it('closes the burst before control input instead of reordering it', () => {
    const { out, r } = reorder()
    r.push('ê')
    r.push('\x7f')
    r.push('\r')
    // Enter is the user's keystroke: it must stay last, and the rewrite before
    // it must already be resolved.
    expect(out[out.length - 1]).toBe('\r')
    expect(apply('tie', out.slice(0, -1))).toBe('tiê')
  })

  it('flushes what it holds on dispose', () => {
    const { out, r } = reorder()
    r.push('ê')
    r.dispose()
    expect(out.join('')).toBe('ê')
  })
})

describe('createImeKeystrokeReorder', () => {
  const mac = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15'
  const windows = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36'

  function platformReorder(userAgent: string | undefined) {
    vi.useFakeTimers()
    if (userAgent === undefined) {
      vi.stubGlobal('navigator', undefined)
    } else {
      vi.stubGlobal('navigator', { userAgent })
    }
    const out: string[] = []
    return { out, r: createImeKeystrokeReorder(d => out.push(d)) }
  }

  it('reorders a macOS event-tap rewrite', () => {
    const { out, r } = platformReorder(mac)
    // The replacement lands before the delete meant for the character it
    // replaces — the captured VietTelex trace.
    r.push('ê')
    r.push('\x7f')
    expect(out).toEqual([])
    vi.runAllTimers()
    expect(apply('tie', out)).toBe('tiê')
  })

  it('delays non-ASCII typing on macOS only while a burst is open', () => {
    const { out, r } = platformReorder(mac)
    r.push('ê')
    expect(out).toEqual([])
    vi.runAllTimers()
    expect(out).toEqual(['ê'])
  })

  it('passes every keystroke straight through on Windows', () => {
    const { out, r } = platformReorder(windows)
    // The buffer exists for macOS event-tap IMEs; on Windows it would only
    // hold each keystroke after a Chinese commit for QUIET_MS.
    r.push('ê')
    r.push('\x7f')
    r.push('l')
    expect(out).toEqual(['ê', '\x7f', 'l'])
    vi.runAllTimers()
    expect(out).toEqual(['ê', '\x7f', 'l'])
  })

  it('passes through when there is no navigator at all', () => {
    const { out, r } = platformReorder(undefined)
    r.push('ê')
    r.push('\x7f')
    expect(out).toEqual(['ê', '\x7f'])
  })
})
