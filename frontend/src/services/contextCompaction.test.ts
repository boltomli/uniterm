import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('@wailsio/runtime', () => ({ Events: { On: vi.fn(() => () => {}), Off: vi.fn() } }))
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  SummarizeAIContext: vi.fn(), ChatCompletion: vi.fn(),
  LoadAISessions: vi.fn().mockResolvedValue({ sessions: [] }),
  SaveAISessions: vi.fn().mockResolvedValue(undefined),
}))
vi.mock('../stores/settingsStore', () => ({ useSettingsStore: () => ({ activeModel: {
  apiKey: 'test', baseURL: 'https://example.invalid/v1', model: 'test', protocol: 'openai',
} }) }))

import { LoadAISessions, SaveAISessions, SummarizeAIContext } from '../../bindings/github.com/ys-ll/uniterm/app'
import { useAIStore } from '../stores/aiStore'
import { prepareAgentContext } from './contextCompaction'
import { estimateRequestTokens } from './aiContext'
import { ChatCancelledError } from './llm'

function longSession() {
  const store = useAIStore()
  store.createSession()
  for (let i = 0; i < 20; i++) store.addMessage({
    id: `message-${i}`, role: i % 2 === 0 ? 'user' : 'assistant',
    content: `${i}:` + 'history '.repeat(1400), createdAt: 1,
  })
  store.addMessage({ id: 'latest-user', role: 'user', content: 'Continue with the pending fix', _contextHeader: 'PowerShell; current directory C:/work', createdAt: 1 })
  return store
}

