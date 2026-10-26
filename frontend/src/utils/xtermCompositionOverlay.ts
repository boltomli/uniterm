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
// cursor, but ONLY while the native view is not visibly rendered, so the two
// never draw the string twice. Every takeover is logged (see DEBUG_KEY) with
// the measured geometry, which is what identifies the cause in the field.

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
  root.appendChild(overlay)

  let composing = false
  let hiddenNative: string | null = null
  let lastText = ''

  const hideOverlay = () => {
    overlay.style.display = 'none'
    if (hiddenNative !== null && nativeView) {
      nativeView.style.visibility = hiddenNative
      hiddenNative = null
    }
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

  const render = () => {
    const text = compositionText(lastText, textarea.value)
    const buffer = terminal.buffer.active
    const cell = cellSize()
    const screenRect = rectOf(screen)
    const rootRect = rectOf(root)
    if (text.length === 0 || cell.width <= 0 || cell.height <= 0) {
      hideOverlay()
      return
    }

    const cursorX = Math.max(0, Math.min(buffer.cursorX, terminal.cols - 1))
    const left = Math.max(0, screenRect.left - rootRect.left + cursorX * cell.width)
    const top = screenRect.top - rootRect.top + buffer.cursorY * cell.height

    const theme = terminal.options.theme ?? {}
    const fontFamily = terminal.options.fontFamily ?? 'monospace'
    const fontSize = terminal.options.fontSize ?? 14
    overlay.textContent = text
    overlay.style.left = `${left}px`
    overlay.style.top = `${top}px`
    overlay.style.height = `${cell.height}px`
    overlay.style.lineHeight = `${cell.height}px`
    overlay.style.fontFamily = fontFamily
    overlay.style.fontSize = `${fontSize}px`
    overlay.style.background = theme.background ?? '#000'
    overlay.style.color = theme.foreground ?? '#fff'
    overlay.style.display = 'block'

    // Never draw the preedit twice: hide xterm's view while ours is up.
    if (nativeView) {
      if (hiddenNative === null) hiddenNative = nativeView.style.visibility
      nativeView.style.visibility = 'hidden'
    }
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
    if (imeDebugEnabled() && nativeView) {
      const nativeRect = rectOf(nativeView)
      if (!isNativeCompositionVisible(nativeRect, rectOf(screen))) {
        log(`native preedit not visible (${nativeRect.width}x${nativeRect.height} at ${nativeRect.left},${nativeRect.top}); overlay takes over`)
      }
    }
    render()
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
      hideOverlay()
      overlay.remove()
    },
  }
}
