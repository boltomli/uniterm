import type { Terminal, IMarker, IDisposable } from '@xterm/xterm'

// BusyBox ash emits CRLF at the right margin while editing a command. Keep
// inferred continuations separate from xterm's buffer: never change the PTY
// stream, isWrapped flags, cursor positioning, or reflow behavior.
interface CommandGroup {
  marker: IMarker
  cols: number
  rows: string[]
}
interface Candidate {
  group: CommandGroup
  expected: string
  prefixLength: number
  verifiedLength: number
  eraseLimit: number
  submitted: boolean
}
const MAX_COMMAND = 16384
const MAX_GROUPS = 128
const states = new WeakMap<Terminal, CommandWrapState>()

class CommandWrapState {
  groups: CommandGroup[] = []
  candidate: Candidate | null = null
  private resize: IDisposable

  constructor(private term: Terminal) {
    // Hard-wrap positions cannot safely be mapped through an arbitrary reflow.
    this.resize = term.onResize(() => this.clear())
  }

  clear() {
    for (const group of this.groups) group.marker.dispose()
    this.candidate?.group.marker.dispose()
    this.groups = []
    this.candidate = null
  }

  dispose() { this.clear(); this.resize.dispose() }

  private abandon() {
    if (!this.candidate) return
    const group = this.candidate.group
    this.groups = this.groups.filter(g => g !== group)
    group.marker.dispose()
    this.candidate = null
  }

