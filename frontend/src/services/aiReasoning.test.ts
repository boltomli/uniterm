import { describe, expect, it } from 'vitest'
import type { AIModelConfig } from '../types/settings'
import { DEFAULT_REASONING, REASONING_EFFORTS, normalizeReasoning, reasoningParameters } from './aiReasoning'

const model: AIModelConfig = { id: 'm', name: 'Custom', apiKey: '', baseURL: 'https://gateway.example/v1', model: 'custom-alias', protocol: 'responses' }

describe('AI reasoning request parameters', () => {
  it('offers all effort choices for custom models', () => {
    expect(REASONING_EFFORTS).toEqual(['default', 'low', 'medium', 'high', 'xhigh', 'max'])
    expect(normalizeReasoning({ reasoningEffort: 'max' })).toEqual({ reasoningEffort: 'max' })
  })

  it.each(['responses', 'openai', 'anthropic'] as const)('omits controls for model defaults with %s', protocol => {
    expect(reasoningParameters({ ...model, protocol }, DEFAULT_REASONING)).toEqual({})
  })

  it('maps effort to the selected OpenAI-compatible protocol', () => {
    expect(reasoningParameters(model, { reasoningEffort: 'xhigh' })).toEqual({ reasoning: { effort: 'xhigh' } })
    expect(reasoningParameters({ ...model, protocol: 'openai' }, { reasoningEffort: 'max' })).toEqual({ reasoning_effort: 'max' })
  })

  it('sends explicit choices to Ark without filtering them by model name', () => {
    const ark = { ...model, model: 'ark-code-latest', baseURL: 'https://ark.cn-beijing.volces.com/api/coding/v3' }
    expect(reasoningParameters(ark, { reasoningEffort: 'low' })).toEqual({ thinking: { type: 'enabled' }, reasoning: { effort: 'low' } })
    expect(reasoningParameters({ ...ark, protocol: 'openai' }, { reasoningEffort: 'high' })).toEqual({ thinking: { type: 'enabled' }, reasoning_effort: 'high' })
    expect(reasoningParameters({ ...ark, model: 'glm-5.3-flash' }, DEFAULT_REASONING)).toEqual({})
  })

  it('uses Claude adaptive thinking or a bounded legacy budget', () => {
    const anthropic = { ...model, protocol: 'anthropic' as const, model: 'claude-sonnet-4-6' }
    expect(reasoningParameters(anthropic, { reasoningEffort: 'high' })).toEqual({ thinking: { type: 'adaptive' }, output_config: { effort: 'high' } })
    const legacy = reasoningParameters({ ...anthropic, model: 'claude-sonnet-4-5' }, { reasoningEffort: 'max' })
    expect((legacy.thinking as any).type).toBe('enabled')
    expect((legacy.thinking as any).budget_tokens).toBeLessThan(16384)
  })

  it('treats corrupt saved values as defaults', () => {
    expect(normalizeReasoning({ reasoningEffort: 'unknown' } as any)).toEqual(DEFAULT_REASONING)
  })

  it('ignores removed thinking switches and preserves only effort', () => {
    for (const thinkingMode of ['enabled', 'disabled']) {
      const saved = { thinkingMode, reasoningEffort: 'high' } as any
      expect(normalizeReasoning(saved)).toEqual({ reasoningEffort: 'high' })
      expect(reasoningParameters(model, saved)).toEqual({ reasoning: { effort: 'high' } })
      expect(reasoningParameters(model, { thinkingMode } as any)).toEqual({})
    }
  })
})
