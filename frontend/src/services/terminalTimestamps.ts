import { getManagedTerminal } from './terminalManager'
import { useSettingsStore } from '../stores/settingsStore'

/**
 * Logical-line registry + recording for the terminal gutter's number/time
 * columns. One module owns the whole feature:
 *
 * 1. Pure registry below (LineRegistryState / recordWrittenLines /
 *    realignRegistry / bufferRowSource) — no xterm/DOM so it is unit-testable.
 * 2. Service functions (recordWrite / stampCommandLine / currentAbsoluteLine)
 *    that adapt the live terminal to the registry.
 *
 * xterm has no notion of "when a line was written" or "which number a line
 * is", so we maintain a registry ourselves. A "logical line" is one
 * shell/output line: a non-wrapped buffer row plus any wrapped continuation
 * rows below it. xterm only exposes physical rows, and reflow (resize)
 * redistributes them, so line numbers and timestamps must NOT be derived from
 * buffer positions — they are assigned once, when the line is born, and stay
 * fixed for the line's lifetime:
 *
 * - `number` — sequential, assigned at registration; wrapped continuation
 *   rows never get one, and resize never renumbers existing lines.
 * - `ts` — arrival time of the write that first put characters on the line;
 *   overwritten with the completion time once the cursor moves past the line
 *   (progress bars redrawn in place keep their start time until the newline
 *   completes them).
 *
 * Entries are keyed by the line's absolute start row (lineOffset-compensated
 * buffer row). Scrollback trimming keeps absolute indexes stable by
 * construction (lineOffset grows in lockstep), and realignRegistry re-keys
 * the whole map after a resize reflow, so a key always points at the row the
 * line currently starts at. The registry lives on the managed terminal, so
 * the data survives KeepAlive and drag-across-panes.
 *
 * Recording is UNCONDITIONAL: we register every line regardless of the
 * showTimestamps/showLineNumbers settings, so enabling a column mid-session
 * still shows data for lines that already appeared. The display settings only
 * decide whether the gutter paints the columns (and whether we bother to poke
 * the gutter to refresh immediately).
 *
 * Display-side computation (resolving a visible row to its number/time)
 * lives in utils/terminalGutter.ts — it is shared by TerminalGutter.vue and
 * TerminalScreenPreview.vue.
 */

// ─── Pure registry (no xterm/DOM) ───────────────────────────────────────────

export interface LineMetaEntry {
  /** Sequential logical line number, fixed at registration time. */
  number: number
  /** Birth time (ms); overwritten with the completion time when done. */
  ts: number
}

export interface LineRegistryState {
  /** Next sequential line number to hand out. */
  nextNumber: number
  /** Absolute start row → meta. Reassigned wholesale by realignRegistry —
   * always reach it through the state object, never cache the Map. */
  entries: Map<number, LineMetaEntry>
  /** Absolute-row high-water mark of everything a write scan has probed.
   * The look-above window only re-probes rows above it: rescanning up to
   * LOOK_ABOVE_ROWS blank rows on EVERY write (translateToString each) was
   * the dominant gutter cost under sustained output. Rows at or below the
   * watermark that gain content later via cursor-addressed repaint are the
   * accepted trade-off — the cursor band itself is always scanned. */
  scannedUpTo: number
}

export function createLineRegistry(): LineRegistryState {
  return { nextNumber: 1, entries: new Map(), scannedUpTo: -1 }
}

/** Minimal row probe the registry needs from the live buffer. */
export interface LineRegistryRowSource {
  isWrapped: (bufferRow: number) => boolean
  /** Whether the row holds any visible (non-whitespace) cell content. */
  hasContent: (bufferRow: number) => boolean
}

/** Duck-typed shape of an xterm buffer, for the adapter below. */
export interface RegistryBufferLike {
  getLine(y: number): {
    isWrapped: boolean
    translateToString?(trimRight: boolean): string
  } | undefined | null
}

/** Adapter from an xterm buffer to the registry's row probe. */
export function bufferRowSource(buf: RegistryBufferLike): LineRegistryRowSource {
  return {
    isWrapped: (y) => buf.getLine(y)?.isWrapped ?? false,
    hasContent: (y) => {
      const line = buf.getLine(y)
      if (!line?.translateToString) return false
      return line.translateToString(true).trim().length > 0
    },
  }
}

