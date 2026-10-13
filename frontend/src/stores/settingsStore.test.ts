// Regression tests for security.md「前端核心」:
//   correctness — a failed LoadSettings left `settings` holding DEFAULT_SETTINGS
//     (loaded=true anyway), so any subsequent mutator or the deep ai watcher
//     wrote the defaults over the user's real settings.json.
//   correctness — store:settings:changed re-merged legacy settings.json over
//     the live settings.value.ai, dragging stale models back.
//   correctness — DEFAULT_SETTINGS was shallow-copied ({ ...DEFAULT_SETTINGS }),
//     so mutating the store mutated the defaults themselves ("reset" stopped
//     resetting); aiConfigStore shared the default model objects too.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const { mockLoadSettings, mockSaveSettings, mockLoadAIModels, mockSaveAIModels, handlers } = vi.hoisted(() => {
  const handlers: Record<string, (ev: { data: unknown }) => void> = {}
  return {
    mockLoadSettings: vi.fn(),
    mockSaveSettings: vi.fn().mockResolvedValue(undefined),
    mockLoadAIModels: vi.fn().mockResolvedValue(undefined),
    mockSaveAIModels: vi.fn().mockResolvedValue(undefined),
    handlers,
  }
})

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: vi.fn((name: string, handler: (ev: { data: unknown }) => void) => {
      handlers[name] = handler
      return () => { delete handlers[name] }
    }),
    Off: vi.fn(),
  },
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadSettings: mockLoadSettings,
  SaveSettings: mockSaveSettings,
  GetAvailableShells: vi.fn().mockResolvedValue([]),
  SetDefaultSessionLogDir: vi.fn().mockResolvedValue(undefined),
  LoadAIModels: mockLoadAIModels,
  SaveAIModels: mockSaveAIModels,
}))

import { useSettingsStore } from './settingsStore'
import { useAIConfigStore } from './aiConfigStore'
import { DEFAULT_SETTINGS } from '../types/settings'
import { nextTick } from 'vue'

function stubBrowserGlobals() {
  vi.stubGlobal('window', {
    matchMedia: () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
  })
  vi.stubGlobal('document', { documentElement: { style: {}, dataset: {} } })
  vi.stubGlobal('localStorage', { setItem: vi.fn(), getItem: vi.fn(() => null) })
}

describe('failed settings load never writes defaults (security.md FE-Medium)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    stubBrowserGlobals()
  })

  it('blocks every mutator after LoadSettings fails', async () => {
    mockLoadSettings.mockRejectedValue(new Error('corrupt json'))
    const store = useSettingsStore()
    await store.init()

    // Explicit mutators must not persist the default blob over settings.json.
    store.updateTheme('light')
    store.updateTerminal({ fontSize: 20 })
    store.addModel({
      id: 'x', name: 'X', apiKey: '', baseURL: 'https://api.openai.com/v1',
      model: 'gpt-4o', protocol: 'anthropic',
    })
    // Direct deep mutation through the ai deep-watcher must not persist either.
    store.settings.ai.maxTurns = 99

    // Explicit mutators call save() unawaited; the deep watcher flushes on
    // nextTick. Await both signals rather than a guessed delay.
    await Promise.resolve()
    await nextTick()
    expect(mockSaveSettings).not.toHaveBeenCalled()
  })

  it('saves normally when LoadSettings succeeds', async () => {
    mockLoadSettings.mockResolvedValue({ ...DEFAULT_SETTINGS, theme: 'dark' })
    const store = useSettingsStore()
    await store.init()

    store.updateTheme('light')
    await nextTick()
    expect(mockSaveSettings).toHaveBeenCalledTimes(1)
  })

  it('keeps DEFAULT_SETTINGS pristine when a failed load is followed by mutations', async () => {
    mockLoadSettings.mockRejectedValue(new Error('corrupt json'))
    const store = useSettingsStore()
    await store.init()

    store.settings.terminal.fontSize = 99
    store.settings.ai.models.push({
      id: 'leak', name: 'Leak', apiKey: '', baseURL: 'x', model: 'm', protocol: 'anthropic',
    })
    store.settings.ai.maxTurns = 77

    expect(DEFAULT_SETTINGS.terminal.fontSize).toBe(14)
    expect(DEFAULT_SETTINGS.ai.models).toHaveLength(1)
    expect(DEFAULT_SETTINGS.ai.maxTurns).toBe(20)
  })
})

describe('store:settings:changed keeps the live ai slice (security.md FE-Medium)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    stubBrowserGlobals()
  })

  it('does not let a legacy settings.json pull stale models back over settings.value.ai', async () => {
    mockLoadSettings.mockResolvedValue({ ...DEFAULT_SETTINGS })
    const store = useSettingsStore()
    await store.init()

    const liveModels = [
      { id: 'fresh', name: 'Fresh', apiKey: 'k', baseURL: 'https://api.openai.com/v1', model: 'gpt-4o', protocol: 'anthropic' as const },
    ]
    store.settings.ai.models = liveModels
    store.settings.ai.activeModelId = 'fresh'

    handlers['store:settings:changed']?.({
      data: {
        ...DEFAULT_SETTINGS,
        ai: {
          maxTurns: 5,
          activeModelId: 'stale',
          models: [{ id: 'stale', name: 'Stale', apiKey: '', baseURL: '', model: 'old', protocol: 'anthropic' }],
        },
      },
    })

    expect(store.settings.ai.models.map(m => m.id)).toEqual(['fresh'])
    expect(store.settings.ai.activeModelId).toBe('fresh')
    expect(store.settings.ai.maxTurns).toBe(20)
    // Non-ai fields still come from the sync payload.
    expect(store.settings.theme).toBe(DEFAULT_SETTINGS.theme)
  })
})

describe('DEFAULT_SETTINGS is cloned, never shared (security.md FE-Medium)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    stubBrowserGlobals()
  })

  it('does not share model objects with DEFAULT_SETTINGS via aiConfigStore', async () => {
    mockLoadAIModels.mockResolvedValue(undefined)
    const cfg = useAIConfigStore()
    await cfg.load()

    expect(cfg.models[0]).not.toBe(DEFAULT_SETTINGS.ai.models[0])
    cfg.models[0].name = 'Mutated'
    cfg.models.push({
      id: 'extra', name: 'Extra', apiKey: '', baseURL: '', model: '', protocol: 'anthropic',
    })

    expect(DEFAULT_SETTINGS.ai.models).toHaveLength(1)
    expect(DEFAULT_SETTINGS.ai.models[0].name).toBe('Default')
  })

  it('does not share the terminal object between the store and DEFAULT_SETTINGS', async () => {
    mockLoadSettings.mockRejectedValue(new Error('corrupt json'))
    const store = useSettingsStore()
    await store.init()

    expect(store.settings.terminal).not.toBe(DEFAULT_SETTINGS.terminal)
    store.settings.terminal.fontSize = 99
    expect(DEFAULT_SETTINGS.terminal.fontSize).toBe(14)
  })
})
