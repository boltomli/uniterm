// RDP redirection (local drives / clipboard) is per-connection and tri-state:
// a field that is absent (connections saved before it existed, or synced from
// an older version) falls back to RDP_REDIRECTION_DEFAULT — clipboard ON, so
// pasting local text into the remote desktop keeps working, and local drives
// OFF, so the remote side never reaches the disks through \\tsclient without
// an explicit opt-in. An explicit value, including an opt-out, is respected
// verbatim. This module is the single source of truth for those defaults; the
// backend mirrors them in buildRdpSettings (backend/session/rdp_session.go).
import type { ConnectionConfig } from '../types/session'

export type RdpRedirection = Pick<ConnectionConfig, 'rdpRedirectClipboard' | 'rdpRedirectDrives'>

// Form defaults for a brand-new RDP connection.
export const RDP_REDIRECTION_DEFAULT: Required<RdpRedirection> = {
  rdpRedirectClipboard: true,
  rdpRedirectDrives: false,
}

// Resolve stored flags for the edit form. Missing fields (legacy connections)
// resolve to the defaults above; explicit values are respected verbatim.
export function resolveRdpRedirection(
  config: RdpRedirection | null | undefined
): Required<RdpRedirection> {
  return {
    rdpRedirectClipboard: config?.rdpRedirectClipboard ?? RDP_REDIRECTION_DEFAULT.rdpRedirectClipboard,
    rdpRedirectDrives: config?.rdpRedirectDrives ?? RDP_REDIRECTION_DEFAULT.rdpRedirectDrives,
  }
}
