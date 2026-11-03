import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Terminal as XTerm } from '@xterm/xterm'
vi.mock('./terminalManager', () => ({ getManagedTerminal: () => undefined }))
vi.mock('../stores/settingsStore', () => ({ useSettingsStore: () => ({ settings: { terminal: { highlightEnabled: true } } }) }))
import { observeCommandInput, updateCommandWraps, isCommandContinuation, isLogicalContinuation, disposeCommandWraps, getTerminalSelection, selectionAsSingleLine } from './terminalCommandWrap'
import { createLineRegistry, recordWrittenLines, bufferRowSource } from './terminalTimestamps'
import { buildGutterLines } from '../utils/terminalGutter'
import { OverlayHighlighter } from '../composables/overlayHighlight'

const prompt = 'root@OpenWrt:/tmp# '
const terms: XTerm[] = []
const recorders = new WeakMap<XTerm, (before: number) => void>()
async function terminal(cols = 40) {
  ;(globalThis as any).self = globalThis
  const { Terminal } = await import('@xterm/xterm')
  const t = new Terminal({ cols, rows: 8, scrollback: 30, allowProposedApi: true })
  terms.push(t)
  await write(t, prompt)
  return t
}
function write(t: XTerm, data: string) {
  const before = t.buffer.active.baseY + t.buffer.active.cursorY
  return new Promise<void>(resolve => t.write(data, () => { updateCommandWraps(t); recorders.get(t)?.(before); resolve() }))
}
async function ashEcho(t: XTerm, text: string, fragmented = false) {
  let x = t.buffer.active.cursorX
  for (const char of text) {
    await write(t, char)
    if (++x === t.cols) {
      if (fragmented) { await write(t, '\r'); await write(t, '\n') }
      else await write(t, '\r\n')
      x = 0
    }
  }
}
function select(t: XTerm, start: { x: number; y: number }, end: { x: number; y: number }, column = false) {
  const rows: string[] = []
  for (let y = start.y; y <= end.y; y++) rows.push(t.buffer.active.getLine(y)!.translateToString(true, y === start.y ? start.x : 0, y === end.y ? end.x : t.cols))
  const native = rows.join('\n')
  vi.spyOn(t, 'getSelection').mockReturnValue(native)
  vi.spyOn(t, 'getSelectionPosition').mockReturnValue({ start, end })
  ;(t as any)._core._selectionService = { _activeSelectionMode: column ? 3 : 0 }
  return native
}
afterEach(() => {
  for (const t of terms.splice(0)) { disposeCommandWraps(t); t.dispose() }
  vi.restoreAllMocks()
})