export interface RecordWriteOptions {
  /** Current lineOffset (trim accumulator) — keys are offset-compensated. */
  lineOffset: number
  /** Absolute cursor row captured before and after the write. If a trim
   * happened inside the write, absolute indexes survive it unchanged. */
  beforeAbs: number
  afterAbs: number
  source: LineRegistryRowSource
  /** Arrival time of this write (ms). */
  now: number
}

/** Rows above the cursor band also scanned for unregistered content —
 * catches in-place redraws (TUI progress lines, prompt repaints) that write
 * above the cursor without the cursor ever travelling there. */
const LOOK_ABOVE_ROWS = 200

/** Entry-count bounds: drop the oldest entries past the cap. The buffer can
 * hold at most scrollback+rows logical lines (~2.5k here), so anything below
 * the kept watermark is guaranteed to be off-buffer already. */
const MAX_ENTRIES = 5000
const KEEP_ENTRIES = 3000

/**
 * Fold one applied write into the registry: register logical lines that just
 * received their first characters, and stamp completion times for lines the
 * cursor has moved past. The main scan walks the band the cursor travelled
 * (exactly the band shell output touches); a look-above window also picks up
 * content painted out-of-band — but only rows the watermark hasn't covered
 * yet, so sustained output doesn't re-probe the same blank rows forever.
 */
export function recordWrittenLines(state: LineRegistryState, opts: RecordWriteOptions): void {
  const { lineOffset, beforeAbs, afterAbs, source, now } = opts
  const from = Math.min(beforeAbs, afterAbs) - lineOffset
  const to = Math.max(beforeAbs, afterAbs) - lineOffset
  const cursorRow = afterAbs - lineOffset
  // Registration: a non-wrapped row with visible content and no entry
  // becomes a new logical line. A newline parks the cursor on an empty row
  // below the output — that row only joins once something writes on it.
  const registerRow = (y: number) => {
    if (source.isWrapped(y)) return
    const abs = lineOffset + y
    if (state.entries.has(abs)) return
    if (!source.hasContent(y)) return
    state.entries.set(abs, { number: state.nextNumber++, ts: now })
  }

  // 1a. Look-above: rows above the cursor band that NO earlier write probed.
  //     Re-probing them every write cost a translateToString per blank row
  //     (up to LOOK_ABOVE_ROWS of them) under sustained output — the
  //     dominant gutter cost (security.md perf item).
  const lookFrom = Math.max(0, from - LOOK_ABOVE_ROWS, state.scannedUpTo + 1 - lineOffset)
  for (let y = lookFrom; y < from; y += 1) registerRow(y)
  // 1b. The cursor band itself: always scanned — this write's output landed
  //     exactly here, so its rows may have gained content just now.
  for (let y = from; y <= to; y += 1) registerRow(y)

  // 2. Completion: for rows in the band belonging to lines that now end above
  //    the cursor, the output has finished — fix their timestamp.
  for (let y = from; y <= to; y += 1) {
    let start = y
    while (start > 0 && source.isWrapped(start)) start -= 1
    let end = start
    while (source.isWrapped(end + 1)) end += 1
    if (end >= cursorRow) continue
    const entry = state.entries.get(lineOffset + start)
    if (entry && entry.ts !== now) entry.ts = now
  }

  // 3. Advance the probe watermark (absolute rows survive trims by
  //    construction — lineOffset grows in lockstep).
  const scanned = lineOffset + to
  if (scanned > state.scannedUpTo) state.scannedUpTo = scanned

  // 4. Bound memory: off-buffer entries are only ever looked up by rows
  //    currently on screen, so dropping the OLDEST numbers is safe.
  if (state.entries.size > MAX_ENTRIES) {
    const byNumber = [...state.entries.values()].sort((a, b) => a.number - b.number)
    const keep = new Set(byNumber.slice(byNumber.length - KEEP_ENTRIES).map((m) => m.number))
    for (const [key, meta] of state.entries) {
      if (!keep.has(meta.number)) state.entries.delete(key)
    }
  }
}

export interface RealignOptions {
  lineOffset: number
  /** Absolute cursor row after the reflow; rows below it are unwritten. */
  cursorAbs: number
  source: LineRegistryRowSource
}

