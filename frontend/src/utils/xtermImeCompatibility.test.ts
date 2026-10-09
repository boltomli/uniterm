import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { installImeCompatibilityPatch, resetXtermInputState } from './xtermImeCompatibility'

interface FakeCore {
  [key: string]: unknown
  _inputEvent: (this: Record<string, unknown>, ev: InputEvent) => boolean
  _keyDownSeen: boolean
  _keyDownHandled: boolean
  _keyPressHandled: boolean
  _unprocessedDeadKey: boolean
  _compositionHelper: Record<string, any>
  textarea: {
    value: string
    listeners: Map<string, EventListener>
    addEventListener: (type: string, fn: EventListener) => void
    removeEventListener: (type: string, fn: EventListener) => void
    blur: () => void
    focus: () => void
  }
}

function makeFakeCore(inputEventResult: boolean = true) {
  const calls: { keyDownSeenAtCall: boolean | undefined }[] = []
  // What xterm's composition pipeline sent to the PTY (the fake helper below
  // mirrors CompositionHelper's send paths).
  const sends: string[] = []
  const core: FakeCore = {
    _inputEvent: function (this: Record<string, unknown>, ev: InputEvent) {
      calls.push({ keyDownSeenAtCall: this._keyDownSeen as boolean | undefined })
      // Mirror the browser: by the time the input event is dispatched the
      // textarea already holds the inserted data.
      if (ev.data) core.textarea.value += ev.data
      return inputEventResult
    },
    _keyDownSeen: true,
    _keyDownHandled: false,
    _keyPressHandled: false,
    _unprocessedDeadKey: false,
    _compositionHelper: {
      _isComposing: false,
      _isSendingComposition: false,
      _compositionPosition: { start: 0, end: 0 },
      _dataAlreadySent: '',
      // Mirror of xterm's CompositionHelper._finalizeComposition: the
      // keydown-driven variant sends substring(start, end) synchronously, the
      // compositionend variant schedules a deferred read of the region minus
      // what _dataAlreadySent says was already sent (xterm.js issue #3191).
      _finalizeComposition(this: Record<string, any>, waitForPropagation: boolean) {
        const start = (this._compositionPosition?.start as number) ?? 0
        const end = (this._compositionPosition?.end as number) ?? start
        this._isComposing = false
        if (!waitForPropagation) {
          this._isSendingComposition = false
          const input = core.textarea.value.substring(start, end)
          if (input.length > 0) sends.push(input)
          return
        }
        const snapshotStart = start
        this._isSendingComposition = true
        setTimeout(() => {
          if (!this._isSendingComposition) return
          this._isSendingComposition = false
          const from = snapshotStart + (this._dataAlreadySent?.length ?? 0)
          const input = core.textarea.value.substring(from)
          if (input.length > 0) sends.push(input)
        }, 0)
      },
      compositionupdate(this: Record<string, any>) {
        setTimeout(() => {
          this._compositionPosition.end = core.textarea.value.length
        }, 0)
      },
    },
    textarea: {
      value: '',
      listeners: new Map(),
      addEventListener(type: string, fn: EventListener) {
        core.textarea.listeners.set(type, fn)
      },
      removeEventListener(type: string, fn: EventListener) {
        if (core.textarea.listeners.get(type) === fn) core.textarea.listeners.delete(type)
      },
      blur: vi.fn(),
      focus: vi.fn(),
    },
  }
  return { core, calls, sends }
}

function fakeTerminal(core: FakeCore, input = vi.fn()) {
  return { _core: core, input } as unknown as import('@xterm/xterm').Terminal
}

function keydown(
  keyCode: number,
  opts: {
    key?: string
    isComposing?: boolean
    shiftKey?: boolean
    capsLock?: boolean
  } = {},
) {
  return {
    keyCode,
    key: opts.key ?? 'a',
    isComposing: opts.isComposing ?? false,
    shiftKey: opts.shiftKey ?? false,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    getModifierState: (modifier: string) => modifier === 'CapsLock' && (opts.capsLock ?? false),
    preventDefault: vi.fn(),
  } as unknown as KeyboardEvent
}

