// Reassemble DCS (Device Control String) sequences — `ESC P … ESC \` — that
// arrive split across `session:data` chunks.
//
// xterm.js itself assembles split escape sequences across writes, but it
// never sees a coherent chunk when the sixel payload is split: the highlight
// pass (useHighlight) segments per chunk, and a payload half it does not
// recognize as part of `ESC P` is treated as plain text — brace/pipe
// characters inside the sixel body (`{ } |` are valid sixel data bytes) get
// SGR color codes injected *inside* the DCS sequence, corrupting the image.
//
// This scanner holds bytes of an open DCS until its terminator arrives, and
// hands back the complete sequence as a marked segment so the caller can
// bypass highlighting for it. Everything outside DCS bodies passes through
// untouched, chunk-boundary-agnostic.

export interface DataSegment {
  text: string
  /** true: a complete (or explicitly aborted) DCS sequence — must bypass highlight */
  dcs: boolean
}

// A DCS is aborted by CAN (0x18) or SUB (0x1A) per the VT spec. Global so
// exec() can resume from a saved cursor instead of rescanning the buffer.
const ABORT_RE = /[\x18\x1a]/g

// Upper bound on a buffered DCS body. A malformed `ESC P` with no terminator
// would otherwise hold back every later byte while the buffer grows without
// bound — past this cap the buffer is flushed as a marked segment and normal
// scanning resumes. 4 MiB comfortably exceeds any real sixel image.
export const MAX_DCS_BUFFER = 4 * 1024 * 1024

export class DcsReassembler {
  private buf = ''
  private inDcs = false
  /** Index in buf from which terminator scanning resumes. Bytes below it
   * were already examined by previous feeds and contain no ESC/CAN/SUB that
   * starts a terminator, so each feed only scans newly appended bytes —
   * rescanning the whole buffer per chunk was O(n²) on large sixel bodies. */
  private scanPos = 0

  /** Feed one chunk; returns the segments that are safe to write now. */
  feed(chunk: string): DataSegment[] {
    return this.feedInner(chunk).filter(s => s.text !== '')
  }

  private feedInner(chunk: string): DataSegment[] {
    const out: DataSegment[] = []
    if (!this.inDcs) {
      this.buf += chunk
      const start = this.buf.indexOf('\x1bP')
      if (start === -1) {
        // A trailing lone ESC may be the first half of an `ESC P`
        // introducer (or of an ST) — hold it back, emit the rest.
        if (this.buf.endsWith('\x1b')) {
          out.push({ text: this.buf.slice(0, -1), dcs: false })
          this.buf = '\x1b'
        } else {
          out.push({ text: this.buf, dcs: false })
          this.buf = ''
        }
        return out
      }
      out.push({ text: this.buf.slice(0, start), dcs: false })
      this.buf = this.buf.slice(start)
      this.inDcs = true
      this.scanPos = 2 // buf[0..2) is the `ESC P` introducer
    } else {
      this.buf += chunk
    }

    // Size cap: an unterminated DCS must not hold back (or grow with) all
    // later output — flush what we buffered (marked, so highlighting stays
    // off the binary body) and resume plain scanning from the next feed.
    if (this.buf.length > MAX_DCS_BUFFER) {
      out.push({ text: this.buf, dcs: true })
      this.buf = ''
      this.inDcs = false
      this.scanPos = 0
      return out
    }

    // this.buf starts with `ESC P`; scan for the terminator from the resume
    // cursor so an `ESC` inside the params is handled too. Only bytes at
    // scanPos and beyond are new — earlier ones were examined already.
    for (;;) {
      const esc = this.buf.indexOf('\x1b', this.scanPos)
      ABORT_RE.lastIndex = this.scanPos
      const abort = ABORT_RE.exec(this.buf)
      const abortAt = abort ? abort.index : -1
      const at = esc === -1 ? abortAt : abortAt === -1 ? esc : Math.min(esc, abortAt)
      if (at === -1) {
        // Everything appended so far is DCS body: resume after it next feed.
        this.scanPos = this.buf.length
        return out
      }
      const next = this.buf[at + 1]
      if (next === undefined) {
        // Trailing ESC/CAN/SUB: ambiguous, keep buffering — resume AT this
        // byte so its resolution gets examined when the next chunk arrives.
        this.scanPos = at
        return out
      }
      if (next === '\\' && this.buf.charCodeAt(at) === 0x1b) {
        out.push({ text: this.buf.slice(0, at + 2), dcs: true })
        this.buf = this.buf.slice(at + 2)
        this.inDcs = false
        this.scanPos = 0
        return out.concat(this.resume())
      }
      // ESC + anything else (or CAN/SUB): the DCS never terminated
      // properly. Flush what we buffered (marked, so highlight still stays
      // off the binary body) and hand the remainder back to normal scanning.
      out.push({ text: this.buf.slice(0, at), dcs: true })
      this.buf = this.buf.slice(at)
      this.inDcs = false
      this.scanPos = 0
      return out.concat(this.resume())
    }
  }

  // Resume idle scanning over whatever is left in buf after a DCS ended.
  private resume(): DataSegment[] {
    if (!this.buf) return []
    return this.feedInner('')
  }

  /** Drop any pending state (e.g. session reset). */
  reset(): void {
    this.buf = ''
    this.inDcs = false
    this.scanPos = 0
  }
}
