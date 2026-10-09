import type { Terminal } from '@xterm/xterm'

// IME compatibility patch for xterm.js on macOS (WKWebView).
//
// With an IME active, WKWebView can report ordinary keystrokes as keydown
// keyCode=229 even when no composition is active. xterm then diverts the
// character to its deferred textarea-diff path, which can drop characters.
// Force eligible single-character insertText events through xterm's direct
// path and rewind the textarea so the deferred diff cannot send them twice.
//
// After switching a Chinese IME to English mode with Caps Lock, WKWebView can
// also emit the input event before its corresponding keyCode-229 keydown. If
// that input event was already delivered, the late keydown must not inject a
// fallback. If no usable input event was delivered, the keydown fallback
// keeps the character from being swallowed.
//
// Some IMEs (e.g. Doubao) swallow keypress for Shift+letter without reporting
// keyCode 229, so uppercase had no delivery path left at all. xterm skips its
// direct path whenever `composed && _keyDownSeen`, so that is the condition we
// key off, rather than inferring it from an IME-specific keyCode.
//
// Windows (WebView2) is covered by installWindowsImePatch and
// resetXtermInputState — see their comments.

export interface Disposable {
  dispose(): void
}

interface XtermCompositionHelperInternals {
  _isComposing?: unknown
  _isSendingComposition?: unknown
  isComposing?: unknown
  _compositionPosition?: { start?: number; end?: number }
  _dataAlreadySent?: string
  compositionupdate?: (
    this: XtermCompositionHelperInternals,
    ev: Pick<CompositionEvent, 'data'>,
  ) => void
  _finalizeComposition?: (
    this: XtermCompositionHelperInternals,
    waitForPropagation: boolean,
  ) => void
}

interface XtermCoreInternals {
  _inputEvent?: (this: XtermCoreInternals, ev: InputEvent) => boolean
  _keyDownSeen?: boolean
  _keyDownHandled?: boolean
  _keyPressHandled?: boolean
  _unprocessedDeadKey?: boolean
  _compositionHelper?: XtermCompositionHelperInternals
  _helperContainer?: {
    querySelector?: (selector: string) => { classList: { remove: (token: string) => void } } | null
  } | null
  textarea?: {
    value: string
    addEventListener: (type: string, listener: EventListener, capture?: boolean) => void
    removeEventListener: (type: string, listener: EventListener, capture?: boolean) => void
    blur?: () => void
    focus?: () => void
  } | null
}

type TerminalWithCore = Terminal & { _core?: XtermCoreInternals }

interface PendingFallback {
  character: string
  textareaValueBeforeKeydown: string
  createdAt: number
  injected: boolean
  timeoutId: ReturnType<typeof setTimeout>
}

interface DeliveredInput {
  character: string
  deliveredAt: number
}

const PRINTABLE_ASCII = /^[\x20-\x7E]$/
const LATE_EVENT_WINDOW_MS = 250
const noopDisposable: Disposable = { dispose() {} }

function isMacPlatform(): boolean {
  return /Mac|iPhone|iPad/.test(navigator.userAgent)
}

function isCompositionActive(helper: XtermCompositionHelperInternals): boolean {
  return (
    helper._isComposing === true ||
    helper._isSendingComposition === true ||
    helper.isComposing === true
  )
}

function isSameCharacter(left: string, right: string): boolean {
  return left.toLowerCase() === right.toLowerCase()
}