function insertText(
  data: string,
  opts: { isComposing?: boolean; composed?: boolean } = {},
) {
  return {
    inputType: 'insertText',
    data,
    isComposing: opts.isComposing ?? false,
    // Real typed input events are composed; the fake must match or the patch
    // never sees the condition that makes xterm skip its direct path.
    composed: opts.composed ?? true,
  } as unknown as InputEvent
}

beforeEach(() => {
  vi.stubGlobal('navigator', { userAgent: 'Macintosh; Intel Mac OS X 10_15_7' })
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('installImeCompatibilityPatch', () => {
  it('is a no-op on platforms without a dedicated patch', () => {
    vi.stubGlobal('navigator', { userAgent: 'X11; Linux x86_64' })
    const { core, calls } = makeFakeCore()
    const disposable = installImeCompatibilityPatch(fakeTerminal(core))
    expect(core._inputEvent).toBe(core._inputEvent)
    expect(core.textarea.listeners.size).toBe(0)
    disposable.dispose()
    expect(calls).toHaveLength(0)
  })

  it('does not patch when xterm internals are missing', () => {
    const disposable = installImeCompatibilityPatch({ _core: {} } as never)
    expect(disposable.dispose).toBeTypeOf('function')
    disposable.dispose()
  })

  it('forces direct delivery for a single printable char after a 229 keydown', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('a'))

    // Original handler must see _keyDownSeen cleared (direct path)...
    expect(calls).toEqual([{ keyDownSeenAtCall: false }])
    // ...and the flag restored afterwards so xterm bookkeeping stays intact.
    expect(core._keyDownSeen).toBe(true)
  })

  it('restores the textarea snapshot after direct delivery', () => {
    const { core } = makeFakeCore()
    core.textarea.value = 'aba'
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('a'))

    // The queued xterm textarea diff must observe the same value it captured
    // before keydown, even when the textarea already contains repeated text.
    expect(core.textarea.value).toBe('aba')
  })

  it('delivers consecutive phantom 229 input once and preserves its case', () => {
    const { core } = makeFakeCore()
    const delivered: string[] = []
    const originalInputEvent = core._inputEvent
    core._inputEvent = function (this: Record<string, unknown>, ev: InputEvent) {
      if (ev.data !== null) delivered.push(ev.data)
      return originalInputEvent.call(this, ev)
    }
    installImeCompatibilityPatch(fakeTerminal(core))

    for (const character of 'asdf') {
      core.textarea.listeners.get('keydown')!(keydown(229))
      core._inputEvent.call(core, insertText(character))
    }

    expect(delivered).toEqual(['a', 's', 'd', 'f'])
    expect(core.textarea.value).toBe('')
  })

  it('injects every English letter after Caps Lock when no input event follows', () => {
    vi.useFakeTimers()
    const { core } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    for (const key of 'ASDF') {
      core.textarea.listeners.get('keydown')!(
        keydown(229, { key, capsLock: true }),
      )
    }
    expect(input).not.toHaveBeenCalled()

    vi.runAllTimers()
    expect(input).toHaveBeenNthCalledWith(1, 'a')
    expect(input).toHaveBeenNthCalledWith(2, 's')
    expect(input).toHaveBeenNthCalledWith(3, 'd')
    expect(input).toHaveBeenNthCalledWith(4, 'f')
  })

  it('keeps Shift uppercase in the Caps Lock fallback', () => {
    vi.useFakeTimers()
    const { core } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    core.textarea.listeners.get('keydown')!(
      keydown(229, { key: 'A', shiftKey: true, capsLock: true }),
    )
    vi.runAllTimers()

    expect(input).toHaveBeenCalledWith('A')
  })

  it('cancels the Caps Lock fallback when the input event arrives', () => {
    vi.useFakeTimers()
    const { core, calls } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'A', capsLock: true }))
    core._inputEvent.call(core, insertText('a'))
    vi.runAllTimers()

    expect(input).not.toHaveBeenCalled()
    expect(calls).toEqual([{ keyDownSeenAtCall: false }])
  })

  it('suppresses a late input event after the Caps Lock fallback injected', () => {
    vi.useFakeTimers()
    const { core, calls } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'A', capsLock: true }))
    vi.runAllTimers()
    core._inputEvent.call(core, insertText('a'))

    expect(input).toHaveBeenCalledTimes(1)
    expect(input).toHaveBeenCalledWith('a')
    expect(calls).toEqual([])
    expect(core.textarea.value).toBe('')
  })

  it('does not inject fallback when input was delivered before its keydown', () => {
    vi.useFakeTimers()
    const { core } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    expect(core._inputEvent.call(core, insertText('s'))).toBe(true)
    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'S', capsLock: true }))
    vi.runAllTimers()

    expect(input).not.toHaveBeenCalled()
  })

  it('injects fallback when an earlier input event was not delivered', () => {
    vi.useFakeTimers()
    const { core } = makeFakeCore(false)
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    expect(core._inputEvent.call(core, insertText('a'))).toBe(false)
    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'A', capsLock: true }))
    vi.runAllTimers()

    expect(input).toHaveBeenCalledTimes(1)
    expect(input).toHaveBeenCalledWith('a')
  })

  it('suppresses late composing input after the Caps Lock fallback injected', () => {
    vi.useFakeTimers()
    const { core, calls } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    for (const [key, character] of [['A', 'a'], ['S', 's'], ['D', 'd'], ['F', 'f']] as const) {
      core.textarea.listeners.get('keydown')!(keydown(229, { key, capsLock: true }))
      vi.runAllTimers()
      core._inputEvent.call(core, insertText(character, { isComposing: true }))
    }

    expect(input.mock.calls).toEqual([['a'], ['s'], ['d'], ['f']])
    expect(calls).toEqual([])
    expect(core.textarea.value).toBe('')
  })

  it('uses the Caps Lock fallback for consecutive missing input events', () => {
    vi.useFakeTimers()
    const { core } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'A', capsLock: true }))
    vi.runAllTimers()
    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'S', capsLock: true }))
    vi.runAllTimers()

    expect(input).toHaveBeenNthCalledWith(1, 'a')
    expect(input).toHaveBeenNthCalledWith(2, 's')
  })

  it('does not use the Caps Lock fallback for ordinary keydowns', () => {
    vi.useFakeTimers()
    const { core } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    core.textarea.listeners.get('keydown')!(keydown(65, { key: 'A', capsLock: true }))
    vi.runAllTimers()

    expect(input).not.toHaveBeenCalled()
  })

  it('mixes fallback injection with direct input delivery without duplicates', () => {
    vi.useFakeTimers()
    const { core, calls } = makeFakeCore()
    const input = vi.fn()
    installImeCompatibilityPatch(fakeTerminal(core, input))

    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'A', capsLock: true }))
    vi.runAllTimers()
    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'S', capsLock: true }))
    vi.runAllTimers()
    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'D', capsLock: true }))
    core._inputEvent.call(core, insertText('d'))
    core.textarea.listeners.get('keydown')!(keydown(229, { key: 'A', capsLock: true }))
    core._inputEvent.call(core, insertText('a'))
    vi.runAllTimers()

    expect(input).toHaveBeenNthCalledWith(1, 'a')
    expect(input).toHaveBeenNthCalledWith(2, 's')
    expect(calls).toEqual([
      { keyDownSeenAtCall: false },
      { keyDownSeenAtCall: false },
    ])
  })

  it('forces composed input after a non-229 keydown', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(65))
    core._inputEvent.call(core, insertText('a'))

    expect(calls).toEqual([{ keyDownSeenAtCall: false }])
  })

  it('only forces once per keydown', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('a'))
    core._inputEvent.call(core, insertText('b'))

    expect(calls).toEqual([
      { keyDownSeenAtCall: false },
      { keyDownSeenAtCall: true },
    ])
  })

  // issue #913: Doubao swallows keypress for Shift+letter without reporting
  // keyCode 229, so the input event was xterm's only remaining delivery path.
  it('delivers uppercase Shift input that reports no 229 keydown', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.value = 'ls '
    core.textarea.listeners.get('keydown')!(keydown(65, { key: 'A', shiftKey: true }))
    core._inputEvent.call(core, insertText('A'))

    expect(calls).toEqual([{ keyDownSeenAtCall: false }])
    // No 229 diff was queued, so the textarea must not be rewound.
    expect(core.textarea.value).toBe('ls A')
    expect(core._keyDownSeen).toBe(true)
  })

  it('forces consecutive composed inputs after non-229 keydowns', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    for (const key of 'ABC') {
      core.textarea.listeners.get('keydown')!(keydown(65, { key, shiftKey: true }))
      core._inputEvent.call(core, insertText(key))
    }

    expect(calls).toEqual([
      { keyDownSeenAtCall: false },
      { keyDownSeenAtCall: false },
      { keyDownSeenAtCall: false },
    ])
    expect(core.textarea.value).toBe('ABC')
  })

  it('does not force composed input while composition is active', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core._compositionHelper._isComposing = true
    core.textarea.listeners.get('keydown')!(keydown(65))
    core._inputEvent.call(core, insertText('a'))

    expect(calls).toEqual([{ keyDownSeenAtCall: true }])
  })

  it('does not force multi-character composed input', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(65))
    core._inputEvent.call(core, insertText('ab'))

    expect(calls).toEqual([{ keyDownSeenAtCall: true }])
  })

  it('leaves non-composed input on xterms own path', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(65))
    core._inputEvent.call(core, insertText('a', { composed: false }))

    expect(calls).toEqual([{ keyDownSeenAtCall: true }])
  })

  // Forcing the gate open cannot double-deliver: xterm's own handler still
  // bails on _keyPressHandled, so keypress wins whenever it did fire.
  it('does not double-deliver when keypress already handled the character', () => {
    const { core } = makeFakeCore()
    const delivered: string[] = []
    // Mirror xterm's _inputEvent: the _keyPressHandled guard runs inside the
    // original handler, after our patch has flipped _keyDownSeen.
    core._inputEvent = function (this: Record<string, unknown>, ev: InputEvent) {
      if (this._keyPressHandled) return false
      if (ev.data) delivered.push(ev.data)
      return true
    }
    core._keyPressHandled = true
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(65, { key: 'A', shiftKey: true }))
    expect(core._inputEvent.call(core, insertText('A'))).toBe(false)

    expect(delivered).toEqual([])
  })

  it('leaves real composition untouched', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('a', { isComposing: true }))
    expect(calls).toEqual([{ keyDownSeenAtCall: true }])

    core._compositionHelper._isComposing = true
    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('a'))
    expect(calls).toEqual([
      { keyDownSeenAtCall: true },
      { keyDownSeenAtCall: true },
    ])
  })

  it('leaves multi-character and non-insertText input untouched', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('ab'))
    expect(calls).toEqual([{ keyDownSeenAtCall: true }])

    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, { inputType: 'deleteContentBackward' } as unknown as InputEvent)
    expect(calls).toEqual([
      { keyDownSeenAtCall: true },
      { keyDownSeenAtCall: true },
    ])
  })

  it('is always active on macOS', () => {
    const { core, calls } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))

    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('a'))
    core.textarea.listeners.get('keydown')!(keydown(229))
    core._inputEvent.call(core, insertText('b'))

    expect(calls).toEqual([
      { keyDownSeenAtCall: false },
      { keyDownSeenAtCall: false },
    ])
  })

  it('dispose restores the original handler and removes the keydown listener', () => {
    const { core } = makeFakeCore()
    const original = core._inputEvent
    const disposable = installImeCompatibilityPatch(fakeTerminal(core))

    expect(core._inputEvent).not.toBe(original)
    expect(core.textarea.listeners.has('keydown')).toBe(true)

    disposable.dispose()
    expect(core._inputEvent).toBe(original)
    expect(core.textarea.listeners.size).toBe(0)
  })
})

