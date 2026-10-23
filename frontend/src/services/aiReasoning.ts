import type { AIModelConfig } from '../types/settings'
import type { AIReasoningEffort, AIReasoningPreference } from '../types/ai'

export const REASONING_EFFORTS: AIReasoningEffort[] = ['default', 'low', 'medium', 'high', 'xhigh', 'max']
export const DEFAULT_REASONING: AIReasoningPreference = { reasoningEffort: 'default' }

// Providers remain authoritative about supported options. Normalize persisted
// values only; model names must not hide menu options or discard user choices.
export function normalizeReasoning(value?: Partial<AIReasoningPreference>): AIReasoningPreference {
  return {
    reasoningEffort: REASONING_EFFORTS.includes(value?.reasoningEffort as AIReasoningEffort) ? value!.reasoningEffort! : 'default',
  }
}

export function reasoningParameters(model: AIModelConfig, value?: Partial<AIReasoningPreference>): Record<string, unknown> {
  const { reasoningEffort: effort } = normalizeReasoning(value)
  if (effort === 'default') return {}
  let host = ''
  try { host = new URL(model.baseURL).hostname } catch { /* Custom URLs still use their configured protocol. */ }
  const thinkingExtension = host.endsWith('.volces.com') || host.endsWith('.volcengine.com')
    || host.endsWith('.bytepluses.com') || host.endsWith('bigmodel.cn') || host === 'api.z.ai'
    || host.endsWith('deepseek.com') || host.endsWith('moonshot.cn') || host.endsWith('moonshot.ai')
  if (model.protocol === 'anthropic') {
    // This selects the wire format, not which controls the user may choose.
    // Legacy Claude versions require an explicit budget instead of adaptive.
    const legacy = /^claude-(?:3[.-]7-sonnet(?:-|$)|(?:sonnet|opus|haiku)-4(?:[.-][015])?(?:-(?:20\d{6}|latest))?$)/.test(model.model.toLowerCase())
    if (legacy) {
      const budgets: Record<Exclude<AIReasoningEffort, 'default'>, number> = { low: 2048, medium: 4096, high: 8192, xhigh: 12288, max: 14336 }
      return { thinking: { type: 'enabled', budget_tokens: budgets[effort] } }
    }
    return {
      thinking: { type: thinkingExtension ? 'enabled' : 'adaptive' },
      output_config: { effort },
    }
  }
  if (thinkingExtension) {
    return {
      thinking: { type: 'enabled' },
      ...(model.protocol === 'responses' ? { reasoning: { effort } } : { reasoning_effort: effort }),
    }
  }
  return model.protocol === 'responses' ? { reasoning: { effort } } : { reasoning_effort: effort }
}
