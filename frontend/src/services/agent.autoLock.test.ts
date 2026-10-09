// Auto-lock: when an AI conversation turn starts with no locked panel, the
// panel that is active at that moment gets locked — same effect as the user
// clicking "Lock AI". An existing lock (manual, or from an earlier turn) is
// left untouched, so the AI keeps targeting the original terminal after tab
// switches. New/switched sessions clear the binding (aiStore side is covered
// in stores/aiStore.sessionLock.test.ts).

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// agent.ts transitively pulls in terminalAgent.ts → terminalManager.ts → xterm
// addons that expect a browser `self` global. Stub the wails runtime + the
// terminal manager so the module graph is small enough to load in vitest.
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  ChatCompletion: vi.fn(),
  GetSkillFile: vi.fn(),
  ListSkillFiles: vi.fn(),
  SessionWrite: vi.fn().mockResolvedValue(undefined),
  LoadAISessions: vi.fn().mockResolvedValue({ sessions: [], currentSessionId: '' }),
  SaveAISessions: vi.fn().mockResolvedValue(undefined),
  DisableSessionOutputLog: vi.fn().mockResolvedValue(undefined),
  RegisterSessionForPanel: vi.fn().mockResolvedValue(undefined),
  UnregisterSession: vi.fn().mockResolvedValue(undefined),
  LoadLocalState: vi.fn().mockResolvedValue({}),
  SaveLocalState: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('./terminalManager', () => ({
  getManagedTerminal: vi.fn(),
}))

// runAgent reads maxTurns from settingsStore; 0 keeps the autonomous loop from
// running so the test only exercises the turn preamble where the lock happens.
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: vi.fn(() => ({ settings: { ai: { maxTurns: 0 } } })),
}))

vi.mock('./terminalAgent', () => ({
  executeCommand: vi.fn(),
  startCommand: vi.fn(),
  sendTerminalKey: vi.fn(),
  captureTerminal: vi.fn(),
  collectOutput: vi.fn(),
  hasActiveSession: vi.fn().mockReturnValue(true),
}))

import { runAgent } from './agent'
import { useTabStore } from '../stores/tabStore'
import { usePanelStore } from '../stores/panelStore'

function setupActiveTerminalPanel(): string {
  const panelStore = usePanelStore()
  const tabStore = useTabStore()
  const panel = panelStore.createPanel(null, 'local')
  panelStore.bindSession(panel.id, 'sess-1')
  tabStore.createTab('terminal', 'T1', panel.id)
  return panel.id
}

describe('runAgent auto-locks the active panel', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    // tabStore keeps its state in a module-level reactive singleton, so the
    // lock set survives pinia resets between tests — clear it explicitly.
    useTabStore().clearAILockedPanels()
  })

  it('locks the panel active at conversation start when nothing is locked', async () => {
    const tabStore = useTabStore()
    const panelId = setupActiveTerminalPanel()
    expect(tabStore.getAILockedPanels()).toEqual([])

    await runAgent('hello')

    expect(tabStore.getAILockedPanels()).toEqual([panelId])
  })

  it('does not touch an existing lock (manual lock wins)', async () => {
    const panelStore = usePanelStore()
    const tabStore = useTabStore()
    const panelA = panelStore.createPanel(null, 'local')
    panelStore.bindSession(panelA.id, 'sess-a')
    const panelB = panelStore.createPanel(null, 'local')
    panelStore.bindSession(panelB.id, 'sess-b')
    const tabA = tabStore.createTab('terminal', 'T-A', panelA.id)
    tabStore.createTab('terminal', 'T-B', panelB.id)

    // User manually locked panel B, but panel A's tab is currently active
    tabStore.addAILockedPanel(panelB.id)
    tabStore.setActiveTab(tabA.id)

    await runAgent('hello')

    expect(tabStore.getAILockedPanels()).toEqual([panelB.id])
  })
})
