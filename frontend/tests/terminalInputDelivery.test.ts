import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTerminalInput } from '../src/composables/useTerminalInput'
import { pasteWithScroll } from '../src/utils/terminalPaste'
import { filterTerminalInput } from '../src/utils/terminalInputFilter'
import { createKeystrokeReorder } from '../src/utils/imeKeystrokeOrder'

// Exercise the component's actual onData callback without mounting Wails,
// WebGL, and every store used by BaseTerminal.
const source = readFileSync(new URL('../src/components/BaseTerminal.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup[^>]*>([\s\S]*?)<\/script>/)![1]
const ast = ts.createSourceFile('BaseTerminal.ts', script, ts.ScriptTarget.Latest, true)
// onData hands each keystroke to the input-method reorder buffer, which
// releases it into processTerminalData — the rest of the pipeline. Both halves
// are needed, and they are wired back together through the real buffer below so
// this still exercises the path a keystroke actually takes.
let callback = ''
let pipeline = ''
function visit(node: ts.Node) {
  if (ts.isBinaryExpression(node) && node.left.getText(ast) === 'onDataDispose' &&
      ts.isCallExpression(node.right) && node.right.expression.getText(ast) === 'terminal.onData') {
    callback = node.right.arguments[0].getText(ast)
  }
  if (ts.isBinaryExpression(node) && node.left.getText(ast) === 'processTerminalData' &&
      ts.isArrowFunction(node.right)) {
    pipeline = node.right.getText(ast)
  }
  ts.forEachChild(node, visit)
}
visit(ast)
if (!callback) throw new Error('BaseTerminal onData callback not found')
if (!pipeline) throw new Error('BaseTerminal processTerminalData not found')
const executable = ts.transpile(
  `const processTerminalData = ${pipeline}; const onData = ${callback}; ({ onData, processTerminalData })`,
  { target: ts.ScriptTarget.ES2022 },
)

function setup() {
  vi.stubGlobal('requestAnimationFrame', () => 0)
  vi.stubGlobal('cancelAnimationFrame', () => {})
  const input = useTerminalInput(null, { mode: 'ssh', sessionId: 'test' })
  const writes: string[] = []
  // The real buffer, not a pass-through: a backspace with no rewrite in flight
  // takes its immediate path, so these assertions stay synchronous.
  let pipe: (data: string) => void = () => {}
  const extracted = runInNewContext(executable, {
    imeOrder: { push: (data: string) => pipe(data) },
    sidNow: 'test', gen: 1,
    getManagedTerminal: () => ({ onDataGeneration: 1 }),
    replySuppressUntil: 0, isMobileTouch: false, retryOnEnter: false,
    props: { mode: 'ssh', sessionId: 'test' },
    terminalInput: input,
    suggestions: { isVisible: () => false, close: () => {} },
    // The inline hint is driven from the pipeline but has nothing to say
    // about Backspace delivery.
    refreshGhostText: () => {},
    ghostSuggestion: () => null,
    setTimeout: () => 0,
    writeTerminalInput: (data: string, inAlt: boolean) => {
      const filtered = filterTerminalInput(data, inAlt)
      if (filtered) writes.push(filtered)
    },
  }) as { onData: (data: string) => void; processTerminalData: (data: string) => void }
  pipe = createKeystrokeReorder(data => extracted.processTerminalData(data)).push
  return { input, writes, onData: extracted.onData }
}

afterEach(() => vi.unstubAllGlobals())

describe('BaseTerminal Backspace delivery', () => {
  it.each([
    'clusconf copy -b -w p[1-8]c[0-9]',
    'vim /opt/gridview/agent/etc/output_redis.ini  ',
  ])('forwards every Backspace after native paste: %s', (command) => {
    const { input, writes, onData } = setup()
    // Cmd+V/context-menu paste writes straight to the session, bypassing
    // onData and the suggestion buffer.
    pasteWithScroll({
      bracketedPasteMode: true,
      write: data => writes.push(data),
      scrollToBottom: () => {},
    }, command)
    expect(input.cursorIndex.value).toBe(0)
    for (const _ of command) onData('\x7f')
    expect(writes.slice(1)).toEqual(Array(command.length).fill('\x7f'))
  })

  it('continues forwarding when a partially tracked command reaches local cursor zero', () => {
    const { input, writes, onData } = setup()
    onData('vim ')
    // A pasted path or shell completion adds text the tracker never sees.
    writes.push('/opt/gridview/agent/etc/output_redis.ini')
    for (let i = 0; i < 42; i++) onData('\x7f')
    expect(input.cursorIndex.value).toBe(0)
    expect(writes.slice(2)).toEqual(Array(42).fill('\x7f'))
  })

  it('forwards Backspace after remote shell history recall', () => {
    const { writes, onData } = setup()
    onData('\x1b[A')
    onData('\x7f')
    expect(writes).toEqual(['\x1b[A', '\x7f'])
  })

  it('leaves alternate-screen Backspace delivery intact', () => {
    const { input, writes, onData } = setup()
    input.handleSessionData('\x1b[?1049h')
    onData('\x7f')
    expect(writes).toEqual(['\x7f'])
  })
})
