import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// identityStore (imported transitively by formatConnSubtitle) pulls in the
// Wails bindings and event runtime; stub both so the import is side-effect safe.
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadIdentities: vi.fn(async () => ({ identities: [] })),
  SaveIdentities: vi.fn(async () => {}),
}))

import { formatConnSubtitle, parseQuickConnect } from './quickConnect'
import { useIdentityStore } from '../stores/identityStore'
import type { ConnectionConfig } from '../types/session'

describe('formatConnSubtitle', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('shows user@host for a plain password connection', () => {
    const cfg = { type: 'ssh', host: 'srv1', port: 22, user: 'alice', authType: 'password' } as ConnectionConfig
    expect(formatConnSubtitle(cfg)).toBe('ssh alice@srv1')
  })

  it('resolves the username from the referenced identity', () => {
    const identities = useIdentityStore()
    identities.identities = [{ id: 'id-1', name: 'work', username: 'bob', authType: 'key' }]

    const cfg = { type: 'ssh', host: 'srv1', port: 22, user: '', authType: 'identity', identityId: 'id-1' } as ConnectionConfig
    expect(formatConnSubtitle(cfg)).toBe('ssh bob@srv1')
  })

  it('falls back to host-only when the identity no longer exists', () => {
    const cfg = { type: 'ssh', host: 'srv1', port: 22, user: '', authType: 'identity', identityId: 'gone' } as ConnectionConfig
    expect(formatConnSubtitle(cfg)).toBe('ssh srv1')
  })
})

describe('parseQuickConnect protocols', () => {
  // Locks the registry-derived protocol table: every quick-connect prefix
  // routes to its connection type, aliases included.
  const cases: [string, string, string | undefined][] = [
    ['ssh', 'ssh', undefined],
    ['telnet', 'telnet', undefined],
    ['mosh', 'mosh', undefined],
    ['rdp', 'rdp', undefined],
    ['vnc', 'vnc', undefined],
    ['spice', 'spice', undefined],
    ['ftp', 'ftp', undefined],
    ['sftp', 'sftp', undefined],
    ['scp', 'scp', undefined],
    ['smb', 'smb', undefined],
    ['s3', 's3', undefined],
    ['webdav', 'webdav', undefined],
    ['tcp', 'tcp', undefined],
    ['mysql', 'database', 'mysql'],
    ['postgres', 'database', 'postgres'],
    ['postgresql', 'database', 'postgres'],
    ['oracle', 'database', 'oracle'],
    ['sqlserver', 'database', 'sqlserver'],
    ['rqlite', 'database', 'rqlite'],
    ['redis', 'redis', undefined],
    ['mongodb', 'mongodb', undefined],
    ['mongo', 'mongodb', undefined],
    ['es', 'elasticsearch', undefined],
    ['elasticsearch', 'elasticsearch', undefined],
    ['opensearch', 'elasticsearch', undefined],
    ['http', 'webdav', undefined],
    ['https', 'webdav', undefined],
  ]
  for (const [prefix, type, dbType] of cases) {
    it(`parses "${prefix}"`, () => {
      const cfg = parseQuickConnect(`${prefix} user@h:1234`) as any
      expect(cfg.type).toBe(type)
      expect(cfg.dbType).toBe(dbType)
      expect(cfg.port).toBe(1234)
    })
  }

  it('falls back to ssh for a bare host', () => {
    const cfg = parseQuickConnect('h') as any
    expect(cfg.type).toBe('ssh')
  })
})

describe('parseQuickConnect credentials and IPv6', () => {
  // Credentials split from the host at the LAST '@' (passwords may contain
  // '@'), userinfo splits at the FIRST ':' (passwords may contain ':');
  // bracketed IPv6 literals keep their colons and lose the brackets.
  const cases: [string, Partial<ConnectionConfig>][] = [
    ['user:p@ss@host', { type: 'ssh', user: 'user', password: 'p@ss', host: 'host', port: 22 }],
    ['[::1]:22', { type: 'ssh', host: '::1', port: 22 }],
    ['user:p@ss@[::1]:22', { type: 'ssh', user: 'user', password: 'p@ss', host: '::1', port: 22 }],
    ['ssh [::1]:2222', { type: 'ssh', host: '::1', port: 2222 }],
  ]
  for (const [input, expected] of cases) {
    it(`parses "${input}"`, () => {
      const cfg = parseQuickConnect(input)
      expect(cfg).toMatchObject(expected)
    })
  }
})
