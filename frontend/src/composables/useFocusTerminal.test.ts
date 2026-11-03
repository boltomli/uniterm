import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ panelId: 'panel-1' as string | null }))
vi.mock('../stores/tabStore', () => ({
  useTabStore: () => ({ getActivePanelId: () => state.panelId }),
}))
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: () => ({ settings: { theme: 'dark' } }),
}))

import { installTerminalFocusRestore } from './useFocusTerminal'

// Minimal DOM surface for native activation: exercise real listeners/timers
// without loading a browser or the Wails runtime.
class ElementStub {
  constructor(readonly selector = '') {}
  closest(selector: string) { return this.selector === selector ? this : null }
  getClientRects() { return [{}] }
}
class TextareaStub extends ElementStub {
  focus = vi.fn()
}

let appWindow: EventTarget
let doc: {
  activeElement: ElementStub | null
  body: ElementStub
  documentElement: ElementStub
  hidden: boolean
  hasFocus: ReturnType<typeof vi.fn>
  querySelector: ReturnType<typeof vi.fn>
  querySelectorAll: ReturnType<typeof vi.fn>
}
let terminal: TextareaStub
let uninstall: () => void

beforeEach(() => {
  vi.useFakeTimers()
  state.panelId = 'panel-1'
  appWindow = new EventTarget()
  terminal = new TextareaStub()
  doc = {
    activeElement: null, body: new ElementStub(), documentElement: new ElementStub(),
    hidden: false, hasFocus: vi.fn(() => true),
    querySelector: vi.fn(() => terminal), querySelectorAll: vi.fn(() => []),
  }
  vi.stubGlobal('window', appWindow)
  vi.stubGlobal('document', doc)
  vi.stubGlobal('HTMLElement', ElementStub)
  vi.stubGlobal('HTMLTextAreaElement', TextareaStub)
  uninstall = installTerminalFocusRestore()
})

afterEach(() => {
  uninstall()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function activate() {
  appWindow.dispatchEvent(new Event('focus'))
  vi.runAllTimers()
}

describe('terminal focus on window activation (#1320)', () => {
  it('restores the active terminal from a window-control button', () => {
    doc.activeElement = new ElementStub('.window-controls')
    activate()
    expect(doc.querySelector).toHaveBeenCalledWith('[data-panel-id="panel-1"] .xterm-helper-textarea')
    expect(terminal.focus).toHaveBeenCalledExactlyOnceWith({ preventScroll: true })
  })

  it('restores focus when the document body has focus', () => {
    doc.activeElement = doc.body
    activate()
    expect(terminal.focus).toHaveBeenCalledOnce()
  })

  it.each(['input', 'textarea', '.sftp-file-list', '.rdp-container', 'button'])(
    'preserves deliberate focus on %s', selector => {
      doc.activeElement = new ElementStub(selector)
      activate()
      expect(terminal.focus).not.toHaveBeenCalled()
    },
  )

  it('preserves focus moved between activation and the deferred restore', () => {
    appWindow.dispatchEvent(new Event('focus'))
    doc.activeElement = new ElementStub('input')
    vi.runAllTimers()
    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('does not focus behind an open dialog or menu', () => {
    doc.querySelectorAll.mockReturnValue([new ElementStub('[role="dialog"]')])
    activate()
    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('ignores hidden dialogs', () => {
    doc.querySelectorAll.mockReturnValue([{ getClientRects: () => [] }])
    activate()
    expect(terminal.focus).toHaveBeenCalledOnce()
  })

  it('cancels restoration when the window loses focus again', () => {
    appWindow.dispatchEvent(new Event('focus'))
    appWindow.dispatchEvent(new Event('blur'))
    vi.runAllTimers()
    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('does not focus a hidden window', () => {
    doc.hidden = true
    activate()
    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('does not focus a non-terminal tab', () => {
    state.panelId = null
    activate()
    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('leaves remote desktop panels without a terminal alone', () => {
    doc.querySelector.mockReturnValue(null)
    activate()
    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('cancels pending restoration and removes listeners on teardown', () => {
    appWindow.dispatchEvent(new Event('focus'))
    uninstall()
    activate()
    expect(terminal.focus).not.toHaveBeenCalled()
  })
})