describe('windows duplicate-commit guard', () => {
  beforeEach(() => {
    vi.stubGlobal('navigator', { userAgent: 'Windows NT 10.0; Win64; x64' })
  })

  it('suppresses the direct input path while a composition commit is pending', () => {
    const { core, calls } = makeFakeCore()
    core._compositionHelper._isSendingComposition = true
    core._compositionHelper._compositionPosition = { start: 0 }
    // The browser inserts the committed text before dispatching input.
    core.textarea.value = '词'
    const disposable = installImeCompatibilityPatch(fakeTerminal(core))

    // keyup already reset _keyDownSeen — vanilla xterm would send directly
    // and the pending compositionend read would send the word a second time.
    core._keyDownSeen = false
    expect(core._inputEvent.call(core, insertText('词'))).toBe(true)
    expect(calls).toHaveLength(0)

    disposable.dispose()
  })

  it('suppresses the direct input path mid-composition', () => {
    const { core, calls } = makeFakeCore()
    core._compositionHelper._isComposing = true
    core._compositionHelper._compositionPosition = { start: 0 }
    core.textarea.value = '词'
    installImeCompatibilityPatch(fakeTerminal(core))

    core._keyDownSeen = false
    expect(core._inputEvent.call(core, insertText('词'))).toBe(true)
    expect(calls).toHaveLength(0)
  })

  it('passes input through when the composition region does not cover it', () => {
    const { core, calls } = makeFakeCore()
    core._compositionHelper._isSendingComposition = true
    core._compositionHelper._compositionPosition = { start: 0 }
    // Blur cleared the textarea: the pending read will deliver nothing, so
    // the input event must not be swallowed or the keystroke would be lost.
    core.textarea.value = ''
    installImeCompatibilityPatch(fakeTerminal(core))

    core._inputEvent.call(core, insertText('词'))
    expect(calls).toHaveLength(1)
  })

  it('passes input through when no composition is in flight', () => {
    const { core, calls } = makeFakeCore()
    core.textarea.value = ''
    installImeCompatibilityPatch(fakeTerminal(core))

    core._inputEvent.call(core, insertText('a'))
    expect(calls).toHaveLength(1)
  })

  it('dispose restores the original handler', () => {
    const { core } = makeFakeCore()
    const original = core._inputEvent
    const disposable = installImeCompatibilityPatch(fakeTerminal(core))
    expect(core._inputEvent).not.toBe(original)

    disposable.dispose()
    expect(core._inputEvent).toBe(original)
  })
})

