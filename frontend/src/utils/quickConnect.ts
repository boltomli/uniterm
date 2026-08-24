import type { ConnectionConfig } from '../types/session'

// Platform detection for Windows-only features (e.g., WSLC)
export const isWindows = /windows/i.test(navigator.userAgent)

const QUICK_PROTOCOLS: Record<string, { type: string; dbType?: string; defaultPort?: number }> = {
  ssh: { type: 'ssh', defaultPort: 22 },
  telnet: { type: 'telnet', defaultPort: 23 },
  mosh: { type: 'mosh', defaultPort: 22 },
  rdp: { type: 'rdp', defaultPort: 3389 },
  vnc: { type: 'vnc', defaultPort: 5900 },
  spice: { type: 'spice' },
  ftp: { type: 'ftp', defaultPort: 21 },
  sftp: { type: 'sftp', defaultPort: 22 },
  smb: { type: 'smb', defaultPort: 445 },
  s3: { type: 's3' },
  webdav: { type: 'webdav' },
  http: { type: 'webdav' },
  https: { type: 'webdav' },
  mysql: { type: 'database', dbType: 'mysql', defaultPort: 3306 },
  postgres: { type: 'database', dbType: 'postgres', defaultPort: 5432 },
  postgresql: { type: 'database', dbType: 'postgres', defaultPort: 5432 },
  redis: { type: 'database', dbType: 'redis', defaultPort: 6379 },
  mongodb: { type: 'database', dbType: 'mongodb', defaultPort: 27017 },
  mongo: { type: 'database', dbType: 'mongodb', defaultPort: 27017 },
  es: { type: 'database', dbType: 'elasticsearch', defaultPort: 9200 },
  elasticsearch: { type: 'database', dbType: 'elasticsearch', defaultPort: 9200 },
  opensearch: { type: 'database', dbType: 'elasticsearch', defaultPort: 9200 },
  oracle: { type: 'database', dbType: 'oracle', defaultPort: 1521 },
  sqlserver: { type: 'database', dbType: 'sqlserver', defaultPort: 1433 },
  rqlite: { type: 'database', dbType: 'rqlite', defaultPort: 4001 },
}

// Helper: parse [user[:password]@]host[:port]
function parseHost(s: string) {
  const m = s.match(/^(?:([^@]+)@)?([^:]+)(?::(\d+))?$/)
  if (!m) return { host: s }
  let user = ''
  let pass = ''
  if (m[1]) {
    const colonIdx = m[1].indexOf(':')
    if (colonIdx >= 0) {
      user = m[1].slice(0, colonIdx)
      pass = m[1].slice(colonIdx + 1)
    } else {
      user = m[1]
    }
  }
  return {
    user,
    password: pass || undefined,
    host: m[2],
    port: m[3] ? parseInt(m[3]) : undefined,
  }
}

export function parseQuickConnect(raw: string): Partial<ConnectionConfig> | null {
  const input = raw.trim()
  if (!input) return null

  const parts = input.split(/\s+/)
  const first = parts[0].toLowerCase()

  // Pattern: [type] [user[:password]@]host[:port]
  if (parts.length >= 2 && QUICK_PROTOCOLS[first]) {
    const cfg = QUICK_PROTOCOLS[first]
    const rest = parts.slice(1).join(' ')
    const h = parseHost(rest)
    const result: any = { type: cfg.type, host: h.host }
    if (cfg.dbType) result.dbType = cfg.dbType
    if (h.user) result.user = h.user
    if (h.password) result.password = h.password
    result.port = h.port || cfg.defaultPort
    return result
  }

  // Patterns: [user[:password]@]host[:port]  or  host[:port]  (default ssh)
  const h = parseHost(input)
  const result: any = { type: 'ssh', host: h.host }
  if (h.user) result.user = h.user
  if (h.password) result.password = h.password
  result.port = h.port || 22
  return result
}

// Look up default port from QUICK_PROTOCOLS by type or dbType
function getDefaultPort(type: string, dbType?: string): number | undefined {
  return QUICK_PROTOCOLS[type]?.defaultPort ?? (dbType ? QUICK_PROTOCOLS[dbType]?.defaultPort : undefined)
}

export function formatConnSubtitle(config: ConnectionConfig, getShellLabel?: (path: string) => string): string {
  let typeLabel = config.type
  if (config.type === 'database') typeLabel = config.dbType || config.type
  else if (config.type === 'container') typeLabel = config.containerRuntime || config.type
  let detail: string
  if (config.type === 's3') {
    detail = config.host
  } else if (config.type === 'local') {
    detail = getShellLabel ? getShellLabel(config.shellPath || '') : 'Local'
  } else {
    const defaultPort = getDefaultPort(config.type, config.dbType)
    const showPort = defaultPort !== config.port && defaultPort !== undefined
    const portStr = showPort ? `:${config.port}` : ''
    detail = config.user ? `${config.user}@${config.host}${portStr}` : `${config.host}${portStr}`
  }
  return `${typeLabel} ${detail}`
}

// Origin (base) connection type for a filter key, ignoring any `:` suffix
// (e.g. `database:mysql` → `database`, `container:docker` → `container`).
export function getTypeBaseType(key: string): string {
  return key.split(':')[0]
}

