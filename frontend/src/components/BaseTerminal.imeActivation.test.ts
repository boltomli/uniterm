// Windows IME regression: a word typed after the window is activated (Alt+Tab,
// taskbar, another app and back) could be committed more than once.
//
// The native move/size modal loop (WM_ENTERSIZEMOVE) was fixed by re-anchoring
// the WebView2 TSF client at drag end (see xtermImeCompatibility and
// app_windows.go). A plain window activation bounces native focus the same way,
// so app_windows.go now repairs it too: WM_ACTIVATE posts a private message
// that forces a fresh WM_SETFOCUS onto the WebView2 child. That half handles
// the TSF anchor; this frontend half handles xterm's transient input state (a
// compositionend dropped mid-switch, the send flags it leaves stuck, the
// helper textarea it left behind), which the native repair cannot see and
// which sends the next committed word through two send paths at once. The two
// halves are independent: the frontend one also runs when the WndProc hook was
// never installed.
//
// The repo has no component-mount tests (node env, no jsdom), so the wiring is
// asserted on the SFC source, same as BaseTerminal.cleanup.test.ts.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { hookBodies, scriptBody, readSrc } from '../testUtils/sfc'

const src = readSrc('components/BaseTerminal.vue')
const script = scriptBody(src)
const unmount = hookBodies(src, 'onUnmounted').join('\n')

/** The Windows-gated activation subscription block. */
const gate = (() => {
  const at = script.indexOf("if (detectPlatformSync() === 'windows') {")
  return at < 0 ? '' : script.slice(at, at + 700)
})()

describe('BaseTerminal.vue Windows IME re-anchor on window activation', () => {
  it('re-anchors from both the Wails activation event and the DOM focus event', () => {
    // Wails re-emits WM_ACTIVATE as windows:WindowActive / WindowClickActive;
    // the DOM focus event is the hook-independent safety net.
    expect(script).toMatch(/Events\.On\('windows:WindowActive'/)
    expect(script).toMatch(/Events\.On\('windows:WindowClickActive'/)
    expect(script).toMatch(/window\.addEventListener\('focus', onImeWindowFocus\)/)
    // Both feed the single shared handler and release their disposers.
    expect(script).toMatch(/const reanchor = \(\) => scheduleImeReanchor\(\)/)
    expect(script).toMatch(/unsubWindowsActivate = Events\.On\('windows:WindowActive', reanchor\)/)
    expect(script).toMatch(/unsubWindowsClickActivate = Events\.On\('windows:WindowClickActive', reanchor\)/)
  })

  it('only installs the Windows-specific repair on Windows', () => {
    expect(gate).toMatch(/detectPlatformSync\(\) === 'windows'/)
    expect(gate).toMatch(/Events\.On\('windows:WindowActive'/)
    expect(gate).toMatch(/Events\.On\('windows:WindowClickActive'/)
    expect(gate).toMatch(/window\.addEventListener\('focus', onImeWindowFocus\)/)
  })

  it('clears the stuck input state and re-focuses the helper textarea', () => {
    // resetXtermInputState cancels the deferred commit reads and clears the
    // flags; the re-focus makes WebView2 re-read the caret rect the IME
    // anchors its candidate window to.
    const reanchor = script.slice(script.indexOf('function reanchorImeAfterFocusChange()'))
    expect(reanchor).toMatch(/resetXtermInputState\(terminal\)/)
    expect(reanchor).toMatch(/textarea\.focus\(\{ preventScroll: true \}\)/)
  })

  it('does not rob a deliberate focus target or a dialog', () => {
    const reanchor = script.slice(
      script.indexOf('function reanchorImeAfterFocusChange()'),
      script.indexOf('function onSplitResizeStart()'),
    )
    expect(reanchor).toMatch(/tagName === 'INPUT' \|\| active\.tagName === 'TEXTAREA'/)
    expect(reanchor).toMatch(/isContentEditable/)
    expect(reanchor).toMatch(/closest\('\.window-controls'\)/)
    expect(reanchor).toMatch(/\[role="dialog"\], \.conn-context-menu/)
  })

  it('debounces the native and DOM events a single activation produces', () => {
    const reanchor = script.slice(
      script.indexOf('function reanchorImeAfterFocusChange()'),
      script.indexOf('function onSplitResizeStart()'),
    )
    expect(script).toMatch(/let imeReanchorAt = 0/)
    expect(reanchor).toMatch(/const now = Date\.now\(\)/)
    expect(reanchor).toMatch(/now - imeReanchorAt < 100/)
  })

  it('runs the repair off the activation turn and cancels it on unmount', () => {
    // A synchronous focus() at activation can race a native dialog tearing
    // down (see useFocusTerminal); defer by a macrotask and keep one pending
    // timer for the Wails + DOM pair a single Alt+Tab produces.
    expect(script).toMatch(/function scheduleImeReanchor\(\)/)
    expect(script).toMatch(/if \(imeReanchorTimer !== null\) return/)
    expect(script).toMatch(/imeReanchorTimer = setTimeout\(\(\) => \{/)
    expect(unmount).toMatch(/clearTimeout\(imeReanchorTimer\)/)
  })

  it('releases the listeners on unmount', () => {
    expect(unmount).toMatch(/unsubWindowsActivate\?\.\(\)/)
    expect(unmount).toMatch(/unsubWindowsClickActivate\?\.\(\)/)
    expect(unmount).toMatch(/window\.removeEventListener\('focus', onImeWindowFocus\)/)
  })
})

// Cross-language guard (same pattern as types/settings.schema.test.ts): the
// frontend half clears xterm's state, but only app_windows.go can re-anchor
// Chromium's TSF client by forcing a fresh WM_SETFOCUS onto the WebView2 child.
// That native repair is what makes a window switch behave like a drag, so it
// must not be dropped while the frontend wiring above stays in place.
const windowsSrc = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../app_windows.go'), 'utf-8')

describe('app_windows.go native IME re-anchor on window activation', () => {
  it('captures the focused descendant when the window is activated', () => {
    expect(windowsSrc).toMatch(/case WM_ACTIVATE:/)
    expect(windowsSrc).toMatch(/rememberChildFocus\(uintptr\(hwnd\)\)/)
  })

  it('defers the repair past the activation message', () => {
    // A SetFocus issued from inside WM_ACTIVATE can be swallowed while window
    // ownership is still settling, so the repair is posted to self and runs on
    // the next message-loop turn.
    expect(windowsSrc).toMatch(/wmAppReanchorIme = 0x8000 \+ 1/)
    expect(windowsSrc).toMatch(/case wmAppReanchorIme:/)
    expect(windowsSrc).toMatch(/procPostMessage\.Call\(uintptr\(hwnd\), wmAppReanchorIme, childFocus, 0\)/)
  })

  it('forces a real focus transition on the child, never outside the window', () => {
    expect(windowsSrc).toMatch(/func reanchorWebviewFocus\(hwnd, remembered uintptr\)/)
    // The child already owning focus is the broken state a switch leaves:
    // a plain SetFocus would be a no-op, so focus is dropped to the thread
    // queue first to force WM_KILLFOCUS/WM_SETFOCUS.
    expect(windowsSrc).toMatch(/if focused, _, _ := procGetFocus\.Call\(\); focused == target \{\s*procSetFocus\.Call\(0\)/)
    // Only descendants of this window are ever targeted.
    expect(windowsSrc).toMatch(/if child, _, _ := procIsChild\.Call\(hwnd, target\); child == 0 \{/)
    expect(windowsSrc).toMatch(/if target == 0 \|\| target == hwnd \{/)
  })
})
