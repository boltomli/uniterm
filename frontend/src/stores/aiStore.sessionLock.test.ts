// Session-boundary behavior of the AI panel binding: creating a new session
// or switching to another session clears the locked panel, so the next
// conversation re-locks onto whatever terminal is active at that moment.

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadAISessions: vi.fn().mockResolvedValue({ sessions: [], currentSessionId: '' }),
  SaveAISessions: vi.fn().mockResolvedValue(undefined),
  DisableSessionOutputLog: vi.fn().mockResolvedValue(undefined),
  RegisterSessionForPanel: vi.fn().mockResolvedValue(undefined),
  UnregisterSession: vi.fn().mockResolvedValue(undefined),
  LoadLocalState: vi.fn().mockResolvedValue({}),
  SaveLocalState: vi.fn().mockResolvedValue(undefined),
}))

import { useAIStore } from './aiStore'
import { useTabStore } from './tabStore'

describe('AI session changes clear the terminal binding', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('createSession clears the locked panel', () => {
    const ai = useAIStore()
    const tabStore = useTabStore()
    tabStore.addAILockedPanel('panel-1')
    expect(tabStore.getAILockedPanels()).toEqual(['panel-1'])

    ai.createSession()

    expect(tabStore.getAILockedPanels()).toEqual([])
  })

  it('switchSession clears the locked panel', () => {
    const ai = useAIStore()
    const tabStore = useTabStore()
    ai.createSession()
    const firstId = ai.currentSessionId!
    ai.createSession()

    tabStore.addAILockedPanel('panel-1')
    expect(tabStore.getAILockedPanels()).toEqual(['panel-1'])

    ai.switchSession(firstId)

    expect(tabStore.getAILockedPanels()).toEqual([])
  })
})