describe('incremental context compaction', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.mocked(SummarizeAIContext).mockResolvedValue('Goal: pending fix. Completed: inspected logs. Next: apply fix.')
  })

  it('does not request summaries for short conversations', async () => {
    const store = useAIStore()
    store.createSession()
    store.addMessage({ id: 'user', role: 'user', content: 'hello' })
    const result = await prepareAgentContext('rules', [])
    expect(SummarizeAIContext).not.toHaveBeenCalled()
    expect(result.messages).toEqual(store.conversation)
  })

  it('compresses incrementally, saves a checkpoint and preserves full history', async () => {
    const store = longSession()
    const full = JSON.stringify(store.messages)
    const result = await prepareAgentContext('rules', [{ description: 'read files' }])
    expect(result.failed).toBe(false)
    expect(JSON.stringify(store.messages)).toBe(full)
    expect(store.contextSummary?.content).toContain('pending fix')
    expect(estimateRequestTokens('rules', result.messages, [])).toBeLessThan(24000)
    expect(JSON.stringify(result.messages)).toContain('PowerShell')
    expect(JSON.stringify(result.messages)).toContain('Goal: pending fix')
    expect(store.compactionRequestId).toBe('')
    const calls = vi.mocked(SummarizeAIContext).mock.calls
    expect(calls.length).toBeGreaterThan(1)
    expect(calls[1][7]).toContain('Goal: pending fix')
    // doSave is debounced (500ms trailing) — flush before reading the payload.
    await new Promise((r) => setTimeout(r, 600))
    expect(JSON.stringify(vi.mocked(SaveAISessions).mock.calls)).toContain('contextSummary')
    const count = calls.length
    await prepareAgentContext('rules', [])
    expect(SummarizeAIContext).toHaveBeenCalledTimes(count)
  })

  it('keeps complete recent exchanges and shows a notice after summary failure', async () => {
    const store = longSession()
    vi.mocked(SummarizeAIContext).mockRejectedValue(new Error('offline'))
    const result = await prepareAgentContext('rules', [])
    expect(result.failed).toBe(true)
    expect(store.contextSummary).toBeUndefined()
    expect(store.contextNotice).toContain('failed')
    expect(JSON.stringify(result.messages)).toContain('Continue with the pending fix')
    expect(store.messages.length).toBe(21)
    await prepareAgentContext('rules', [], true)
    expect(SummarizeAIContext).toHaveBeenCalledTimes(1)
  })

  it('carries the checkpoint into later compression passes', async () => {
    const store = longSession()
    await prepareAgentContext('rules', [])
    const firstCheckpoint = store.contextSummary!
    const count = vi.mocked(SummarizeAIContext).mock.calls.length
    for (let i = 0; i < 16; i++) store.addMessage({
      id: `later-${i}`, role: i % 2 === 0 ? 'user' : 'assistant', content: 'new evidence '.repeat(1000),
    })
    vi.mocked(SummarizeAIContext).mockResolvedValue('Updated goal and new evidence')
    await prepareAgentContext('rules', [])
    expect(vi.mocked(SummarizeAIContext).mock.calls[count][7]).toBe(firstCheckpoint.content)
    expect(store.contextSummary?.throughMessageId).not.toBe(firstCheckpoint.throughMessageId)
    expect(store.contextSummary?.content).toBe('Updated goal and new evidence')
    expect(store.messages.some(m => m.id === 'message-0')).toBe(true)
  })

  it('does not commit partial summaries when a later chunk fails', async () => {
    const store = longSession()
    vi.mocked(SummarizeAIContext).mockResolvedValueOnce('partial handoff').mockRejectedValueOnce(new Error('offline'))
    const result = await prepareAgentContext('rules', [])
    expect(result.failed).toBe(true)
    expect(store.contextSummary).toBeUndefined()
  })

  it('rejects oversized summaries without committing a checkpoint', async () => {
    longSession()
    vi.mocked(SummarizeAIContext).mockResolvedValueOnce('PRIVATE_SUMMARY '.repeat(5000))
    const result = await prepareAgentContext('rules', [])
    expect(result.failed).toBe(true)
    expect(useAIStore().contextSummary).toBeUndefined()
    expect(useAIStore().contextNotice).toContain('failed')
  })

  it.each(['stop', 'rollback', 'session', 'superseded'])('discards results after %s', async (action) => {
    const store = longSession()
    let current = true
    vi.mocked(SummarizeAIContext).mockImplementationOnce(async () => {
      if (action === 'stop') store.stop()
      if (action === 'rollback') store.truncateFrom('message-2')
      if (action === 'session') store.createSession()
      if (action === 'superseded') current = false
      return 'stale summary'
    })
    await expect(prepareAgentContext('rules', [], false, () => current)).rejects.toBeInstanceOf(ChatCancelledError)
    expect(store.contextSummary).toBeUndefined()
    expect(store.compactionRequestId).toBe('')
  })

  it('invalidates checkpoints on deletion and persists rollback after switching sessions', async () => {
    const store = longSession()
    await prepareAgentContext('rules', [])
    const id = store.currentSessionId!
    expect(store.contextSummary).toBeDefined()
    store.switchSession(id)
    store.deleteMessage('message-0')
    expect(store.contextSummary).toBeUndefined()
    expect(store.sessions.find(s => s.id === id)?.messages.some(m => m.id === 'message-0')).toBe(false)
    store.truncateFrom('message-3')
    expect(store.sessions.find(s => s.id === id)?.messages.map(m => m.id)).toEqual(['message-1', 'message-2'])
  })

  it('restores checkpoints and environment headers from saved sessions', async () => {
    const store = longSession()
    await prepareAgentContext('rules', [])
    await store.doSave()
    await new Promise((r) => setTimeout(r, 600))
    const saved = vi.mocked(SaveAISessions).mock.calls.slice(-1)[0][0]
    vi.mocked(LoadAISessions).mockResolvedValueOnce(saved as any)
    setActivePinia(createPinia())
    const restored = useAIStore()
    await restored.init()
    restored.switchSession(saved.sessions[0].id)
    expect(restored.contextSummary?.content).toContain('pending fix')
    expect(JSON.stringify(restored.conversation)).toContain('PowerShell')
  })

  it('preserves multiple tool results through conversation normalization', () => {
    const store = useAIStore()
    const calls = ['a', 'b'].map(id => ({ id, type: 'function' as const, function: { name: 'read', arguments: '{}' } }))
    const messages = store.buildConversation([
      { id: 'call', role: 'assistant', content: '', tool_calls: calls },
      { id: 'a', role: 'tool', content: 'first result', tool_call_id: 'a' },
      { id: 'b', role: 'tool', content: 'second result', tool_call_id: 'b' },
    ])
    expect((messages[0].content as any[]).filter(b => b.type === 'tool_use')).toHaveLength(2)
    expect((messages[1].content as any[]).filter(b => b.type === 'tool_result')).toHaveLength(2)
  })

  it('rejects oversized current input instead of silently dropping it', async () => {
    const store = useAIStore()
    store.createSession()
    store.addMessage({ id: 'only', role: 'user', content: 'X'.repeat(200000) })
    await expect(prepareAgentContext('rules', [])).rejects.toThrow('budget')
    expect(store.messages[0].content.length).toBe(200000)
    expect(SummarizeAIContext).not.toHaveBeenCalled()
  })

  it('does not spend a summary request when the retained latest exchange alone is oversized', async () => {
    const store = longSession()
    store.addMessage({ id: 'huge-latest', role: 'user', content: 'X'.repeat(200000) })
    await expect(prepareAgentContext('rules', [])).rejects.toThrow('budget')
    expect(SummarizeAIContext).not.toHaveBeenCalled()
    expect(store.messages[store.messages.length - 1].content.length).toBe(200000)
  })
})
