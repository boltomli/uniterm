import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AIModelConfig } from '../types/settings'

const { settings, local, ai } = vi.hoisted(() => ({
  settings: { activeModel: undefined as AIModelConfig | undefined },
  local: { state: { ai: { modelPreferences: {} as Record<string, any> } } },
  ai: { currentSessionId: 'session' },
}))
vi.mock('@wailsio/runtime', () => ({ Events: { On: vi.fn(() => () => {}) } }))
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({ ChatCompletion: vi.fn() }))
vi.mock('../stores/settingsStore', () => ({ useSettingsStore: () => settings }))
vi.mock('../stores/localStateStore', () => ({ useLocalStateStore: () => local }))
vi.mock('../stores/aiStore', () => ({ useAIStore: () => ai }))
import { ChatCompletion } from '../../bindings/github.com/ys-ll/uniterm/app'
import { captureChatConfiguration, chat } from './llm'

beforeEach(() => {
  vi.clearAllMocks()
  settings.activeModel = { id: 'm', name: 'Model', apiKey: 'test-key', baseURL: 'https://gateway.example/v1', protocol: 'responses', model: 'alias' }
  local.state.ai.modelPreferences = {}
  vi.mocked(ChatCompletion).mockResolvedValue(JSON.stringify({ role: 'assistant', content: [{ type: 'text', text: 'ok' }] }))
})

describe('chat reasoning controls', () => {
  it('does not send controls when the user selects model defaults', async () => {
    await chat({ system: 'rules', messages: [] })
    const body = JSON.parse(vi.mocked(ChatCompletion).mock.calls[0][4])
    expect(body.reasoning).toBeUndefined()
    expect(body.thinking).toBeUndefined()
  })

  it('freezes model and reasoning settings across tool steps', async () => {
    local.state.ai.modelPreferences.m = { reasoningEffort: 'high' }
    const configuration = captureChatConfiguration()!
    settings.activeModel!.model = 'different-model'
    local.state.ai.modelPreferences.m.reasoningEffort = 'low'
    await chat({ system: 'rules', messages: [], configuration })
    await chat({ system: 'rules', messages: [], configuration })
    for (const call of vi.mocked(ChatCompletion).mock.calls) {
      expect(call[3]).toBe('alias')
      expect(JSON.parse(call[4]).reasoning).toEqual({ effort: 'high' })
    }
  })

  it('surfaces unsupported parameters without retrying or changing saved choices', async () => {
    const preference = { reasoningEffort: 'max' }
    local.state.ai.modelPreferences.m = preference
    vi.mocked(ChatCompletion).mockRejectedValueOnce(new Error('HTTP 400: {"error":{"code":"unsupported_parameter","param":"reasoning.effort","message":"Reasoning effort max is not supported"}}'))
    await expect(chat({ system: 'rules', messages: [] })).rejects.toThrow('Reasoning effort max is not supported')
    expect(ChatCompletion).toHaveBeenCalledTimes(1)
    expect(local.state.ai.modelPreferences.m).toEqual(preference)
  })
})