describe('windows composition pipeline', () => {
  beforeEach(() => {
    vi.stubGlobal('navigator', { userAgent: 'Windows NT 10.0; Win64; x64' })
    vi.useFakeTimers()
  })

  it('sends a keydown-finalized commit once even when compositionend follows it', () => {
    const { core, sends } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))
    const helper = core._compositionHelper
    // The committed word is in the textarea (xterm never clears it mid-flow).
    core.textarea.value = '你好'
    helper._isComposing = true
    helper._compositionPosition = { start: 0, end: 2 }

    // Enter/Space confirming the candidate finalizes through the keydown path…
    helper._finalizeComposition(false)
    expect(sends).toEqual(['你好'])

    // …and the reordered compositionend schedules xterm's deferred read,
    // which must not deliver the same text a second time.
    helper._finalizeComposition(true)
    vi.runAllTimers()
    expect(sends).toEqual(['你好'])
  })

  it('drops a late insertText whose text the composition read just delivered', () => {
    const { core, calls, sends } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))
    const helper = core._compositionHelper
    core.textarea.value = '词'
    helper._isComposing = true
    helper._compositionPosition = { start: 0 }

    // compositionend → the deferred read delivers the word…
    helper._finalizeComposition(true)
    vi.runAllTimers()
    expect(sends).toEqual(['词'])

    // …then the commit's input event arrives (Chromium can order
    // compositionend first). The flags are clear, but the word is already on
    // the PTY — the region still holds exactly what was delivered.
    core._keyDownSeen = false
    expect(core._inputEvent.call(core, insertText('词'))).toBe(true)
    expect(calls).toHaveLength(0)
    expect(sends).toEqual(['词'])
  })

  it('delivers a second commit of the same word outside the dedup window', () => {
    const { core, calls, sends } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))
    const helper = core._compositionHelper
    core.textarea.value = '词'
    helper._isComposing = true
    helper._compositionPosition = { start: 0 }
    helper._finalizeComposition(true)
    vi.runAllTimers()
    expect(sends).toEqual(['词'])

    vi.advanceTimersByTime(300)
    core._keyDownSeen = false
    expect(core._inputEvent.call(core, insertText('词'))).toBe(true)
    // The fake handler reports its own result; assert it actually ran and
    // appended the second word instead of being swallowed.
    expect(calls).toHaveLength(1)
    expect(core.textarea.value).toBe('词词')
  })

  it('rebases the composition region when compositionstart was dropped', () => {
    const { core, sends } = makeFakeCore()
    installImeCompatibilityPatch(fakeTerminal(core))
    const helper = core._compositionHelper
    // '旧词' was committed earlier and the drag's modal loop dropped the next
    // compositionstart: xterm's region still points at offset 0 while the
    // textarea keeps accumulating.
    core.textarea.value = '旧词新'
    helper._isComposing = false
    helper._isSendingComposition = false
    helper._compositionPosition = { start: 0 }

    helper.compositionupdate({ data: '新' })
    expect(helper._isComposing).toBe(true)
    expect(helper._compositionPosition.start).toBe(2)

    // The preedit commit lands in the textarea, then compositionend runs the
    // deferred read — only the new word may go out.
    core.textarea.value = '旧词新词'
    helper._finalizeComposition(true)
    vi.runAllTimers()
    expect(sends).toEqual(['新词'])
  })

  it('dispose restores the composition handlers', () => {
    const { core } = makeFakeCore()
    const helper = core._compositionHelper
    const originalUpdate = helper.compositionupdate
    const originalFinalize = helper._finalizeComposition
    const disposable = installImeCompatibilityPatch(fakeTerminal(core))
    expect(helper.compositionupdate).not.toBe(originalUpdate)
    expect(helper._finalizeComposition).not.toBe(originalFinalize)

    disposable.dispose()
    expect(helper.compositionupdate).toBe(originalUpdate)
    expect(helper._finalizeComposition).toBe(originalFinalize)
  })
})

