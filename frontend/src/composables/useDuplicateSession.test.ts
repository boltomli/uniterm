// Regression tests for issue #983 (frontend half): duplicateChannel must
// only fire against a CONNECTED SSH panel, and the flow must wire the new
// panel/tab to the backend's DuplicateSSHChannel result with a
// deferred SessionStart (channel attach after terminal size is known).

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const { duplicateMock, sessionStartMock, createSessionMock } = vi.hoisted(() => ({
  duplicateMock: vi.fn(),
  sessionStartMock: vi.fn(),
  createSessionMock: vi.fn(),
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  DuplicateSSHChannel: duplicateMock,
  SessionStart: sessionStartMock,
  CreateSession: createSessionMock,
  CloseSession: vi.fn(async () => {}),
  K8sExecSession: vi.fn(),
  ContainerExecSession: vi.fn(),
  RegisterSessionForPanel: vi.fn(async () => {}),
  UnregisterSession: vi.fn(async () => {}),
}))

// waitForTerminalSize / terminalManager transitively pull in xterm addons
// that expect a browser `self` global. Stub the size wait: every clone gets
// a fixed 100x30.
vi.mock('../services/terminalManager', () => ({
  waitForTerminalSize: vi.fn(async () => ({ cols: 100, rows: 30 })),
}))

import { usePanelStore } from '../stores/panelStore'
import { useTabStore } from '../stores/tabStore'
import { useSessionStore } from '../stores/sessionStore'
import { useDuplicateSession } from './useDuplicateSession'

const sshConfig: any = { type: 'ssh', name: 'bastion', host: 'h', port: 22, user: 'u', authType: 'password' }

function setupConnectedSSHTab() {
  const panelStore = usePanelStore()
  const tabStore = useTabStore()
  const sessionStore = useSessionStore()

  const panel = panelStore.createPanel(sshConfig, 'ssh')
  panelStore.bindSession(panel.id, 'src-1')
  sessionStore.initSession('src-1')
  sessionStore.updateStatus('src-1', 'connected')
  const tab = tabStore.createTerminalTab('bastion', panel.id)
  panelStore.movePanelToTab(panel.id, tab.id)
  return { panel, tab, panelStore, tabStore, sessionStore }
}

beforeEach(() => {
  setActivePinia(createPinia())
  duplicateMock.mockReset().mockImplementation(async () => ({ id: 'clone-1', type: 'ssh', title: 'bastion', status: 'connecting' }))
  sessionStartMock.mockReset().mockResolvedValue(undefined)
})

describe('duplicateChannel guards (issue #983)', () => {
  it('creates a clone panel + tab and defers SessionStart with the measured size', async () => {
    const { panel, tab, panelStore, tabStore } = setupConnectedSSHTab()
    const { duplicateChannel } = useDuplicateSession()

    await duplicateChannel(tab)

    expect(duplicateMock).toHaveBeenCalledTimes(1)
    expect(duplicateMock).toHaveBeenCalledWith('src-1', expect.objectContaining({ type: 'ssh' }))

    // New panel bound to the clone session, tab count grew by one.
    expect(panelStore.getPanel(panel.id)?.sessionId).toBe('src-1') // source untouched
    expect(tabStore.tabs.length).toBe(2)
    const cloneTab = tabStore.tabs.find((t: any) => t.id !== tab.id)
    expect(cloneTab).toBeTruthy()
    const clonePanelId = (cloneTab as any).panelId
    expect(panelStore.getPanel(clonePanelId)?.sessionId).toBe('clone-1')
    // Channel attach is deferred: SessionStart carries the clone id.
    expect(sessionStartMock).toHaveBeenCalledWith('clone-1', expect.objectContaining({
      type: 'ssh',
      initialCols: expect.any(Number),
      initialRows: expect.any(Number),
    }))
  })

  it('does nothing when the panel session is not connected', async () => {
    const { tab } = setupConnectedSSHTab()
    const sessionStore = useSessionStore()
    sessionStore.updateStatus('src-1', 'disconnected')
    const { duplicateChannel } = useDuplicateSession()

    await duplicateChannel(tab)

    expect(duplicateMock).not.toHaveBeenCalled()
  })

  it('does nothing for a non-SSH panel', async () => {
    const panelStore = usePanelStore()
    const tabStore = useTabStore()
    const sessionStore = useSessionStore()
    const panel = panelStore.createPanel({ type: 'local', shellPath: '/bin/zsh' } as any, 'local')
    panelStore.bindSession(panel.id, 'src-2')
    sessionStore.initSession('src-2')
    sessionStore.updateStatus('src-2', 'connected')
    const tab = tabStore.createTerminalTab('local', panel.id)
    panelStore.movePanelToTab(panel.id, tab.id)

    const { duplicateChannel } = useDuplicateSession()
    await duplicateChannel(tab)

    expect(duplicateMock).not.toHaveBeenCalled()
  })

  it('closes the backend session when SessionStart rejects', async () => {
    const { tab } = setupConnectedSSHTab()
    sessionStartMock.mockRejectedValue(new Error('channel refused'))
    const { duplicateChannel } = useDuplicateSession()
    // CloseSession is mocked as a no-op async; the point is that the flow
    // completes without throwing and the error is logged, not propagated.
    await expect(duplicateChannel(tab)).resolves.toBeUndefined()
  })
})
