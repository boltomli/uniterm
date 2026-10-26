// Regression tests for the unified connection launcher: persistence
// bookkeeping shared by save / connect / connect-only, and per-type routing
// (session kind, session-less types, terminal fallback).
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const { createSessionMock, recordRecentMock } = vi.hoisted(() => ({
  createSessionMock: vi.fn(),
  recordRecentMock: vi.fn(),
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  CreateSession: createSessionMock,
  RecordRecentConnection: recordRecentMock,
  SaveConnections: vi.fn(async () => {}),
  LoadConnections: vi.fn(async () => ({ groups: [], connections: [] })),
  DisableSessionOutputLog: vi.fn(async () => {}),
  RegisterSessionForPanel: vi.fn(async () => {}),
  UnregisterSession: vi.fn(async () => {}),
}))

vi.mock('../services/message', () => ({
  msg: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), copyable: vi.fn() },
}))

import { useConnectionStore, migrateLegacyDatabaseTypes } from '../stores/connectionStore'
import { usePanelStore } from '../stores/panelStore'
import { useTabStore } from '../stores/tabStore'
import { launchConnection, launchFileBrowser, configureLauncher } from './connectionLauncher'
import { msg } from '../services/message'

const ensureCredentials = vi.fn(async (c: any) => c)

function baseConfig(type: string): any {
  return { type, name: '', host: 'h', port: 22, user: 'u', authType: 'password' }
}

beforeEach(() => {
  setActivePinia(createPinia())
  createSessionMock.mockReset().mockImplementation(async (type: string) => ({ id: `sid-${type}` }))
  recordRecentMock.mockReset()
  ensureCredentials.mockClear()
  configureLauncher({ ensureCredentials, closeStartAndReposition: () => () => {} })
})

describe('launchConnection persistence', () => {
  it('connect-only (persist=false) saves nothing but still connects', async () => {
    const conns = useConnectionStore()
    const cfg = baseConfig('sftp')
    await launchConnection(cfg, { persist: false })

    expect(conns.connections).toHaveLength(0)
    expect(recordRecentMock).not.toHaveBeenCalled()
    // Transient session still gets a stable id for panel/tab wiring.
    expect(cfg.id).toBeTruthy()
    expect(createSessionMock).toHaveBeenCalledWith('sftp', cfg)
  })

  it('connect (persist=true) adds the connection and records recent', async () => {
    const conns = useConnectionStore()
    const cfg = baseConfig('sftp')
    await launchConnection(cfg, { persist: true })

    expect(conns.connections).toHaveLength(1)
    expect(recordRecentMock).toHaveBeenCalledTimes(1)
  })

  it('wasEdit updates the existing connection instead of adding', async () => {
    const conns = useConnectionStore()
    await conns.add({ ...baseConfig('sftp'), id: 'c1', name: 'old' } as any)
    await launchConnection({ ...baseConfig('sftp'), id: 'c1', name: 'new' }, { persist: true, wasEdit: true })

    expect(conns.connections).toHaveLength(1)
    expect(conns.connections[0].name).toBe('new')
  })
})

describe('launchConnection routing', () => {
  it('binds SPICE to the local backend proxy and passes through SSH tunnel credentials', async () => {
    const proxyAddr = 'ws://127.0.0.1:12345/spice-session-token'
    createSessionMock.mockResolvedValue({ id: 'spice-session', proxyAddr })
    const cfg = { ...baseConfig('spice'), host: 'guest.internal', port: 5900,
      tunnelSSHConnId: 'jump', tunnelSSHUser: 'jump-user', tunnelSSHPassword: 'jump-password' }
    await launchConnection(cfg, { persist: false })
    expect(createSessionMock).toHaveBeenCalledWith('spice', expect.objectContaining(cfg))
    const tab = useTabStore().tabs.at(-1)!
    expect(tab.type).toBe('spice')
    const panelId = (tab as any).panelId
    expect(usePanelStore().getPanel(panelId)?.sessionId).toBe('spice-session')
    expect(usePanelStore().getProxyAddr(panelId)).toBe(proxyAddr)
    expect(cfg.host).toBe('guest.internal')
    expect(cfg.port).toBe(5900)
  })

  for (const obsoleteEngine of [undefined, 'microsoft', 'ironrdp']) {
    it(`opens a canonical RDP tab and preserves its proxy/settings (legacy engine=${obsoleteEngine})`, async () => {
      createSessionMock.mockResolvedValue({ id: 'rdp-session', proxyAddr: 'ws://127.0.0.1:1234/token' })
      const cfg = { ...baseConfig('rdp'), rdpEngine: obsoleteEngine, rdpEnableNLA: true, rdpAdminSession: true }
      await launchConnection(cfg, { persist: false })
      expect(createSessionMock).toHaveBeenCalledWith('rdp', expect.objectContaining({ rdpEnableNLA: true, rdpAdminSession: true }))
      const tab = useTabStore().tabs.at(-1)!
      expect(tab.type).toBe('rdp')
      const panelId = (tab as any).panelId
      expect(usePanelStore().getPanel(panelId)?.type).toBe('rdp')
      expect(usePanelStore().getPanel(panelId)?.sessionId).toBe('rdp-session')
      expect(usePanelStore().getProxyAddr(panelId)).toBe('ws://127.0.0.1:1234/token')
    })
  }

  const cases: [string, string][] = [
    ['sftp', 'sftp'],
    ['scp', 'scp'],
    ['ftp', 'ftp'],
    ['smb', 'smb'],
    ['webdav', 'webdav'],
    ['s3', 's3'],
    ['rdp', 'rdp'],
    ['vnc', 'vnc'],
    ['spice', 'spice'],
    ['monitor', 'monitor'],
    ['database', 'database'],
  ]

  for (const [type, sessionType] of cases) {
    it(`routes ${type} to a ${sessionType} session`, async () => {
      await launchConnection(baseConfig(type))
      expect(createSessionMock).toHaveBeenCalledWith(sessionType, expect.anything())
    })
  }

  it('connects a legacy database+dbType:redis config after store migration', async () => {
    // The store migrates legacy NoSQL configs on load/add; the launcher then
    // sees a standalone redis config.
    const conns = useConnectionStore()
    const cfg: any = { ...baseConfig('database'), dbType: 'redis' }
    await conns.add(cfg)
    expect(cfg.type).toBe('redis')
    expect(cfg.dbType).toBeUndefined()
    await launchConnection(cfg)
    expect(createSessionMock).toHaveBeenCalledWith('redis', expect.anything())
  })

  it('does not create sessions for component-owned types (x11-desktop, k8s, container)', async () => {
    const tabsBefore = useTabStore().tabs.length
    for (const type of ['x11-desktop', 'k8s', 'container']) {
      await launchConnection(baseConfig(type))
    }
    expect(createSessionMock).not.toHaveBeenCalled()
    expect(useTabStore().tabs.length).toBe(tabsBefore + 3)
  })

  it('falls back to the terminal path for types without a spec (ssh)', async () => {
    const connectTerminal = vi.fn()
    await launchConnection(baseConfig('ssh'), { connectTerminal })
    expect(connectTerminal).toHaveBeenCalledTimes(1)
    expect(createSessionMock).not.toHaveBeenCalled()
  })
})