  input(data: string) {
    const buf = this.term.buffer.active
    if (buf.type !== 'normal') { this.abandon(); return }
    if (this.candidate?.submitted) {
      if (!this.groups.includes(this.candidate.group)) this.candidate.group.marker.dispose()
      this.candidate = null
    }
    // Only ASCII input at the end (including Backspace), with at most one Enter, is known
    // exactly. Completion/history/cursor movement, Unicode and multiline paste need
    // shell integration; guessing would merge unrelated terminal output.
    if (!/^[\x08\x7f\x20-\x7e]*(?:\r|\n)?$/.test(data)) { this.abandon(); return }
    const submitted = /[\r\n]$/.test(data)
    const text = submitted ? data.slice(0, -1) : data
    if (!this.candidate && text) {
      const row = buf.baseY + buf.cursorY
      const line = buf.getLine(row)
      const prefix = line?.translateToString(false, 0, buf.cursorX) ?? ''
      const remainder = line?.translateToString(true, buf.cursorX) ?? ''
      // A complete, plain POSIX user@host:path prompt, with the cursor at its
      // end. Generic '$' in program output is not enough evidence.
      if (line?.isWrapped || remainder.trim() || !/^[\w.-]+@[\w.-]+:[\x21-\x7e]*[$#] ?$/.test(prefix)) return
      const marker = this.term.registerMarker()
      if (!marker) return
      this.candidate = { group: { marker, cols: this.term.cols, rows: [] }, expected: prefix, prefixLength: prefix.length, verifiedLength: prefix.length, eraseLimit: prefix.length, submitted: false }
    }
    const candidate = this.candidate
    if (!candidate) return
    for (const char of text) {
      if (char === '\b' || char === '\x7f') {
        candidate.eraseLimit = Math.max(candidate.eraseLimit, candidate.expected.length)
        if (candidate.expected.length > candidate.prefixLength) candidate.expected = candidate.expected.slice(0, -1)
      } else candidate.expected += char
    }
    candidate.submitted = submitted
    if (candidate.expected.length > MAX_COMMAND) this.abandon()
  }

  parsed() {
    this.groups = this.groups.filter(g => !g.marker.isDisposed)
    const candidate = this.candidate
    if (!candidate) return
    const { group, expected, submitted } = candidate
    const buf = this.term.buffer.active
    if (buf.type !== 'normal' || group.marker.isDisposed || group.cols !== this.term.cols) { this.abandon(); return }
    const start = group.marker.line
    const cursorLength = (buf.baseY + buf.cursorY - start) * group.cols + buf.cursorX
    // Erasing at the end can arrive as three separate bytes: BS, space, BS.
    // The temporary space advances the cursor one cell past the new input.
    // Validate only the shortened input and require a blank tail below.
    const length = submitted ? expected.length : Math.min(cursorLength, expected.length)
    // CR and LF can arrive in separate writes. A CR temporarily moves the
    // cursor before the prompt; wait for the rest rather than abandoning.
    if (length <= 0) return
    // A fragmented CR temporarily moves backward through already verified
    // text. Keep its seam metadata until LF arrives. A local Backspace is
    // different: it also shortened expected, so the new prefix can be checked.
    if (!submitted && length < candidate.verifiedLength && expected.length >= candidate.verifiedLength) return
    if (!submitted && cursorLength > Math.max(expected.length, candidate.eraseLimit)) { this.abandon(); return }
    const count = Math.ceil(length / group.cols)
    const rows: string[] = []
    let echoed = ''
    for (let i = 0; i < count; i++) {
      const line = buf.getLine(start + i)
      if (!line) return
      rows.push(line.translateToString(false, 0, group.cols))
      echoed += line.translateToString(false, 0, Math.min(group.cols, length - i * group.cols))
    }
    if (echoed !== expected.slice(0, length)) {
      // Input can still be awaiting its echo or a redraw; do not infer anything
      // new from that write. Existing snapshots stop matching edited rows.
      return
    }
    const lastWidth = length - (count - 1) * group.cols
    if (!submitted && buf.getLine(start + count - 1)?.translateToString(true, lastWidth).trim()) return
    // At the right margin ash parks the caret on a new empty row before
    // Enter is pressed. It is part of the editable input only while empty;
    // submitting (or receiving output there) removes this inferred seam.
    if (!submitted && length % group.cols === 0 && buf.cursorX === 0 &&
        buf.baseY + buf.cursorY === start + count &&
        buf.getLine(start + count)?.translateToString(true) === '') {
      rows.push(buf.getLine(start + count)!.translateToString(false, 0, group.cols))
    }
    candidate.verifiedLength = length
    group.rows = rows
    if (rows.length > 1 && !this.groups.includes(group)) {
      this.groups.push(group)
      if (this.groups.length > MAX_GROUPS) this.groups.shift()?.marker.dispose()
    }
    if (submitted) {
      if (rows.length <= 1) group.marker.dispose()
      this.candidate = null
    }
  }

  continuation(row: number): boolean {
    if (this.term.buffer.active.type !== 'normal') return false
    const buf = this.term.buffer.active
    for (const group of this.groups) {
      const offset = row - group.marker.line
      if (group.marker.isDisposed || group.cols !== this.term.cols || offset <= 0 || offset >= group.rows.length) continue
      // Rows may be overwritten in place by shell redraws or terminal apps.
      // Require both sides of a seam to still match the verified input.
      if (buf.getLine(row - 1)?.translateToString(false, 0, group.cols) === group.rows[offset - 1] &&
          buf.getLine(row)?.translateToString(false, 0, group.cols) === group.rows[offset]) return true
    }
    return false
  }
}

export function observeCommandInput(term: Terminal, data: string): void {
  let state = states.get(term)
  if (!state) { state = new CommandWrapState(term); states.set(term, state) }
  state.input(data)
}
export function updateCommandWraps(term: Terminal): void { states.get(term)?.parsed() }
export function isCommandContinuation(term: Terminal, row: number): boolean { return states.get(term)?.continuation(row) ?? false }
export function isLogicalContinuation(term: Terminal, row: number): boolean {
  return term.buffer.active.getLine(row)?.isWrapped === true || isCommandContinuation(term, row)
}
export function disposeCommandWraps(term: Terminal): void { states.get(term)?.dispose(); states.delete(term) }

// Preserve ordinary/rectangular selection behavior. Reconstruct only when a
// selected seam was positively identified as a command-input continuation.
export function getTerminalSelection(term: Terminal): string {
  const native = term.getSelection()
  const range = term.getSelectionPosition()
  const service = (term as unknown as { _core?: { _selectionService?: { _activeSelectionMode?: number } } })._core?._selectionService
  // SelectionMode.COLUMN = 3 in the pinned xterm fork. If private API changes,
  // prefer the native result to guessing the semantics of a selection.
  if (!native || !range || !service || ![0, 1, 2].includes(service._activeSelectionMode ?? -1)) return native
  let inferred = false
  for (let y = range.start.y + 1; y <= range.end.y; y++) if (isCommandContinuation(term, y)) { inferred = true; break }
  if (!inferred) return native
  const buf = term.buffer.active
  const separator = native.includes('\r\n') ? '\r\n' : '\n'
  let result = ''
  for (let y = range.start.y; y <= range.end.y; y++) {
    const line = buf.getLine(y)
    if (!line) return native
    const start = y === range.start.y ? range.start.x : 0
    const end = y === range.end.y ? range.end.x : term.cols
    if (y !== range.start.y && !isLogicalContinuation(term, y)) result += separator
    const keepTrailingSpaces = y < range.end.y && isCommandContinuation(term, y + 1)
    result += line.translateToString(!keepTrailingSpaces, start, end).replace(/\u00a0/g, ' ')
  }
  return result
}

// Explicit opt-in for copying a screen-wrapped command when its original
// logical boundaries cannot be established. Never applied to normal copy.
export function selectionAsSingleLine(text: string): string {
  return text.replace(/\r\n|\r|\n/g, '')
}
