import { Browser } from '@wailsio/runtime'

/**
 * External-link guard (F-09c).
 *
 * The app webview must never navigate away from the bundled UI: a page that
 * ends up in the WebView inherits the injected Wails runtime and every Go
 * binding — equivalent to remote code execution. All anchor clicks (including
 * markdown-rendered links in AI messages) and window.open calls are therefore
 * evaluated against the app origin: same-origin stays in the SPA, http(s) /
 * mailto open in the SYSTEM browser, everything else (javascript:, data:, …)
 * is blocked outright.
 */

export type LinkAction =
  | { kind: 'allow' }
  | { kind: 'open-external'; url: string }
  | { kind: 'block' }

/** decideLinkAction evaluates one link target against the app origin policy. */
export function decideLinkAction(href: string, appOrigin: string): LinkAction {
  if (!href || href.startsWith('#')) return { kind: 'allow' }
  let target: URL
  let base: URL
  try {
    target = new URL(href, appOrigin)
    base = new URL(appOrigin)
  } catch {
    return { kind: 'block' }
  }
  if (target.origin === base.origin) return { kind: 'allow' }
  if (target.protocol === 'http:' || target.protocol === 'https:' || target.protocol === 'mailto:') {
    return { kind: 'open-external', url: target.href }
  }
  return { kind: 'block' }
}

/** openExternalURL hands a URL to the OS handler (never the webview). */
export function openExternalURL(url: string): void {
  try {
    Browser.OpenURL(url)
  } catch {
    // a missing/failed OS handler must never break the app
  }
}

/** Minimal window shape the guard needs — injectable for tests. */
export interface LinkGuardWindow {
  location: { origin: string }
  open: (...args: unknown[]) => unknown
}

/** Minimal event-target shape (document / element) — injectable for tests. */
export interface LinkGuardTarget {
  addEventListener(type: string, listener: (ev: any) => void, options?: unknown): void
  removeEventListener(type: string, listener: (ev: any) => void, options?: unknown): void
}

function handleAnchorClick(ev: any, win: LinkGuardWindow): void {
  const el = ev?.target?.closest?.('a[href]')
  if (!el) return
  const action = decideLinkAction(el.getAttribute('href') || '', win.location.origin)
  if (action.kind === 'allow') return
  ev.preventDefault?.()
  ev.stopPropagation?.()
  if (action.kind === 'open-external') openExternalURL(action.url)
}

/**
 * installExternalLinkGuard wires the click/auxclick/window.open interception
 * and returns the cleanup function.
 */
export function installExternalLinkGuard(
  target: LinkGuardTarget = document as unknown as LinkGuardTarget,
  win: LinkGuardWindow = window as unknown as LinkGuardWindow,
): () => void {
  const onClick = (ev: any) => handleAnchorClick(ev, win)
  // Middle-click opens anchors via auxclick, not click.
  const onAuxClick = (ev: any) => {
    if (ev?.button === 1) handleAnchorClick(ev, win)
  }
  target.addEventListener('click', onClick, true)
  target.addEventListener('auxclick', onAuxClick, true)

  const originalOpen = win.open
  win.open = (...args: unknown[]) => {
    const href = typeof args[0] === 'string' || args[0] instanceof URL ? String(args[0]) : ''
    const action = decideLinkAction(href, win.location.origin)
    if (action.kind === 'open-external') openExternalURL(action.url)
    // Always refused as a browsing-context opener: nothing in the app
    // legitimately needs a popup, and returning null is the spec-sanctioned
    // "blocked" signal.
    return null
  }

  return () => {
    target.removeEventListener('click', onClick, true)
    target.removeEventListener('auxclick', onAuxClick, true)
    win.open = originalOpen
  }
}
