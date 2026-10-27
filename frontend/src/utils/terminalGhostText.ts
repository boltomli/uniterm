import type { Terminal } from '@xterm/xterm'

// Inline suggestion hint for the terminal, fish / zsh-autosuggestions style:
// the rest of the matched command drawn dimmed, starting at the cursor cell.
//
// The element lives inside xterm's own `.xterm-screen`, which is already
// position:relative, so cell coordinates place it on the character grid with
// nothing to measure. This is the same placement trick xterm uses for its IME
// composition view.
//
// Font and colour are a different matter: xterm hangs the font off .xterm-rows
// and the colour off the per-cell spans within it, so an element appended to
// .xterm-screen inherits neither and must copy both.
//
// Nothing is ever written to the PTY: the hint is a local overlay, so there is
// no echo to undo and no way for it to desync the shell's own line state.

export interface GhostText {
  /** Draw `text` at the cursor. An empty string hides the hint. */
  set(text: string): void
  dispose(): void
}

interface GhostCandidate {
  type: string
  value: string
}

/**
 * The suggestion whose text literally continues `typed`, so its tail can be
 * drawn after the cursor — or null when nothing can be hinted.
 *
 * The prefix test is case-sensitive on purpose. History matching is
 * case-insensitive, so "GIT st" also offers "git status": a fine popup entry,
 * but hinting "atus" after it would draw a command nobody could have typed.
 *
 * AI items are excluded: 'ai-preview' carries no command at all, and
 * 'ai-result' arrives seconds later, which would make the hint flicker in
 * after the user has moved on.
 */
export function pickGhostSuggestion<T extends GhostCandidate>(
  items: readonly T[],
  typed: string,
): T | null {
  if (!typed) return null
  return items.find(item =>
    (item.type === 'history' || item.type === 'quick-command') &&
    item.value.length > typed.length &&
    item.value.startsWith(typed),
  ) ?? null
}

const noopGhostText: GhostText = { set() {}, dispose() {} }

interface XtermGeometryInternals {
  buffer?: { x: number; y: number }
  _renderService?: { dimensions?: { css?: { cell?: { width: number; height: number } } } }
}

interface CellGeometry {
  left: number
  top: number
  height: number
}

function cellGeometry(terminal: Terminal): CellGeometry | null {
  const core = (terminal as Terminal & { _core?: XtermGeometryInternals })._core
  const buffer = core?.buffer
  const cell = core?._renderService?.dimensions?.css?.cell
  if (!buffer || !cell?.width || !cell?.height) return null
  return {
    left: buffer.x * cell.width,
    top: buffer.y * cell.height,
    height: cell.height,
  }
}

export function installGhostText(terminal: Terminal): GhostText {
  const screen = terminal.element?.querySelector('.xterm-screen') as HTMLElement | null
  if (!screen) return noopGhostText
  // xterm puts the font on .xterm-rows and the colour on the per-cell spans
  // inside it. Neither is an ancestor of an element appended to .xterm-screen,
  // so nothing here is inherited — see applyAppearance.
  const rows = terminal.element?.querySelector('.xterm-rows') as HTMLElement | null

  const element = document.createElement('div')
  // aria-hidden: the hint is a visual affordance, and the screen reader already
  // announces the characters the user actually typed.
  element.setAttribute('aria-hidden', 'true')
  element.style.cssText = [
    'position:absolute',
    'pointer-events:none',
    'user-select:none',
    'white-space:pre',
    // Dim by opacity rather than a fixed colour so the hint stays legible on
    // every theme, including the user's custom ones. 0.4 was too faint to
    // notice at an 11px terminal font; 0.55 still reads as a hint next to
    // full-strength text.
    'opacity:0.55',
    'z-index:2',
    'display:none',
  ].join(';')
  screen.appendChild(element)

  // The hint body and the accept cue are separate nodes so the cue can be
  // dimmer than the body without touching it.
  const textNode = document.createTextNode('')
  element.appendChild(textNode)
  // Fish shows a small → after its ghost text for the same reason: without an
  // on-screen affordance there is no way to discover that → accepts the hint.
  const cue = document.createElement('span')
  cue.textContent = '→'
  cue.style.cssText = 'margin-left:0.5em'
  element.appendChild(cue)

  let text = ''

  // Copy what the overlay cannot inherit. Left out, the hint renders as black
  // 16px Times: invisible on a dark theme, and too wide to sit on the character
  // grid. Read fresh each time so changing font or theme carries over.
  const applyAppearance = () => {
    const options = terminal.options
    element.style.fontFamily = options.fontFamily ?? 'monospace'
    element.style.fontSize = `${options.fontSize ?? 14}px`
    element.style.fontWeight = String(options.fontWeight ?? 'normal')
    // xterm nudges letter-spacing to make characters land exactly one cell
    // apart; without the same nudge a long hint drifts off the grid. It is an
    // inline style, so this is a property read rather than a style recalc.
    element.style.letterSpacing = rows?.style.letterSpacing ?? ''
    // getComputedStyle is the fallback only — themes normally carry an explicit
    // foreground, and the `||` keeps the style recalc out of the common path.
    element.style.color =
      options.theme?.foreground || (rows ? getComputedStyle(rows).color : '#ffffff')
  }

  const render = () => {
    const geometry = text ? cellGeometry(terminal) : null
    if (!geometry) {
      element.style.display = 'none'
      return
    }
    textNode.data = text
    applyAppearance()
    element.style.left = `${geometry.left}px`
    element.style.top = `${geometry.top}px`
    element.style.lineHeight = `${geometry.height}px`
    element.style.display = ''
  }

  // The hint is placed from the cursor cell, and the cursor moves on its own:
  // the shell echoes the keystroke after set() has already run, and arriving
  // output scrolls the viewport under a hint that is already on screen. Both
  // raise onCursorMove, so re-placing there keeps the hint glued to the cursor.
  //
  // onRender would read like the better signal but never fires in this xterm
  // fork — measured in a browser: two content-changing writes produced zero
  // onRender events and one onCursorMove each.
  const renderDispose = terminal.onCursorMove(render)

  return {
    set(next: string) {
      if (next === text) {
        // Same text, but the cursor may have moved — reposition, skip the
        // textContent write that would invalidate layout for nothing.
        if (text) render()
        return
      }
      text = next
      render()
    },
    dispose() {
      renderDispose.dispose()
      element.remove()
    },
  }
}