describe('migrateLegacyDatabaseTypes', () => {
  it('rewrites NoSQL database configs to standalone types and drops dbType', () => {
    const conns: any[] = [
      { type: 'database', dbType: 'redis' },
      { type: 'database', dbType: 'mongodb' },
      { type: 'database', dbType: 'elasticsearch' },
      { type: 'database', dbType: 'mysql' },
      { type: 'ssh' },
    ]
    migrateLegacyDatabaseTypes(conns)
    expect(conns.map(c => c.type)).toEqual(['redis', 'mongodb', 'elasticsearch', 'database', 'ssh'])
    expect(conns[3].dbType).toBe('mysql')
  })

  it('is idempotent', () => {
    const conns: any[] = [{ type: 'redis' }]
    migrateLegacyDatabaseTypes(conns)
    expect(conns[0]).toEqual({ type: 'redis' })
  })
})

describe('launchFileBrowser', () => {
  it('opens an SFTP browser with the connection protocol preference', async () => {
    const cfg = { ...baseConfig('ssh'), fileTransferProto: 'scp' }
    await launchFileBrowser(cfg)
    expect(createSessionMock).toHaveBeenCalledWith('scp', expect.anything())
  })

  it('does not persist a connect-only config', async () => {
    const conns = useConnectionStore()
    await launchFileBrowser(baseConfig('ssh'), { persist: false })
    expect(conns.connections).toHaveLength(0)
  })
})

describe('session failure handling', () => {
  // tabStore/panelState hold module-level reactive state that survives pinia
  // resets, so assertions use count deltas instead of absolute values.
  it('session-first types (vnc) roll back the panel and never open a tab', async () => {
    createSessionMock.mockRejectedValue(new Error('boom'))
    const panels = usePanelStore()
    const tabs = useTabStore()
    const tabsBefore = tabs.tabs.length
    const panelsBefore = panels.panels.size
    await launchConnection(baseConfig('vnc'))

    expect(panels.panels.size).toBe(panelsBefore)
    expect(tabs.tabs).toHaveLength(tabsBefore)
  })

  it('tab-first types (ftp) close the tab and remove the panel', async () => {
    createSessionMock.mockRejectedValue(new Error('boom'))
    const panels = usePanelStore()
    const tabs = useTabStore()
    const tabsBefore = tabs.tabs.length
    const panelsBefore = panels.panels.size
    await launchConnection(baseConfig('ftp'))

    expect(panels.panels.size).toBe(panelsBefore)
    expect(tabs.tabs).toHaveLength(tabsBefore)
  })

  it('database tabs stay open with an error status and toast', async () => {
    createSessionMock.mockRejectedValue(new Error('boom'))
    const panels = usePanelStore()
    const tabs = useTabStore()
    const tabsBefore = tabs.tabs.length
    await launchConnection(baseConfig('database'))

    expect(tabs.tabs).toHaveLength(tabsBefore + 1)
    expect(createSessionMock).toHaveBeenCalledWith('database', expect.anything())
    expect(msg.error).toHaveBeenCalled()
    const panelsList = [...panels.panels.values()]
    const panel = panelsList[panelsList.length - 1]
    expect(panel.status).toBe('error')
  })
})
