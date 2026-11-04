import type { Terminal } from '@xterm/xterm'

// xterm draws the IME preedit itself, in `.composition-view`: a div inside
// `.xterm-helpers` positioned from cell metrics measured at runtime. When that
// view ends up zero-sized, covered, or translated away from the cursor (font
// re-measure after a theme/size change, a scrolled container, a helper
// container the CSS moved), the user is left with the IME candidate window and
// no visible preedit: typing "shuru" shows only the candidates, so a typo can
// no longer be corrected before committing.
//
// This overlay renders the same composition string pinned to the terminal
// cursor and hides xterm's view while it is up, so the string is never drawn
// twice. It owns the preedit for the whole composition because it can do two
// things xterm's view cannot:
//
//   * clamp horizontally. `.terminal-host` clips with `overflow: clip` and the
//     custom scrollbar strip sits over the last columns, so a preedit that
//     starts near the right edge loses its tail — the user watches the pinyin
//     they typed disappear off the side of the terminal.
//   * use the cursor's *viewport* row. `buffer.cursorY` is relative to baseY,
//     so on a scrolled terminal xterm's own view (and a naive mirror) draws the
//     preedit rows above the cursor, or on screen for a cursor scrolled out of
//     view entirely.
//
// Every takeover is logged (see DEBUG_KEY) with the measured geometry, which
// is what identifies the cause in the field.

export interface Disposable {
  dispose(): void
}

/** Opt-in diagnostics: `localStorage.setItem('uniterm.debug.ime', '1')`. */
const DEBUG_KEY = 'uniterm.debug.ime'

type Rect = { left: number; top: number; width: number; height: number }

const noopDisposable: Disposable = { dispose() {} }

export function imeDebugEnabled(): boolean {
  try {
    return globalThis.localStorage?.getItem(DEBUG_KEY) === '1'
  } catch {
    return false
  }
}

/** A composition's text comes from `compositionupdate.data`, but some IMEs
 * leave it empty and only fill the helper textarea — prefer whichever carries
 * the characters. */
export function compositionText(data: string | null | undefined, textareaValue: string): string {
  return data && data.length > 0 ? data : textareaValue
}

/** xterm's own preedit is usable only when it actually paints inside the
 * terminal screen: a zero-sized or fully clipped box is invisible. */
export function isNativeCompositionVisible(native: Rect, host: Rect): boolean {
  if (native.width <= 0 || native.height <= 0) return false
  return native.left + native.width > host.left &&
    native.left < host.left + host.width &&
    native.top + native.height > host.top &&
    native.top < host.top + host.height
}

/**
 * The cursor's row inside the *visible* viewport.
 *
 * `buffer.cursorY` is `buffer.y`, which is relative to `baseY` rather than to
 * the top of the screen; the screen is never translated on scroll (xterm
 * repaints the rows), so a preedit row must be computed the same way
 * `Buffer.isCursorInViewport` does: absolute row minus `viewportY`.
 */
export function cursorViewportRow(baseY: number, cursorY: number, viewportY: number): number {
  return baseY + cursorY - viewportY
}

/**
 * Left edge for a preedit that must stay inside the screen.
 *
 * The caret's column is only a starting point: `cursorX * cellWidth` can land
 * past the right edge (a full line, stale cell metrics after a window drag or
 * a DPI change), and `.terminal-host`'s `overflow: clip` would then hide the
 * tail of whatever the user is typing. Pull the whole string back on screen
 * instead — a preedit that is not where the caret is beats one you cannot read.
 * Absent geometry skips the clamp; absurd cell metrics collapse to 0 rather
 * than to an unpositionable value.
 */
export function preeditLeft(
  cursorX: number,
  cellWidth: number,
  textWidth: number,
  screenWidth: number,
): number {
  const desired = Number.isFinite(cursorX) && Number.isFinite(cellWidth)
    ? Math.max(0, cursorX) * cellWidth
    : 0
  const safeDesired = Number.isFinite(desired) ? Math.max(0, desired) : 0
  // Unknown geometry means "do not clamp" rather than "collapse to zero":
  // the caret position is still better than nothing.
  if (!Number.isFinite(screenWidth) || !Number.isFinite(textWidth)) return safeDesired
  return Math.min(safeDesired, Math.max(0, screenWidth - textWidth))
}

