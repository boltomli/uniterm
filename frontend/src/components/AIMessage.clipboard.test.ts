import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, ssrContextKey } from 'vue'
import AIMessage from './AIMessage.vue'

const { setText, writeText, showError } = vi.hoisted(() => ({
  setText: vi.fn(), writeText: vi.fn(), showError: vi.fn(),
}))
vi.mock('@wailsio/runtime', () => ({ Clipboard: { SetText: setText } }))
vi.mock('element-plus', () => ({ ElMessage: { error: showError } }))
vi.mock('../stores/aiStore', () => ({ useAIStore: () => ({ isRunning: false }) }))
vi.mock('../i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

// Run the real SFC setup and clipboard helper without a browser DOM.
// Rendering is disabled; only the handlers and their feedback are needed.
const renderer = createRenderer<object, object>({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  insert: () => {}, remove: () => {}, setText: () => {},
  setElementText: () => {}, patchProp: () => {},
  parentNode: () => null, nextSibling: () => null,
})
type Handlers = {
  copyAsMarkdown(): Promise<void>
  copyToolText(text: string, key: string): Promise<void>
  onTextClick(event: MouseEvent): Promise<void>
  copyMdLabel: string
  copiedTool: string
}
let handlers: Handlers
let unmount: () => void
const markdown = '# 分析结果\n\n```sh\nprintf "你好"\n```'

beforeEach(() => {
  vi.useFakeTimers()
  setText.mockReset().mockResolvedValue(undefined)
  writeText.mockReset().mockResolvedValue(undefined)
  showError.mockReset()
  vi.stubGlobal('navigator', { userAgent: 'Macintosh', clipboard: { writeText } })
  const app = renderer.createApp({ ...AIMessage, render: () => null }, {
    message: { id: 'answer', role: 'assistant', content: markdown },
  })
  const root = {}
  app.provide(ssrContextKey, {})
  const vm = app.mount(root)
  handlers = (vm.$ as unknown as { setupState: Handlers }).setupState
  unmount = () => app.unmount()
})
afterEach(() => {
  unmount()
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function codeClick(code: string) {
  const button = {
    innerHTML: 'copy',
    closest: () => ({ querySelector: () => ({ textContent: code }) }),
  }
  const event = { target: { closest: () => button } } as unknown as MouseEvent
  return { button, event }
}

for (const platform of ['Macintosh', 'Windows NT 10.0']) {
  describe(platform, () => {
    beforeEach(() => { Object.defineProperty(navigator, 'userAgent', { value: platform }) })

    it('copies Markdown, tool input/output and code blocks through the platform clipboard path', async () => {
      await handlers.copyAsMarkdown()
      expect(handlers.copyMdLabel).toBe('ai.copied')
      await handlers.copyToolText('printf "你好"', 'tool-in')
      expect(handlers.copiedTool).toBe('tool-in')
      await handlers.copyToolText('输出\r\nsecond line', 'tool-out')
      expect(handlers.copiedTool).toBe('tool-out')
      const { button, event } = codeClick('echo "你好"\n')
      await handlers.onTextClick(event)
      expect(button.innerHTML).toContain('polyline')
      const primary = platform === 'Macintosh' ? setText : writeText
      const fallback = platform === 'Macintosh' ? writeText : setText
      expect(primary.mock.calls.map(call => call[0])).toEqual([
        markdown, 'printf "你好"', '输出\r\nsecond line', 'echo "你好"\n',
      ])
      expect(fallback).not.toHaveBeenCalled()
    })

    it('falls back when the preferred clipboard API rejects', async () => {
      const primary = platform === 'Macintosh' ? setText : writeText
      primary.mockRejectedValue(new Error('preferred API unavailable'))
      await handlers.copyAsMarkdown()
      expect(handlers.copyMdLabel).toBe('ai.copied')
      expect(setText).toHaveBeenCalledWith(markdown)
      expect(writeText).toHaveBeenCalledWith(markdown)
    })

    it('reports failure without claiming a successful tool or code copy', async () => {
      setText.mockResolvedValue(false)
      writeText.mockRejectedValue(new Error('clipboard unavailable'))
      await handlers.copyAsMarkdown()
      expect(handlers.copyMdLabel).toBe('ai.copyFailed')
      await handlers.copyToolText('command', 'tool-in')
      expect(handlers.copiedTool).toBe('')
      const { button, event } = codeClick('command')
      await handlers.onTextClick(event)
      expect(button.innerHTML).toBe('copy')
      expect(showError).toHaveBeenCalledTimes(2)
      expect(showError).toHaveBeenCalledWith('ai.copyFailed')
    })
  })
}