describe('verified command-input hard wraps', () => {
  it('joins BusyBox CRLF echo for copy, gutter and highlighting without changing native wrap flags', async () => {
    const t = await terminal()
    const command = "echo '" + 'a'.repeat(14) + '  ' + 'b'.repeat(24) + "'"
    observeCommandInput(t, command)
    await ashEcho(t, command)
    expect(t.buffer.active.getLine(1)!.isWrapped).toBe(false)
    expect(isCommandContinuation(t, 1)).toBe(true)
    const end = { x: t.buffer.active.cursorX, y: t.buffer.active.cursorY }
    const native = select(t, { x: prompt.length, y: 0 }, end)
    expect(native).toContain('\n')
    expect(getTerminalSelection(t)).toBe(command)
    const registry = createLineRegistry()
    recordWrittenLines(registry, { lineOffset: 0, beforeAbs: 0, afterAbs: end.y, now: 100, source: bufferRowSource(t.buffer.active, y => isLogicalContinuation(t, y)) })
    const gutter = buildGutterLines({ rows: 3, viewportY: 0, lineOffset: 0, cursorAbsoluteY: end.y, getLine: y => ({ isWrapped: isLogicalContinuation(t, y) }), getMeta: y => registry.entries.get(y) })
    expect(gutter.lines.map(row => row.lineNumber)).toEqual(['1', '', ''])
    const colors = vi.spyOn(t, 'setCellColorOverrides').mockImplementation(() => {})
    vi.spyOn(t, 'clearCellColorOverrides').mockImplementation(() => {})
    const highlighter = new OverlayHighlighter(t)
    highlighter.forceRefresh()
    expect(colors.mock.calls.some(([row, spans]) => row === 1 && spans?.some(s => s.color === '#e5e510'))).toBe(true)
    highlighter.dispose()
  })

  it('handles byte-fragmented CRLF and keeps the next real output line separate', async () => {
    const t = await terminal()
    const command = "echo '" + 'a'.repeat(70) + "'"
    observeCommandInput(t, command)
    await ashEcho(t, command, true)
    expect(isCommandContinuation(t, 1)).toBe(true)
    expect(isCommandContinuation(t, 2)).toBe(true)
    const commandEnd = t.buffer.active.cursorY
    observeCommandInput(t, '\r')
    await write(t, '\r\n' + 'output'.padEnd(t.cols, 'x') + '\r\nnext output')
    expect(isCommandContinuation(t, commandEnd + 1)).toBe(false)
    expect(isCommandContinuation(t, commandEnd + 2)).toBe(false)
    select(t, { x: prompt.length, y: 0 }, { x: t.buffer.active.cursorX, y: t.buffer.active.cursorY })
    expect(getTerminalSelection(t)).toBe(command + '\n' + 'output'.padEnd(t.cols, 'x') + '\nnext output')
  })

  it('works while single keystrokes and their echo arrive incrementally', async () => {
    const t = await terminal()
    for (const ch of "echo '" + 'a'.repeat(60) + "'") {
      observeCommandInput(t, ch)
      await ashEcho(t, ch, true)
    }
    expect(isCommandContinuation(t, 1)).toBe(true)
  })

  it('does not infer grouping for unrelated output, mismatched echo, multiline paste or editing', async () => {
    const t = await terminal()
    observeCommandInput(t, 'a'.repeat(60))
    await ashEcho(t, 'b'.repeat(60))
    expect(isCommandContinuation(t, 1)).toBe(false)
    await write(t, '\r\n' + prompt)
    observeCommandInput(t, 'a'.repeat(50) + '\nb')
    await ashEcho(t, 'a'.repeat(50))
    expect(isCommandContinuation(t, t.buffer.active.cursorY)).toBe(false)
    await write(t, '\r\n' + prompt)
    observeCommandInput(t, 'c'.repeat(60))
    await ashEcho(t, 'c'.repeat(60))
    expect(isCommandContinuation(t, t.buffer.active.cursorY)).toBe(true)
    observeCommandInput(t, '\x1b[D')
    expect(isCommandContinuation(t, t.buffer.active.cursorY)).toBe(false)
  })

  it('assigns no extra registry numbers during incremental echo and resumes numbering for output', async () => {
    const t = await terminal()
    const registry = createLineRegistry()
    const record = (before: number) => recordWrittenLines(registry, {
      lineOffset: 0, beforeAbs: before, afterAbs: t.buffer.active.baseY + t.buffer.active.cursorY,
      now: 100, source: bufferRowSource(t.buffer.active, y => isLogicalContinuation(t, y)),
    })
    record(0)
    recorders.set(t, record)
    for (const char of 'a'.repeat(70)) {
      observeCommandInput(t, char)
      await ashEcho(t, char, true)
    }
    expect(registry.nextNumber).toBe(2)
    const lastInputRow = t.buffer.active.cursorY
    observeCommandInput(t, '\r')
    await write(t, '\r\nresult\r\n' + prompt)
    expect(registry.entries.get(lastInputRow + 1)?.number).toBe(2)
    expect(registry.entries.get(lastInputRow + 2)?.number).toBe(3)
  })

  it('keeps end-of-line Backspace verifiable without merging overwritten rows', async () => {
    const t = await terminal()
    const registry = createLineRegistry()
    const record = (before: number) => recordWrittenLines(registry, {
      lineOffset: 0, beforeAbs: before, afterAbs: t.buffer.active.baseY + t.buffer.active.cursorY,
      now: 100, source: bufferRowSource(t.buffer.active, y => isLogicalContinuation(t, y)),
    })
    record(0)
    recorders.set(t, record)
    const command = "echo '" + 'a'.repeat(50) + "'"
    observeCommandInput(t, command)
    await ashEcho(t, command)
    observeCommandInput(t, '\x7f')
    await write(t, '\b')
    await write(t, ' ')
    await write(t, '\b')
    observeCommandInput(t, "b'")
    await ashEcho(t, "b'")
    select(t, { x: prompt.length, y: 0 }, { x: t.buffer.active.cursorX, y: t.buffer.active.cursorY })
    expect(getTerminalSelection(t)).toBe(command.slice(0, -1) + "b'")
    expect(registry.nextNumber).toBe(2)
    const row = t.buffer.active.cursorY
    await write(t, '\r\x1b[2Kother text')
    expect(isCommandContinuation(t, row)).toBe(false)
  })

  it('handles a caret parked at the exact margin, then preserves command output', async () => {
    const t = await terminal()
    const command = 'a'.repeat(t.cols - prompt.length)
    observeCommandInput(t, command)
    await ashEcho(t, command, true)
    expect(t.buffer.active.cursorX).toBe(0)
    expect(isCommandContinuation(t, 1)).toBe(true)
    select(t, { x: prompt.length, y: 0 }, { x: 0, y: 1 })
    expect(getTerminalSelection(t)).toBe(command)
    observeCommandInput(t, '\r')
    await write(t, 'result')
    expect(isCommandContinuation(t, 1)).toBe(false)
    select(t, { x: prompt.length, y: 0 }, { x: 6, y: 1 })
    expect(getTerminalSelection(t)).toBe(command + '\nresult')
  })

  it('keeps completed command metadata while it moves into scrollback', async () => {
    const t = await terminal()
    const command = "echo '" + 'a'.repeat(50) + "'"
    observeCommandInput(t, command)
    await ashEcho(t, command)
    observeCommandInput(t, '\r')
    await write(t, '\r\n' + ('line\r\n').repeat(12))
    expect(isCommandContinuation(t, 1)).toBe(true)
    select(t, { x: prompt.length, y: 0 }, { x: (prompt.length + command.length) % t.cols, y: 1 })
    expect(getTerminalSelection(t)).toBe(command)
  })

  it('preserves rectangular copy and real newlines when no input was verified', async () => {
    const t = await terminal()
    observeCommandInput(t, 'a'.repeat(60))
    await ashEcho(t, 'a'.repeat(60))
    const native = select(t, { x: 2, y: 0 }, { x: 12, y: 1 }, true)
    expect(getTerminalSelection(t)).toBe(native)
    await write(t, '\r\nplain output\r\nmore output')
    const raw = select(t, { x: 0, y: 2 }, { x: 11, y: 3 })
    expect(getTerminalSelection(t)).toBe(raw)
  })

  it('rejects a bare prompt and invalidates inference on overwrite, resize or alternate screen', async () => {
    const t = await terminal()
    await write(t, '\r\x1b[2K$ ')
    observeCommandInput(t, 'a'.repeat(60))
    await ashEcho(t, 'a'.repeat(60))
    expect(isCommandContinuation(t, 1)).toBe(false)
    await write(t, '\r\n' + prompt)
    observeCommandInput(t, 'a'.repeat(60))
    await ashEcho(t, 'a'.repeat(60))
    const row = t.buffer.active.baseY + t.buffer.active.cursorY
    expect(isCommandContinuation(t, row)).toBe(true)
    await write(t, '\x1b[?1049h')
    expect(isCommandContinuation(t, row)).toBe(false)
    await write(t, '\x1b[?1049l')
    t.resize(50, 8)
    expect(isCommandContinuation(t, row)).toBe(false)
  })
})

describe('explicit single-line copy', () => {
  it('removes line separators while preserving spaces, tabs and quotes', () => {
    expect(selectionAsSingleLine("echo 'aa  \r\nbb\ncc'\t\rd")).toBe("echo 'aa  bbcc'\td")
  })
})