// Windows/WebView2: a native window drag runs a modal loop (WM_ENTERSIZEMOVE)
// that reorders and drops IME events, and xterm delivers a committed word
// through more than one path — the deferred textarea read queued on
// compositionend, the immediate send of the keydown finalize (Enter/Space
// confirming a candidate), and the direct insertText send in _inputEvent
// (reached when the commit's keyup beat its input event, or the commit came
// from an IME candidate click with no keydown at all). When two paths fire for
// one commit the word reaches the PTY twice, and a dropped compositionstart
// leaves _compositionPosition at an old offset so later commits re-send
// everything accumulated in the textarea. Three guards, one per ordering:
//
// - compositionupdate re-syncs the composition region when compositionstart
//   was dropped;
// - the keydown finalize marks its region as sent (xterm's own
//   _dataAlreadySent mechanism, xterm.js issue #3191) so a compositionend read
//   arriving after it cannot send the same text again;
// - _inputEvent drops an insertText whose data the composition read has just
//   delivered (Chromium can order compositionend before the commit's input).
function installWindowsImePatch(terminal: Terminal): Disposable {
  const core = (terminal as TerminalWithCore)._core
  const helper = core?._compositionHelper
  if (!core || !helper || typeof core._inputEvent !== 'function' || !core.textarea) {
    return noopDisposable
  }

  const textarea = core.textarea
  const disposers: (() => void)[] = []
  // Last compositionupdate data: the committed text of a composition xterm
  // never saw is the tail that update describes.
  let lastCompositionData = ''
  let lastCompositionCommitAt = 0

  if (typeof helper.compositionupdate === 'function') {
    const originalCompositionUpdate = helper.compositionupdate
    const patchedCompositionUpdate = function patchedCompositionUpdate(
      this: XtermCompositionHelperInternals,
      ev: Pick<CompositionEvent, 'data'>,
    ): void {
      lastCompositionData = typeof ev?.data === 'string' ? ev.data : ''
      if (this._isComposing !== true && this._isSendingComposition !== true) {
        // compositionstart was dropped: rebase the region to this update, the
        // way compositionstart would have, so the compositionend read delivers
        // only the new text instead of the whole accumulated textarea value.
        this._isComposing = true
        this._isSendingComposition = false
        this._dataAlreadySent = ''
        const start = Math.max(0, textarea.value.length - lastCompositionData.length)
        if (this._compositionPosition) this._compositionPosition.start = start
        else this._compositionPosition = { start, end: start }
      }
      originalCompositionUpdate.call(this, ev)
    }
    helper.compositionupdate = patchedCompositionUpdate
    disposers.push(() => {
      if (helper.compositionupdate === patchedCompositionUpdate) {
        helper.compositionupdate = originalCompositionUpdate
      }
    })
  }

  if (typeof helper._finalizeComposition === 'function') {
    const originalFinalize = helper._finalizeComposition
    const patchedFinalize = function patchedFinalize(
      this: XtermCompositionHelperInternals,
      waitForPropagation: boolean,
    ): void {
      const start = this._compositionPosition?.start ?? 0
      const region = textarea.value.substring(start)
      originalFinalize.call(this, waitForPropagation)
      lastCompositionCommitAt = Date.now()
      if (!waitForPropagation && region.length > 0) {
        // The keydown finalize sends substring(start, end) immediately but
        // leaves the text in the textarea, so a compositionend landing after
        // it would schedule a second send of the same text. Record the whole
        // region as sent: xterm's _dataAlreadySent advances the deferred read
        // past it (and past the trailing characters their own key paths send).
        this._dataAlreadySent = region
      }
    }
    helper._finalizeComposition = patchedFinalize
    disposers.push(() => {
      if (helper._finalizeComposition === patchedFinalize) {
        helper._finalizeComposition = originalFinalize
      }
    })
  }

  const originalInputEvent = core._inputEvent
  const patchedInputEvent = function patchedInputEvent(
    this: XtermCoreInternals,
    ev: InputEvent,
  ): boolean {
    if (
      ev.inputType === 'insertText' &&
      typeof ev.data === 'string' &&
      ev.data.length > 0
    ) {
      const start = this._compositionHelper?._compositionPosition?.start ?? 0
      const region = (this.textarea ?? textarea).value.substring(start)
      if (region.includes(ev.data)) {
        const inFlight =
          this._compositionHelper?._isSendingComposition === true ||
          this._compositionHelper?._isComposing === true
        // Either the composition path owns this text and its pending read will
        // deliver the whole region, or the read just did (the region still
        // holds exactly what was delivered) — either way, sending it again
        // from the direct path would commit the word twice.
        if (inFlight || (Date.now() - lastCompositionCommitAt <= LATE_EVENT_WINDOW_MS && region === ev.data)) {
          return true
        }
      }
    }
    return originalInputEvent!.call(this, ev)
  }
  core._inputEvent = patchedInputEvent

  return {
    dispose() {
      if (core._inputEvent === patchedInputEvent) {
        core._inputEvent = originalInputEvent
      }
      for (const disposer of disposers) disposer()
      disposers.length = 0
    },
  }
}