function log(message: string) {
  if (!imeDebugEnabled()) return
  // Loaded on demand: the overlay lives in the terminal's hot path, and the
  // generated bindings pull the whole Wails runtime into the module graph.
  void import('../../bindings/github.com/ys-ll/uniterm/app')
    .then(bindings => bindings.FrontendLog('ime', message))
    .catch(() => {
      // Bindings are absent outside the Wails runtime (browser dev, tests).
    })
}

function rectOf(element: Element): Rect {
  const rect = element.getBoundingClientRect()
  return { left: rect.left, top: rect.top, width: rect.width, height: rect.height }
}

export function installImeCompositionOverlay(terminal: Terminal): Disposable {
  if (typeof document === 'undefined') return noopDisposable
  const root = terminal.element
  const textarea = root?.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')
  const screen = root?.querySelector<HTMLElement>('.xterm-screen')
  const nativeView = root?.querySelector<HTMLElement>('.composition-view')
  if (!root || !textarea || !screen) return noopDisposable

  const overlay = document.createElement('div')
  overlay.className = 'uniterm-ime-preedit'
  overlay.setAttribute('aria-hidden', 'true')
  overlay.style.cssText = [
    'position: absolute',
    'display: none',
    'white-space: pre',
    'pointer-events: none',
    'z-index: 6',
    'padding: 0 1px',
    'border-radius: 2px',
    'box-shadow: 0 0 0 1px currentColor',
  ].join(';')
  // Anchored to `.xterm-screen`, not the `.xterm` root: the screen is the text
  // area the cells are painted in, so screen-relative cell maths needs no
  // padding subtraction and cannot drift when the root's padding changes.
  screen.appendChild(overlay)

  let composing = false
  let hiddenNative: string | null = null
  let lastText = ''
  let anchorTimer: ReturnType<typeof setTimeout> | null = null

  const hideOverlay = (restoreNative = true) => {
    overlay.style.display = 'none'
    if (anchorTimer !== null) {
      clearTimeout(anchorTimer)
      anchorTimer = null
    }
    if (restoreNative && hiddenNative !== null && nativeView) {
      nativeView.style.visibility = hiddenNative
      hiddenNative = null
    }
  }

  // Keep xterm's view down while ours owns the preedit, so the string is
  // never drawn twice — including the cases where we deliberately draw
  // nothing (cursor scrolled out of the viewport).
  const suppressNative = () => {
    if (!nativeView) return
    if (hiddenNative === null) hiddenNative = nativeView.style.visibility
    nativeView.style.visibility = 'hidden'
  }

  const cellSize = (): { width: number; height: number } => {
    const core = (terminal as unknown as { _core?: { _renderService?: { dimensions?: { css?: { cell?: { width?: number; height?: number } } } } } })._core
    const cell = core?._renderService?.dimensions?.css?.cell
    if (cell && (cell.width ?? 0) > 0 && (cell.height ?? 0) > 0) {
      return { width: cell.width!, height: cell.height! }
    }
    // Fall back to the painted screen size: it is what the user sees, and it
    // needs no internals beyond the buffer dimensions already on the terminal.
    const rect = rectOf(screen)
    return {
      width: terminal.cols > 0 ? rect.width / terminal.cols : 0,
      height: terminal.rows > 0 ? rect.height / terminal.rows : 0,
    }
  }

  const render = (): string | null => {
    const text = compositionText(lastText, textarea.value)
    const buffer = terminal.buffer.active
    const cell = cellSize()
    const screenRect = rectOf(screen)
    if (
      text.length === 0 ||
      cell.width <= 0 ||
      cell.height <= 0 ||
      screenRect.width <= 0 ||
      terminal.rows <= 0
    ) {
      hideOverlay()
      return null
    }

    const row = cursorViewportRow(buffer.baseY, buffer.cursorY, buffer.viewportY)
    // The cursor is scrolled out of view: there is no caret to pin to, and
    // xterm's own view would paint the string at a stale row — so draw
    // nothing and keep its view down too.
    if (row < 0 || row >= terminal.rows) {
      hideOverlay(false)
      suppressNative()
      return null
    }

    const cursorX = Math.max(0, Math.min(buffer.cursorX, terminal.cols - 1))

    overlay.textContent = text
    // Laid out before measuring: the clamp needs the string's real width.
    overlay.style.display = 'block'
    const textWidth = overlay.getBoundingClientRect().width
    const left = preeditLeft(cursorX, cell.width, textWidth, screenRect.width)

    overlay.style.left = `${left}px`
    overlay.style.top = `${row * cell.height}px`
    overlay.style.height = `${cell.height}px`
    overlay.style.lineHeight = `${cell.height}px`
    overlay.style.fontFamily = terminal.options.fontFamily ?? 'monospace'
    overlay.style.fontSize = `${terminal.options.fontSize ?? 14}px`
    const theme = terminal.options.theme ?? {}
    overlay.style.background = theme.background ?? '#000'
    overlay.style.color = theme.foreground ?? '#fff'

    // Never draw the preedit twice: hide xterm's view while ours is up.
    suppressNative()

    // The IME anchors its candidate window to `.xterm-helper-textarea`, which
    // xterm parks at the caret — i.e. possibly under the same clipped right
    // edge. Re-point it at the string the user can actually read, after
    // xterm's own positioning (it schedules its own setTimeout(0) from
    // compositionupdate; this one is queued later, so it lands last).
    if (anchorTimer !== null) clearTimeout(anchorTimer)
    anchorTimer = setTimeout(() => {
      anchorTimer = null
      if (!composing) return
      textarea.style.left = `${left}px`
      textarea.style.top = `${row * cell.height}px`
      textarea.style.width = `${Math.max(textWidth, 1)}px`
      textarea.style.height = `${cell.height}px`
      textarea.style.lineHeight = `${cell.height}px`
    }, 0)

    if (!imeDebugEnabled()) return null
    const nativeRect = nativeView ? rectOf(nativeView) : null
    return (
      `preedit "${text}" at ${left},${row * cell.height} ` +
      `(cell=${cell.width}x${cell.height} cursorX=${cursorX} row=${row}) ` +
      `native=${nativeRect ? JSON.stringify(nativeRect) : 'none'} ` +
      `screen=${JSON.stringify(screenRect)}`
    )
  }

  const onCompositionStart = (event: CompositionEvent) => {
    composing = true
    lastText = event.data ?? ''
    const nativeRect = nativeView ? rectOf(nativeView) : null
    log(`composition start data=${JSON.stringify(event.data ?? '')} native=${nativeRect ? JSON.stringify(nativeRect) : 'none'} screen=${JSON.stringify(rectOf(screen))}`)
    // Do not fall back to the textarea here: it can still hold the previously
    // committed text until xterm clears it, which would flash as the preedit.
    if (lastText.length > 0) render()
    else hideOverlay()
  }

  const onCompositionUpdate = (event: CompositionEvent) => {
    if (!composing) {
      // Some IMEs emit updates without a start event in this webview.
      composing = true
      log(`composition update without start: ${JSON.stringify(event.data ?? '')}`)
    }
    lastText = event.data ?? ''
    // Once per IME keystroke, not once per cursor move: this line is what
    // identifies a misplaced preedit in the field.
    const geometry = render()
    if (geometry) log(geometry)
  }

  const onCompositionEnd = () => {
    composing = false
    lastText = ''
    hideOverlay()
    log('composition end')
  }

  const onInput = () => {
    // Composition keystrokes also land here; refresh the preedit when the IME
    // only maintains the textarea value.
    if (composing) render()
  }

  const onBlur = () => {
    composing = false
    lastText = ''
    hideOverlay()
  }

  // A resize or a scroll changes the cell grid under the caret while a
  // composition is live — the window-drag case in particular, where the
  // terminal re-fits up to 150ms after the drag ends. Without a re-render the
  // preedit keeps its stale offset and drifts away from the cursor.
  const rerenderIfComposing = () => {
    if (composing) render()
  }
  const resizeDispose = terminal.onResize(rerenderIfComposing)
  const scrollDispose = terminal.onScroll(rerenderIfComposing)
  const cursorDispose = terminal.onCursorMove(rerenderIfComposing)

  textarea.addEventListener('compositionstart', onCompositionStart, true)
  textarea.addEventListener('compositionupdate', onCompositionUpdate, true)
  textarea.addEventListener('compositionend', onCompositionEnd, true)
  textarea.addEventListener('input', onInput, true)
  textarea.addEventListener('blur', onBlur, true)

  return {
    dispose() {
      textarea.removeEventListener('compositionstart', onCompositionStart, true)
      textarea.removeEventListener('compositionupdate', onCompositionUpdate, true)
      textarea.removeEventListener('compositionend', onCompositionEnd, true)
      textarea.removeEventListener('input', onInput, true)
      textarea.removeEventListener('blur', onBlur, true)
      resizeDispose.dispose()
      scrollDispose.dispose()
      cursorDispose.dispose()
      hideOverlay()
      overlay.remove()
    },
  }
}
