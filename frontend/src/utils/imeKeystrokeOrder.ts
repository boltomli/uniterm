// Vietnamese input methods on macOS edit text that is already on the line:
// typing "ee" turns the previous "e" into "ê". They express that as a delete
// plus an insert, but the two do not always reach the page in that order.
//
// Measured in uniTerm with VietTelex (an InputMethodKit method), typing
// "tieengs vieejt":
//
//   keydown 'e'   -> onData 'e'    line "tie"
//   keydown 'ê'   -> onData 'ê'    line "tieê"   <- replacement arrives first
//   keydown Bksp  -> onData '\x7f' line "tie"    <- delete lands after it
//
// The delete was meant for the old "e"; arriving late it removes the "ê" that
// was just inserted, so both are lost and "tiếng" comes out as "tieến". The
// text insertion and the synthesized delete travel different paths into
// WKWebView, and their relative order is not preserved.
//
// Rather than guess at an order, collect the keystrokes an input method emits
// for one keypress and apply the intent they encode: delete N characters, then
// insert the replacement. Order within the burst stops mattering.
//
// ponytail: a quiet-period heuristic, tuned to bursts a human cannot produce.
// A keypress pair 15ms apart is ~4000 WPM, so only a machine lands two events
// inside one window. If an input method ever spreads one edit across a longer
// pause, raise QUIET_MS; the ceiling is that a real pause splits the burst.

const DEL = '\x7f'
const QUIET_MS = 15
// How long a rewrite keeps the buffer engaged after the last accented
// character. Telex rewrites are usually marked by one — "aa" gives "â" — but
// the one that undoes a rewrite is not: from "hí", typing "s" again restores
// "his", a delete plus the pure ASCII "is". Treating anything typed shortly
// after an accented character as part of the same Vietnamese run catches it,
// while English typing never opens a burst and never pays the wait.
const IME_ACTIVE_MS = 3000
// Control characters end a burst: Enter, Ctrl-C, arrow keys and escape
// sequences are the user's own input, never part of an input method's rewrite,
// and reordering around them would corrupt real keystrokes.
const PRINTABLE = /^[^\p{C}]+$/u

export interface KeystrokeReorder {
  /** Feed one chunk from terminal.onData. */
  push(data: string): void
  dispose(): void
}

/** The platforms whose input methods actually need reordering. */
function isEventTapPlatform(): boolean {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.userAgent)
}

/**
 * The reorder buffer for the current platform.
 *
 * The mechanism above exists for macOS event-tap IMEs (XKey/EVKey/OpenKey):
 * they edit text already on the line and WKWebView delivers their delete and
 * replacement through different paths in an order the page does not control.
 *
 * Everywhere else it is pure cost. The buffer only engages after a non-ASCII
 * character, and it stays armed for IME_ACTIVE_MS — so on Windows every
 * keystroke following a Chinese/Japanese commit is held for QUIET_MS, and a
 * DEL chunk gets hoisted ahead of text it was never meant to precede (a
 * genuine `rm -f x` backspace-then-insert would be reordered). No known
 * rewrite IME needs it off macOS, so pass those keystrokes straight through.
 */
export function createImeKeystrokeReorder(
  emit: (data: string) => void,
): KeystrokeReorder {
  if (!isEventTapPlatform()) {
    return {
      push: (data: string) => emit(data),
      dispose() {},
    }
  }
  return createKeystrokeReorder(emit)
}

export function createKeystrokeReorder(
  emit: (data: string) => void,
  quietMs: number = QUIET_MS,
): KeystrokeReorder {
  let burst: string[] = []
  let timer: ReturnType<typeof setTimeout> | null = null
  let imeActiveUntil = 0

  const flush = () => {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
    if (burst.length === 0) return
    const chunks = burst
    burst = []
    // Deletes first, then the replacement text, whatever order they arrived in.
    const deletes = chunks.reduce((n, chunk) => n + (chunk === DEL ? 1 : 0), 0)
    const text = chunks.filter(chunk => chunk !== DEL).join('')
    if (deletes > 0) emit(DEL.repeat(deletes))
    if (text) emit(text)
  }

  const arm = () => {
    if (timer !== null) clearTimeout(timer)
    timer = setTimeout(() => {
      timer = null
      flush()
    }, quietMs)
  }

  return {
    push(data: string) {
      const isDelete = data === DEL
      const isText = PRINTABLE.test(data)
      // eslint-disable-next-line no-control-regex
      if (/[^\x00-\x7F]/.test(data)) {
        imeActiveUntil = Date.now() + IME_ACTIVE_MS
      }

      // Nothing buffered and no Vietnamese typing in flight: the common path,
      // where plain typing must not pay a millisecond of latency.
      if (burst.length === 0 && Date.now() >= imeActiveUntil) {
        emit(data)
        return
      }

      // Control input is the user's own: Enter, Ctrl-C, arrow keys, escape
      // sequences. Close the burst in intent order, let the keystroke follow
      // untouched, and stand down — the line is over, so the next keystrokes
      // should not pay the wait.
      if (!isDelete && !isText) {
        flush()
        imeActiveUntil = 0
        emit(data)
        return
      }

      burst.push(data)
      arm()
    },
    dispose() {
      flush()
    },
  }
}
