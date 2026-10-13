// RDP redirection (local drives / clipboard) is an explicit per-connection
// opt-in that defaults to OFF. This module is the single source of truth for
// that default: connections saved before the fields existed (or synced from
// an older version) carry no value at all and must resolve to off — the old
// always-on behavior must not come back through the absence of a field.
import type { ConnectionConfig } from '../types/session'

export type RdpRedirection = Pick<ConnectionConfig, 'rdpRedirectClipboard' | 'rdpRedirectDrives'>

// Form defaults for a brand-new RDP connection.
export const RDP_REDIRECTION_DEFAULT: Required<RdpRedirection> = {
  rdpRedirectClipboard: false,
  rdpRedirectDrives: false,
}

// Resolve stored flags for the edit form. Missing fields (legacy connections)
// resolve to false; explicit values are respected verbatim.
export function resolveRdpRedirection(
  config: RdpRedirection | null | undefined
): Required<RdpRedirection> {
  return {
    rdpRedirectClipboard: config?.rdpRedirectClipboard ?? RDP_REDIRECTION_DEFAULT.rdpRedirectClipboard,
    rdpRedirectDrives: config?.rdpRedirectDrives ?? RDP_REDIRECTION_DEFAULT.rdpRedirectDrives,
  }
}