/**
 * Re-key the registry after a resize reflow. Reflow redistributes physical
 * rows but never merges or reorders logical lines, so the logical line starts
 * currently in the buffer (non-wrapped content rows, top of buffer down to
 * the cursor) correspond, IN ORDER, to a suffix of the registered lines.
 *
 * Matching is anchored at the BOTTOM: the last start pairs with the newest
 * entry, the second-to-last with the second-newest, and so on. A few
 * unregistered lines (out-of-band repaints) can only shift lines ABOVE them —
 * the visible bottom of the screen always keeps its number/time.
 */
export function realignRegistry(state: LineRegistryState, opts: RealignOptions): void {
  const { lineOffset, cursorAbs, source } = opts
  const cursorRow = Math.max(0, cursorAbs - lineOffset)

  const starts: number[] = []
  for (let y = 0; y <= cursorRow; y += 1) {
    if (source.isWrapped(y)) continue
    if (state.entries.has(lineOffset + y) || source.hasContent(y)) starts.push(y)
  }

  const byNumber = [...state.entries.values()].sort((a, b) => a.number - b.number)

  const next = new Map<number, LineMetaEntry>()
  for (let i = 0; i < starts.length; i += 1) {
    // i counts from the BOTTOM of the buffer.
    const meta = byNumber[byNumber.length - 1 - i]
    if (meta) next.set(lineOffset + starts[starts.length - 1 - i], meta)
  }
  state.entries = next
  // realignRegistry itself probed every row from the buffer top to the
  // cursor, so the write scan's watermark can advance to match.
  const scanned = lineOffset + cursorRow
  if (scanned > state.scannedUpTo) state.scannedUpTo = scanned
}

/** Debounce window for resize-triggered realigns: dragging a split fires
 * onResize repeatedly, and each realign walks the whole buffer — with long
 * scrollback that stutters. One realign 150ms after the resize settles. */
export const REALIGN_DEBOUNCE_MS = 150

// Pending debounced realign per registry — keyed by state so concurrent
// terminals never delay each other.
const realignTimers = new WeakMap<LineRegistryState, ReturnType<typeof setTimeout>>()

/**
 * Debounced wrapper around realignRegistry for terminal.onResize. The
 * options are re-read at flush time so the settled buffer state (final
 * cols/rows, cursor) is what gets re-keyed, not the mid-drag snapshot.
 */
export function scheduleRealign(state: LineRegistryState, opts: () => RealignOptions): void {
  // A pending timer no-ops the previous one; clearTimeout accepts undefined.
  clearTimeout(realignTimers.get(state))
  realignTimers.set(state, setTimeout(() => {
    realignTimers.delete(state)
    // The terminal may have been released while the timer was pending —
    // its buffer rows are no longer readable; dropping the rekey is fine
    // because a disposed terminal's registry is discarded with it.
    try {
      realignRegistry(state, opts())
    } catch { /* disposed terminal: nothing left to realign */ }
  }, REALIGN_DEBOUNCE_MS))
}

/** Drop a pending debounced realign (terminal disposal). */
export function cancelScheduledRealign(state: LineRegistryState): void {
  const timer = realignTimers.get(state)
  if (timer !== undefined) {
    clearTimeout(timer)
    realignTimers.delete(state)
  }
}

// ─── Service functions (live terminal adaptation) ───────────────────────────

/**
 * Frame-coalesced gutter notification. Writes arrive in bursts (shell output,
 * TUI redraws): dispatching one window event per write made every pane's
 * gutter re-read the registry once per write. Batch the requesting session
 * ids instead and dispatch once per animation frame, carrying the ids so a
 * pane's gutter can ignore refreshes belonging to other sessions.
 */
export interface GutterCoalescer {
  request(sessionId: string): void
  /** Session ids waiting for the next flush. */
  pending(): string[]
}

export function createGutterCoalescer(
  schedule: (flush: () => void) => void,
  flush: (sessionIds: string[]) => void,
): GutterCoalescer {
  const pending = new Set<string>()
  let scheduled = false

  return {
    request(sessionId) {
      pending.add(sessionId)
      if (scheduled) return
      scheduled = true
      schedule(() => {
        scheduled = false
        const ids = [...pending]
        pending.clear()
        flush(ids)
      })
    },
    pending: () => [...pending],
  }
}

