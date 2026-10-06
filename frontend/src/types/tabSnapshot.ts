// Snapshot shape for "reopen last session's tabs" (issue #937-4). Persisted
// into local_state.json (per machine, never synced). Host references are
// stored by connectionId only — credentials re-resolve from the connection
// store at restore time, so no secret is ever written to the snapshot.

export interface SavedTabEntry {
  // Tab kind as created by tabStore ('terminal' | 'sftp' | 'rdp' | 'vnc' |
  // 'spice' | 'database' | 'mongodb' | 'redis' | 'elasticsearch' | 'monitor'
  // | 'k8s' | 'container' | 'x11-desktop' | 'workspace').
  kind: string
  name: string
  // Host connection to re-resolve at restore time. Empty for self-contained
  // local/wsl panels (rebuilt from type + shellPath instead).
  connectionId?: string
  // Panel type — distinguishes e.g. an sftp-family tab's session kind and
  // carries the self-contained local/wsl rebuild info.
  type?: string
  shellPath?: string
  // Workspace tabs: link to the saved-workspace connection record when the
  // tab was linked to one; otherwise inline members + layout (same shape as
  // the saved-workspace record).
  savedWorkspaceId?: string
  members?: import('./session').SavedWorkspaceMember[]
  layout?: import('./session').SavedWorkspaceLayoutNode | null
}

export interface SavedTabsSnapshot {
  tabs: SavedTabEntry[]
}
