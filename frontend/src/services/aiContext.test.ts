import { describe, expect, it } from 'vitest'
import type { AIMessage } from '../types/ai'
import {
  activeHistory, compactionCut, contextGroups, estimateRequestTokens,
  estimateTokens, splitSummaryTranscript, summaryTranscript, SUMMARY_CHUNK_TOKENS,
} from './aiContext'

const message = (id: string, role: AIMessage['role'], content = 'hello'): AIMessage => ({ id, role, content })

describe('AI context budgeting and compaction boundaries', () => {
  it('counts actual system, tools, headers and attachments without tokenizing base64', () => {
    const text = estimateRequestTokens('system', [{ role: 'user', content: 'hello' }], [])
    expect(estimateRequestTokens('system', [{ role: 'user', content: 'hello' }], [{ description: 'x'.repeat(10000) }])).toBeGreaterThan(text + 2000)
    expect(estimateTokens({ type: 'image', source: { data: 'A'.repeat(100_000) } })).toBe(4096)
  })

  it('never separates a parallel tool invocation from its results', () => {
    const call = message('call', 'assistant', '')
    call.tool_calls = ['a', 'b'].map(id => ({ id, type: 'function', function: { name: 'read', arguments: '{}' } }))
    const history = [message('old', 'user', 'x'.repeat(10000)), call,
      { ...message('a-result', 'tool', 'x'.repeat(10000)), tool_call_id: 'a' },
      { ...message('b-result', 'tool'), tool_call_id: 'b' }, message('reply', 'assistant'), message('new', 'user')]
    expect(contextGroups(history).map(g => g.map(m => m.id))).toEqual([
      ['old'], ['call', 'a-result', 'b-result'], ['reply'], ['new'],
    ])
    expect(compactionCut(history, 100)).toBe(4)
  })

  it('protects an unresolved tool chain and keeps an enormous latest input intact', () => {
    const pending = { ...message('call', 'assistant'), tool_calls: [{ id: 'pending', type: 'function' as const, function: { name: 'run', arguments: '{}' } }] }
    const history = [message('old', 'user'), pending, message('latest', 'user', 'x'.repeat(200000))]
    expect(contextGroups(history)[1].map(m => m.id)).toEqual(['call', 'latest'])
    expect(compactionCut(history, 100)).toBe(1)
  })

  it('uses checkpoint IDs and ignores missing or unsupported checkpoints', () => {
    const history = [message('a', 'user'), message('b', 'assistant'), message('c', 'user')]
    const summary = { version: 1 as const, content: 'handoff', throughMessageId: 'b', createdAt: 1 }
    expect(activeHistory(history, summary).map(m => m.id)).toEqual(['c'])
    expect(activeHistory(history, { ...summary, throughMessageId: 'deleted' })).toEqual(history)
  })

  it('includes task evidence, headers and text files but excludes hidden thinking and image bytes', () => {
    const m = { ...message('a', 'assistant', 'observed failure'), _contextHeader: 'PowerShell',
      _rawApiMsg: { content: [{ type: 'thinking', thinking: 'PRIVATE' }, { type: 'tool_use', id: 't', name: 'run', input: { command: 'pwd' } }] },
      attachments: [{ id: 'i', kind: 'image' as const, name: 'screenshot.png', mime: 'image/png', size: 10, data: 'SECRET_BASE64' },
        { id: 't', kind: 'text' as const, name: 'error.log', mime: 'text/plain', size: 10, data: 'actual error' }] }
    const transcript = summaryTranscript([m])
    expect(transcript).toContain('actual error')
    expect(transcript).toContain('PowerShell')
    expect(transcript).toContain('pwd')
    expect(transcript).not.toContain('PRIVATE')
    expect(transcript).not.toContain('SECRET_BASE64')
    expect(transcript).toContain('Image unavailable')
  })

  it('chunks legacy large histories losslessly within the summary request budget', () => {
    const text = ('终端日志😀 result\n').repeat(15000)
    const chunks = splitSummaryTranscript(text)
    expect(chunks.length).toBeGreaterThan(2)
    expect(chunks.join('')).toBe(text)
    for (const chunk of chunks) {
      expect(estimateTokens(chunk)).toBeLessThanOrEqual(SUMMARY_CHUNK_TOKENS + 2)
      const last = chunk.charCodeAt(chunk.length - 1)
      expect(last < 0xd800 || last > 0xdbff).toBe(true)
    }
  })
})
