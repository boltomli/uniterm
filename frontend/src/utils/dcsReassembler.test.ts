import { describe, it, expect } from 'vitest'
import { DcsReassembler, MAX_DCS_BUFFER } from './dcsReassembler'

// A minimal but structurally valid sixel image: DCS introducer, P1;P2
// params, 'q' final, a raster attributes block and one sixel data line,
// terminated by ST (ESC \).
const SIXEL = '\x1bP0;1q#0;2;100;100;100~~-~~\x1b\\'
// DECRQSS-style DCS (tmux passthrough / terminal queries also use DCS).
const QUERY = '\x1bP$qm\x1b\\'

function feedAll(d: DcsReassembler, chunks: string[]) {
  const out = []
  for (const c of chunks) out.push(...d.feed(c))
  return out
}

describe('DcsReassembler', () => {
  it('passes plain text through untouched', () => {
    const d = new DcsReassembler()
    expect(d.feed('hello world\n')).toEqual([{ text: 'hello world\n', dcs: false }])
  })

  it('passes a complete DCS in one chunk as a marked segment', () => {
    const d = new DcsReassembler()
    expect(d.feed('before' + SIXEL + 'after')).toEqual([
      { text: 'before', dcs: false },
      { text: SIXEL, dcs: true },
      { text: 'after', dcs: false },
    ])
  })

  it('reassembles a DCS split across chunk boundaries', () => {
    const d = new DcsReassembler()
    const segs = feedAll(d, ['\x1bP0;1q#0;2;100;100', ';100~~-~', '~\x1b', '\\done'])
    expect(segs).toEqual([
      { text: SIXEL, dcs: true },
      { text: 'done', dcs: false },
    ])
  })

  it('holds a trailing lone ESC and joins it with the next chunk', () => {
    const d = new DcsReassembler()
    expect(d.feed('ok\x1b')).toEqual([{ text: 'ok', dcs: false }])
    expect(d.feed('P1q~~\x1b\\')).toEqual([{ text: '\x1bP1q~~\x1b\\', dcs: true }])
  })

  it('does not mistake a mid-chunk ESC for a held one', () => {
    const d = new DcsReassembler()
    // \x1b[2J is a CSI sequence, not a DCS introducer — pass through as-is.
    expect(d.feed('a\x1b[2Jb')).toEqual([{ text: 'a\x1b[2Jb', dcs: false }])
  })

  it('starts a DCS split right after the introducer', () => {
    const d = new DcsReassembler()
    expect(d.feed('\x1bP')).toEqual([])
    expect(d.feed('0;1q#0;2;100;100;100~~-~~\x1b\\')).toEqual([{ text: SIXEL, dcs: true }])
  })

  it('aborts buffering when ESC is followed by something other than backslash', () => {
    const d = new DcsReassembler()
    // A shell prompt redraw (CSI) arrives while a DCS was still open —
    // the DCS is malformed, so flush it and resume normal scanning.
    const segs = d.feed('\x1bP0;1q~~\x1b[2Jrest')
    expect(segs).toEqual([
      { text: '\x1bP0;1q~~', dcs: true },
      { text: '\x1b[2Jrest', dcs: false },
    ])
  })

  it('completes a held DCS even when ST is the whole next chunk', () => {
    const d = new DcsReassembler()
    expect(d.feed(QUERY.slice(0, -2))).toEqual([])
    expect(d.feed('\x1b\\')).toEqual([{ text: QUERY, dcs: true }])
  })

  it('handles several DCS sequences in one chunk', () => {
    const d = new DcsReassembler()
    expect(d.feed(SIXEL + 'x' + QUERY + 'y')).toEqual([
      { text: SIXEL, dcs: true },
      { text: 'x', dcs: false },
      { text: QUERY, dcs: true },
      { text: 'y', dcs: false },
    ])
  })

  it('reset() discards pending state', () => {
    const d = new DcsReassembler()
    d.feed('\x1bP0;1q')
    d.reset()
    expect(d.feed('plain')).toEqual([{ text: 'plain', dcs: false }])
  })

  it('flushes an unterminated DCS as a marked segment on abort', () => {
    const d = new DcsReassembler()
    // ESC followed by a non-backslash aborts; CAN (0x18) also aborts per
    // the VT spec. Either way the buffered bytes must surface (marked as
    // DCS) instead of being held forever.
    const segs = d.feed('\x1bP0;1q~~\x1bX')
    expect(segs).toEqual([
      { text: '\x1bP0;1q~~', dcs: true },
      { text: '\x1bX', dcs: false },
    ])
  })

  it('resumes the scan from the last examined byte (cursor, not rescan)', () => {
    // Feed the body in many chunks; only the chunk that carries ST should
    // produce output, and the reassembled DCS must be exactly the fed bytes.
    const d = new DcsReassembler()
    const chunks = ['\x1bP0;1q']
    for (let i = 0; i < 50; i++) chunks.push('#'.repeat(1000))
    chunks.push('\x1b', '\\tail')
    const segs = feedAll(d, chunks)
    const body = '\x1bP0;1q' + '#'.repeat(50 * 1000)
    expect(segs).toEqual([
      { text: body + '\x1b\\', dcs: true },
      { text: 'tail', dcs: false },
    ])
  })

  it('holds an unterminated DCS below the size cap', () => {
    const d = new DcsReassembler()
    const big = 'a'.repeat(MAX_DCS_BUFFER - 16)
    expect(d.feed('\x1bP0;1q' + big)).toEqual([])
  })

  it('flushes an over-cap DCS so later output is not held hostage', () => {
    const d = new DcsReassembler()
    const big = 'a'.repeat(MAX_DCS_BUFFER + 1)
    // The oversized body surfaces immediately, marked as DCS so the
    // highlighter still bypasses it.
    const first = d.feed('\x1bP0;1q' + big)
    expect(first).toEqual([{ text: '\x1bP0;1q' + big, dcs: true }])
    // Subsequent plain output must flow through instead of being buffered.
    expect(d.feed('plain output\n')).toEqual([{ text: 'plain output\n', dcs: false }])
    // And a fresh DCS afterwards still reassembles normally.
    expect(d.feed(QUERY)).toEqual([{ text: QUERY, dcs: true }])
  })

  it('flushes across chunk boundary once the cap is crossed', () => {
    const d = new DcsReassembler()
    const part1 = 'a'.repeat(MAX_DCS_BUFFER - 10)
    const part2 = 'b'.repeat(64)
    expect(d.feed('\x1bP' + part1)).toEqual([])
    // Crossing the cap flushes everything buffered so far.
    expect(d.feed(part2)).toEqual([{ text: '\x1bP' + part1 + part2, dcs: true }])
    expect(d.feed('after')).toEqual([{ text: 'after', dcs: false }])
  })
})