const gutterNotifier = createGutterCoalescer(
  (flush) => {
    // rAF in the renderer; the fallback covers environments without it (node
    // test runs) so a notification can never get stuck pending forever.
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => flush())
    else setTimeout(flush, 16)
  },
  (sessionIds) => {
    window.dispatchEvent(new CustomEvent('terminal:refresh-gutter', { detail: { sessionIds } }))
  },
)

// Ask the gutters of `sessionId`'s panes to re-read the (now updated)
// registry — coalesced to one dispatch per animation frame.
function notifyGutter(sessionId: string) {
  gutterNotifier.request(sessionId)
}

function timestampsEnabled(): boolean {
  return useSettingsStore().settings.terminal.showTimestamps ?? false
}

/** Absolute buffer row index of the cursor, compensating for scrollback trim. */
function absoluteCursorLine(sessionId: string, lineOffset: number): number {
  const managed = getManagedTerminal(sessionId)
  const t = managed?.terminal
  if (!t) return lineOffset
  const buf = t.buffer.active
  return lineOffset + buf.baseY + buf.cursorY
}

/**
 * Fold one applied write into the registry. Call this from the write's
 * completion callback, with `beforeAbs` captured before the write started.
 * No-op while an alternate-screen app owns the buffer — lines there are
 * transient and never join the registry.
 */
export function recordWrite(sessionId: string, beforeAbs: number, ts: number): void {
  const managed = getManagedTerminal(sessionId)
  if (!managed) return
  const t = managed.terminal
  const buf = t.buffer.active
  if (buf.type === 'alternate') return

  recordWrittenLines(managed.lineRegistry, {
    lineOffset: managed.lineOffset,
    beforeAbs,
    afterAbs: managed.lineOffset + buf.baseY + buf.cursorY,
    source: bufferRowSource(buf),
    now: ts,
  })
  notifyGutter(sessionId)
}

/**
 * Overwrite the current command line's timestamp with `ts` — called when the
 * user submits a command (Enter), so the prompt line records when it was
 * executed. Walks back through the wrapped group so a wrapped command shares
 * one timestamp.
 */
export function stampCommandLine(sessionId: string, ts: number): void {
  const managed = getManagedTerminal(sessionId)
  if (!managed) return
  const t = managed.terminal
  const buf = t.buffer.active
  if (buf.type === 'alternate') return

  const registry = managed.lineRegistry
  const offset = managed.lineOffset
  const cursorRow = buf.baseY + buf.cursorY

  let startRow = cursorRow
  while (startRow > 0 && buf.getLine(startRow)?.isWrapped) startRow -= 1

  const abs = offset + startRow
  const entry = registry.entries.get(abs)
  if (entry) {
    entry.ts = ts
  } else {
    // Normally the prompt line registered itself when the shell drew it; only
    // materialize it here if that somehow didn't happen and it has content.
    const line = buf.getLine(startRow)
    if (line?.translateToString?.(true)?.trim()) {
      registry.entries.set(abs, { number: registry.nextNumber++, ts } satisfies LineMetaEntry)
    }
  }
  if (timestampsEnabled()) notifyGutter(sessionId)
}

/** Return the cursor's absolute line index for the current terminal. */
export function currentAbsoluteLine(sessionId: string): number {
  const managed = getManagedTerminal(sessionId)
  return managed ? absoluteCursorLine(sessionId, managed.lineOffset) : 0
}

/**
 * Drop every registered line and restart numbering at 1 — used by the
 * "reset terminal output / clear scrollback" actions (issue #989): the
 * cleared buffer's surviving line is the cursor row, whose number would
 * otherwise keep counting from the pre-clear total.
 */
export function clearRegistry(sessionId: string): void {
  const managed = getManagedTerminal(sessionId)
  if (!managed) return
  managed.lineRegistry.nextNumber = 1
  managed.lineRegistry.entries.clear()
  // The cleared buffer's rows are new territory: the look-above window must
  // probe them again, so rewind the write-scan watermark.
  managed.lineRegistry.scannedUpTo = -1
  notifyGutter(sessionId)
}