// Reset every transient xterm input state to the "nothing in flight"
// baseline. xterm routes each keystroke through exactly one send path
// (keydown / keypress / input / composition finalize), gated by mutually
// exclusive flags; the WM_ENTERSIZEMOVE modal loop can drop or reorder key
// events and leave those flags stuck, after which one keystroke goes out
// through several paths at once (duplicate/triplicate input). Safe at
// drag/resize boundaries and when the terminal is hidden — the user is not
// typing at those moments. `blur: false` keeps the textarea focused (drag
// start) while still clearing its value, so a late compositionend read finds
// nothing to send; `blur: true` (the default) also ends any OS-level
// composition.
export function resetXtermInputState(
  terminal: Terminal | null | undefined,
  opts: { blur?: boolean } = {},
): void {
  if (!terminal) return
  const core = (terminal as TerminalWithCore)._core
  const textarea = core?.textarea
  if (core) {
    core._keyDownHandled = false
    core._keyPressHandled = false
    core._keyDownSeen = false
    core._unprocessedDeadKey = false
  }
  const helper = core?._compositionHelper
  if (helper) {
    helper._isSendingComposition = false
    helper._isComposing = false
    helper._dataAlreadySent = ''
    helper._compositionPosition = { start: 0, end: 0 }
    const compositionView = core?._helperContainer?.querySelector?.('.composition-view')
    compositionView?.classList.remove('active')
  }
  if (textarea) {
    textarea.value = ''
    if (opts.blur !== false) textarea.blur?.()
  }
}

export function installImeCompatibilityPatch(terminal: Terminal): Disposable {
  if (isMacPlatform()) {
    return installMacImePatch(terminal)
  }
  if (/Windows/i.test(navigator.userAgent)) {
    return installWindowsImePatch(terminal)
  }
  return noopDisposable
}

