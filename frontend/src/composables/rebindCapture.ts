// Capture-phase keyboard listener used while the user rebinds a shortcut
// (SettingsTab). The listener swallows every keystroke (preventDefault +
// stopPropagation) so a chord never reaches the app's own handlers — which
// means it MUST NOT outlive the rebind session: the Settings tab lives in
// <KeepAlive>, and a deactivated tab that keeps swallowing keys breaks the
// whole app until it is reopened (security.md component chapter #2).
//
// Registration and removal are a symmetric start/stop pair: start is
// idempotent (no stacked listeners) and stop always leaves zero listeners
// behind, so every exit path (Escape, save, clear, tab switch, unmount) can
// call it unconditionally.

export interface CaptureTargets {
  document: {
    addEventListener(type: string, listener: (e: Event) => void, options?: unknown): void
    removeEventListener(type: string, listener: (e: Event) => void, options?: unknown): void
  }
  window: {
    addEventListener(type: string, listener: (e: Event) => void, options?: unknown): void
    removeEventListener(type: string, listener: (e: Event) => void, options?: unknown): void
  }
}

export interface RebindCaptureHandlers {
  onKeydown: (e: KeyboardEvent) => void
  onBlur: (e: Event) => void
}

export interface RebindCapture {
  isActive(): boolean
  start(): void
  stop(): void
}

export function createRebindCapture(targets: CaptureTargets, handlers: RebindCaptureHandlers): RebindCapture {
  let active = false
  // The DOM hands the listener an Event; for a 'keydown' registration it is
  // always a KeyboardEvent, which is what the rebind handler inspects.
  const onKeydown = (e: Event) => handlers.onKeydown(e as KeyboardEvent)

  return {
    isActive: () => active,

    start() {
      if (active) return
      active = true
      // Capture phase: the rebind must win over every other keydown handler.
      targets.document.addEventListener('keydown', onKeydown, true)
      targets.window.addEventListener('blur', handlers.onBlur)
    },

    stop() {
      if (!active) return
      active = false
      targets.document.removeEventListener('keydown', onKeydown, true)
      targets.window.removeEventListener('blur', handlers.onBlur)
    },
  }
}