// Map of base type → top-level filter category, mirroring the two-level type
// layout of the new-connection form (ConnectionForm `categories`).
export const TYPE_CATEGORY: Record<string, string> = {
  ssh: 'terminal', telnet: 'terminal', mosh: 'terminal', local: 'terminal', serial: 'terminal', monitor: 'terminal',
  sftp: 'filetransfer', ftp: 'filetransfer', smb: 'filetransfer', s3: 'filetransfer', webdav: 'filetransfer',
  rdp: 'remote', vnc: 'remote', spice: 'remote', 'x11-desktop': 'remote',
  database: 'database',
  k8s: 'container', container: 'container',
}

// SQL-family database types placed under the "SQL数据库" category; everything
// else with type 'database' (Redis/MongoDB/Elasticsearch) sits under NoSQL.
export const SQL_DB_TYPES = ['mysql', 'postgres', 'oracle', 'sqlserver', 'rqlite']
export function isSqlDbType(dbType?: string): boolean {
  return !!dbType && SQL_DB_TYPES.includes(dbType)
}

// Grouping key used by the type filter for a connection, the same shape as a
// filter value: `database:<dbType>` / `container:<runtime>` / plain type.
export function getConnectionTypeKey(config: ConnectionConfig): string {
  if (config.type === 'database' && config.dbType) return `database:${config.dbType}`
  if (config.type === 'container') return `container:${config.containerRuntime || 'docker'}`
  return config.type
}

// Pretty-print a type filter key that isn't covered by a static label — today
// only container runtimes (`container:docker` → `Docker`). `containerRuntime`
// values are lowercase; capitalize the first letter for the menu/trigger.
export function formatTypeFilterLabel(key: string): string {
  const prefix = 'container:'
  if (key.startsWith(prefix)) {
    const rt = key.slice(prefix.length)
    return rt.charAt(0).toUpperCase() + rt.slice(1)
  }
  return key
}

// Top-level category (key) a filter value belongs to, with a fallback category
// for any unexpected type so it still shows up in the menu. Database types are
// further routed to the sql/nosql sub-categories by dbType.
export function getTypeCategory(key: string): string {
  const base = getTypeBaseType(key)
  if (base === 'database') {
    return isSqlDbType(key.slice('database:'.length)) ? 'sql' : 'nosql'
  }
  return TYPE_CATEGORY[base] || 'other'
}

// Ordered, labelled category keys for the two-level filter menu.
export const TYPE_CATEGORIES: string[] = ['terminal', 'filetransfer', 'remote', 'sql', 'nosql', 'container', 'other']

// Two-level type catalog for the type filter, matching the new-connection form
// (ConnectionForm `categories` + `allSubTypes`) exactly — same category order,
// same subtype order, same labels. `t` is required only for the few names that
// are localized (category titles, local terminal, serial).
// `isWin` controls whether Windows-only options (WSLC) are included.
export function getTypeFilterCatalog(t: (key: string) => string, isWin = false) {
  return [
    {
      key: 'terminal',
      label: t('conn.categoryTerminal'),
      items: [
        { key: 'ssh', label: 'SSH (SFTP)' },
        { key: 'telnet', label: 'Telnet' },
        { key: 'mosh', label: 'Mosh' },
        { key: 'local', label: t('conn.localTerminal') },
        { key: 'serial', label: t('serial.title') },
      ],
    },
    {
      key: 'filetransfer',
      label: t('conn.categoryFileTransfer'),
      items: [
        { key: 'ftp', label: 'FTP' },
        { key: 'smb', label: 'SMB' },
        { key: 's3', label: 'S3' },
        { key: 'webdav', label: 'WebDAV' },
      ],
    },
    {
      key: 'remote',
      label: t('conn.categoryRemote'),
      items: [
        { key: 'rdp', label: 'RDP' },
        { key: 'vnc', label: 'VNC' },
        { key: 'spice', label: 'SPICE' },
        { key: 'x11-desktop', label: 'X11 Desktop' },
      ],
    },
    {
      key: 'sql',
      label: t('db.categorySQL'),
      items: [
        { key: 'database:mysql', label: 'MySQL' },
        { key: 'database:postgres', label: 'PostgreSQL' },
        { key: 'database:oracle', label: 'Oracle' },
        { key: 'database:sqlserver', label: 'SQL Server' },
        { key: 'database:rqlite', label: 'rqlite' },
      ],
    },
    {
      key: 'nosql',
      label: t('db.categoryNoSQL'),
      items: [
        { key: 'database:redis', label: 'Redis' },
        { key: 'database:mongodb', label: 'MongoDB' },
        { key: 'database:elasticsearch', label: 'Elasticsearch' },
      ],
    },
    {
      key: 'container',
      label: t('conn.categoryContainer'),
      items: [
        { key: 'k8s', label: 'Kubernetes' },
        { key: 'container:docker', label: 'Docker' },
        { key: 'container:podman', label: 'Podman' },
        { key: 'container:nerdctl', label: 'nerdctl' },
        ...(isWin ? [{ key: 'container:wslc', label: 'WSLC' }] : []),
      ],
    },
  ]
}
