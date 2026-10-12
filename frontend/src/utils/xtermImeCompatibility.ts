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
  // Text of the most recent composition commit, recorded by the Windows patch
  // and cleared on the next compositionstart (and by resetXtermInputState).
  _lastCommittedText?: string
  _resetWindowsIme?: () => void
  _coreService?: {
    triggerDataEvent?: (this: unknown, data: string, wasUserInput?: boolean) => void
  }
  compositionstart?: (this: XtermCompositionHelperInternals) => void
  compositionupdate?: (
    this: XtermCompositionHelperInternals,
    ev: Pick<CompositionEvent, 'data'>,
  ) => void
  _finalizeComposition?: (
    this: XtermCompositionHelperInternals,
    waitForPropagation: boolean,
  ) => void
  _handleAnyTextareaChanges?: () => void
}

interface XtermCoreInternals {
  _inputEvent?: (this: XtermCoreInternals, ev: InputEvent) => boolean
  _keyDownSeen?: boolean
  _keyDownHandled?: boolean
  _keyPressHandled?: boolean
  _unprocessedDeadKey?: boolean
  _compositionHelper?: XtermCompositionHelperInternals
  _syncTextArea?: () => void
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
// everything accumulated in the textarea.
//
// Own the deferred reads on Windows so reset can cancel them, and remember
// the text actually sent — not the preedit value present at compositionend.
// Flush a pending commit before a new composition starts, preserving both
// commit order and genuine repeated words. Direct input and textarea-diff
// fallback must never both own the same edit.
function installWindowsImePatch(terminal: Terminal): Disposable {
  const core = (terminal as TerminalWithCore)._core
  const helper = core?._compositionHelper
  if (!core || !helper || typeof core._inputEvent !== 'function' || !core.textarea) {
    return noopDisposable
  }

  const textarea = core.textarea
  const disposers: (() => void)[] = []
  let compositionTimer: number | undefined
  let textareaTimer: number | undefined
  let pendingStart = 0
  helper._lastCommittedText = ''

  const cancelTextareaRead = () => {
    window.clearTimeout(textareaTimer)
    textareaTimer = undefined
  }
  const cancelCompositionRead = () => {
    window.clearTimeout(compositionTimer)
    compositionTimer = undefined
    helper._isSendingComposition = false
  }
  const sendCommit = (data: string) => {
    if (!data) return
    helper._lastCommittedText = data
    helper._coreService?.triggerDataEvent?.(data, true)
  }
  const flushComposition = () => {
    if (compositionTimer === undefined) return
    const start = pendingStart + (helper._dataAlreadySent?.length ?? 0)
    cancelCompositionRead()
    sendCommit(textarea.value.substring(start))
  }
  const reset = () => {
    cancelCompositionRead()
    cancelTextareaRead()
  }
  helper._resetWindowsIme = reset
  disposers.push(() => {
    reset()
    if (helper._resetWindowsIme === reset) delete helper._resetWindowsIme
  })

  if (typeof helper.compositionstart === 'function') {
    const originalCompositionStart = helper.compositionstart
    const patchedCompositionStart = function patchedCompositionStart(
      this: XtermCompositionHelperInternals,
    ): void {
      flushComposition()
      cancelTextareaRead()
      this._lastCommittedText = ''
      originalCompositionStart.call(this)
    }
    helper.compositionstart = patchedCompositionStart
    disposers.push(() => {
      if (helper.compositionstart === patchedCompositionStart) {
        helper.compositionstart = originalCompositionStart
      }
    })
  }

  if (typeof helper.compositionupdate === 'function') {
    const originalCompositionUpdate = helper.compositionupdate
    const patchedCompositionUpdate = function patchedCompositionUpdate(
      this: XtermCompositionHelperInternals,
      ev: Pick<CompositionEvent, 'data'>,
    ): void {
      const compositionData = typeof ev?.data === 'string' ? ev.data : ''
      if (this._isComposing !== true && this._isSendingComposition !== true) {
        // compositionstart was dropped: rebase the region to this update, the
        // way compositionstart would have, so the compositionend read delivers
        // only the new text instead of the whole accumulated textarea value.
        // This is still the start of a new composition, so forget the prior
        // commit the same way compositionstart does.
        cancelTextareaRead()
        this._lastCommittedText = ''
        this._isComposing = true
        this._isSendingComposition = false
        this._dataAlreadySent = ''
        const start = Math.max(0, textarea.value.length - compositionData.length)
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
      const end = this._compositionPosition?.end ?? start
      const wasComposing = this._isComposing === true
      this._isComposing = false
      core._helperContainer?.querySelector?.('.composition-view')?.classList.remove('active')
      cancelTextareaRead()

      if (!waitForPropagation) {
        cancelCompositionRead()
        const input = textarea.value.substring(start, end)
        if (input !== this._lastCommittedText) sendCommit(input)
        this._dataAlreadySent = textarea.value.substring(start)
        return
      }

      // An end without an active composition is a duplicate or a straggler
      // from before reset. It must not read newly typed text as an old commit.
      if (!wasComposing || compositionTimer !== undefined) return
      pendingStart = start
      this._isSendingComposition = true
      compositionTimer = window.setTimeout(flushComposition, 0)
    }
    helper._finalizeComposition = patchedFinalize
    disposers.push(() => {
      if (helper._finalizeComposition === patchedFinalize) {
        helper._finalizeComposition = originalFinalize
      }
    })
  }

  if (typeof helper._handleAnyTextareaChanges === 'function') {
    const originalTextareaRead = helper._handleAnyTextareaChanges
    const patchedTextareaRead = function patchedTextareaRead(): void {
      if (textareaTimer !== undefined) return
      const oldValue = textarea.value
      textareaTimer = window.setTimeout(() => {
        textareaTimer = undefined
        if (isCompositionActive(helper)) return
        const value = textarea.value
        const diff = value.replace(oldValue, '')
        helper._dataAlreadySent = diff
        if (value.length < oldValue.length) {
          helper._coreService?.triggerDataEvent?.('\x7f', true)
        } else {
          const input = value.length > oldValue.length ? diff : value !== oldValue ? value : ''
          if (input && input !== helper._lastCommittedText) {
            helper._coreService?.triggerDataEvent?.(input, true)
          }
        }
      }, 0)
    }
    helper._handleAnyTextareaChanges = patchedTextareaRead
    disposers.push(() => {
      if (helper._handleAnyTextareaChanges === patchedTextareaRead) {
        helper._handleAnyTextareaChanges = originalTextareaRead
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
      const helper = this._compositionHelper
      const inFlight =
        helper?._isSendingComposition === true || helper?._isComposing === true
      if (inFlight) {
        // The composition path owns this text: the pending compositionend
        // read delivers the whole region, so the direct send would commit
        // it a second time.
        const start = helper?._compositionPosition?.start ?? 0
        if ((this.textarea ?? textarea).value.substring(start).includes(ev.data)) {
          return true
        }
      }
      // The composition read (or the keydown finalize) already delivered
      // exactly this text and the commit's input event arrived after it — drop
      // it. Matching the committed text structurally (not by a time window)
      // keeps a late straggler loss-free, and a genuinely new commit always
      // starts with a fresh composition that clears the recorded text.
      if (ev.data === helper?._lastCommittedText) {
        return true
      }
    }
    const handled = originalInputEvent!.call(this, ev)
    if (handled) cancelTextareaRead()
    return handled
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
  // Blur can synchronously produce compositionend; cancel its read only
  // after the OS-level composition has ended.
  if (opts.blur !== false) textarea?.blur?.()
  core?._compositionHelper?._resetWindowsIme?.()
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
    helper._lastCommittedText = ''
    helper._compositionPosition = { start: 0, end: 0 }
    const compositionView = core?._helperContainer?.querySelector?.('.composition-view')
    compositionView?.classList.remove('active')
  }
  if (textarea) textarea.value = ''
  core?._syncTextArea?.()
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