function installMacImePatch(terminal: Terminal): Disposable {
  const core = (terminal as TerminalWithCore)._core
  const helper = core?._compositionHelper
  if (
    !core ||
    !helper ||
    typeof core._inputEvent !== 'function' ||
    typeof core._keyDownSeen !== 'boolean' ||
    !core.textarea ||
    typeof core.textarea.addEventListener !== 'function'
  ) {
    return noopDisposable
  }

  let latestKeydownWas229 = false
  let forcedSinceKeydown = false
  let textareaValueBefore229Keydown = ''
  const pendingFallbacks: PendingFallback[] = []
  const deliveredInputs: DeliveredInput[] = []

  const removePendingFallback = (pendingFallback: PendingFallback) => {
    const index = pendingFallbacks.indexOf(pendingFallback)
    if (index >= 0) pendingFallbacks.splice(index, 1)
  }

  const pruneLateEventRecords = () => {
    const now = Date.now()
    for (let index = pendingFallbacks.length - 1; index >= 0; index -= 1) {
      const pendingFallback = pendingFallbacks[index]
      if (now - pendingFallback.createdAt <= LATE_EVENT_WINDOW_MS) continue
      if (!pendingFallback.injected) clearTimeout(pendingFallback.timeoutId)
      pendingFallbacks.splice(index, 1)
    }
    for (let index = deliveredInputs.length - 1; index >= 0; index -= 1) {
      if (now - deliveredInputs[index].deliveredAt <= LATE_EVENT_WINDOW_MS) continue
      deliveredInputs.splice(index, 1)
    }
  }

  const onKeyDown = (event: Event) => {
    const keyboardEvent = event as KeyboardEvent
    pruneLateEventRecords()

    latestKeydownWas229 = keyboardEvent.keyCode === 229
    forcedSinceKeydown = false
    const textareaValueBeforeKeydown = core.textarea?.value ?? ''
    if (latestKeydownWas229) {
      textareaValueBefore229Keydown = textareaValueBeforeKeydown
    }

    if (
      keyboardEvent.keyCode !== 229 ||
      keyboardEvent.isComposing ||
      !keyboardEvent.getModifierState('CapsLock') ||
      !/^[A-Za-z]$/.test(keyboardEvent.key) ||
      keyboardEvent.ctrlKey ||
      keyboardEvent.metaKey ||
      keyboardEvent.altKey ||
      isCompositionActive(helper)
    ) {
      return
    }

    const character = keyboardEvent.shiftKey
      ? keyboardEvent.key
      : keyboardEvent.key.toLowerCase()
    const deliveredIndex = deliveredInputs.findIndex(deliveredInput =>
      isSameCharacter(deliveredInput.character, character),
    )

    // WKWebView can emit insertText before the corresponding 229 keydown. If
    // xterm already delivered that input, only cancel the late keydown.
    if (deliveredIndex >= 0) {
      deliveredInputs.splice(deliveredIndex, 1)
      keyboardEvent.preventDefault()
      return
    }

    keyboardEvent.preventDefault()
    const pendingFallback: PendingFallback = {
      character,
      textareaValueBeforeKeydown,
      createdAt: Date.now(),
      injected: false,
      timeoutId: setTimeout(() => {
        // xterm queues a textarea-diff timeout on the same keydown. Wait one
        // more turn so that diff can run before the fallback injects.
        pendingFallback.timeoutId = setTimeout(() => {
          removePendingFallback(pendingFallback)
          if (
            isCompositionActive(helper) ||
            (core.textarea && core.textarea.value !== textareaValueBeforeKeydown)
          ) {
            return
          }
          pendingFallback.injected = true
          pendingFallbacks.push(pendingFallback)
          terminal.input(character)
        }, 0)
      }, 0),
    }
    pendingFallbacks.push(pendingFallback)
  }

  core.textarea.addEventListener('keydown', onKeyDown, true)

  const originalInputEvent = core._inputEvent
  const patchedInputEvent = function patchedInputEvent(
    this: XtermCoreInternals,
    ev: InputEvent,
  ): boolean {
    pruneLateEventRecords()

    let matchingIndex = -1
    if (ev.data) {
      for (let index = pendingFallbacks.length - 1; index >= 0; index -= 1) {
        if (isSameCharacter(pendingFallbacks[index].character, ev.data)) {
          matchingIndex = index
          break
        }
      }
    }
    const matchingFallback = matchingIndex >= 0
      ? pendingFallbacks[matchingIndex]
      : null

    // A fallback already delivered this character; suppress the late input.
    if (matchingFallback?.injected) {
      if (this.textarea) {
        this.textarea.value = matchingFallback.textareaValueBeforeKeydown
      }
      removePendingFallback(matchingFallback)
      return true
    }

    if (ev.isComposing || isCompositionActive(helper)) {
      for (let index = pendingFallbacks.length - 1; index >= 0; index -= 1) {
        const pendingFallback = pendingFallbacks[index]
        if (pendingFallback.injected) continue
        clearTimeout(pendingFallback.timeoutId)
        pendingFallbacks.splice(index, 1)
      }
    }

    const was229 = latestKeydownWas229
    const shouldForceDirectPath =
      ev.inputType === 'insertText' &&
      Boolean(ev.data) &&
      ev.data!.length === 1 &&
      PRINTABLE_ASCII.test(ev.data!) &&
      !ev.isComposing &&
      !isCompositionActive(helper) &&
      this._keyDownSeen === true &&
      (was229 ? !forcedSinceKeydown : ev.composed)

    let result: boolean
    if (shouldForceDirectPath) {
      forcedSinceKeydown = true
      const savedKeyDownSeen = this._keyDownSeen
      this._keyDownSeen = false
      try {
        result = originalInputEvent!.call(this, ev)
      } finally {
        // Only the 229 path has a deferred diff to neutralise; rewinding on the
        // composed path would discard text xterm never queued.
        if (was229 && this.textarea) {
          this.textarea.value = textareaValueBefore229Keydown
        }
        this._keyDownSeen = savedKeyDownSeen
      }
    } else {
      result = originalInputEvent!.call(this, ev)
    }

    // If xterm delivered this input, remember it briefly. WKWebView may send
    // the matching 229 keydown afterward, and that keydown must not inject a
    // second copy of the character.
    if (
      result &&
      ev.inputType === 'insertText' &&
      ev.data &&
      PRINTABLE_ASCII.test(ev.data) &&
      !ev.isComposing &&
      !isCompositionActive(helper)
    ) {
      deliveredInputs.push({ character: ev.data, deliveredAt: Date.now() })
      if (matchingFallback && !matchingFallback.injected) {
        clearTimeout(matchingFallback.timeoutId)
        removePendingFallback(matchingFallback)
      }
    }

    return result
  }

  core._inputEvent = patchedInputEvent

  return {
    dispose() {
      for (const pendingFallback of pendingFallbacks) {
        if (!pendingFallback.injected) clearTimeout(pendingFallback.timeoutId)
      }
      pendingFallbacks.length = 0
      deliveredInputs.length = 0
      core.textarea?.removeEventListener('keydown', onKeyDown, true)
      if (core._inputEvent === patchedInputEvent) {
        core._inputEvent = originalInputEvent
      }
    },
  }
}