describe('resetXtermInputState', () => {
  it('clears key and composition state and blurs the textarea', () => {
    const { core } = makeFakeCore()
    core._keyDownHandled = true
    core._keyPressHandled = true
    core._keyDownSeen = true
    core._unprocessedDeadKey = true
    const helper = core._compositionHelper
    helper._isComposing = true
    helper._isSendingComposition = true
    helper._dataAlreadySent = '词'
    helper._compositionPosition = { start: 1, end: 2 }
    core.textarea.value = '词'

    resetXtermInputState(fakeTerminal(core))

    expect([
      core._keyDownHandled,
      core._keyPressHandled,
      core._keyDownSeen,
      core._unprocessedDeadKey,
    ]).toEqual([false, false, false, false])
    expect(helper._isComposing).toBe(false)
    expect(helper._isSendingComposition).toBe(false)
    expect(helper._dataAlreadySent).toBe('')
    expect(helper._compositionPosition).toEqual({ start: 0, end: 0 })
    expect(core.textarea.value).toBe('')
    expect(core.textarea.blur).toHaveBeenCalled()
  })

  it('keeps focus when called with blur: false (drag start)', () => {
    const { core } = makeFakeCore()
    core.textarea.value = '词'

    resetXtermInputState(fakeTerminal(core), { blur: false })

    expect(core.textarea.value).toBe('')
    expect(core.textarea.blur).not.toHaveBeenCalled()
  })

  it('is a no-op without a terminal', () => {
    expect(() => resetXtermInputState(null)).not.toThrow()
  })
})
