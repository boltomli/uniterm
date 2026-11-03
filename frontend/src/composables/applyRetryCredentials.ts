import type { CredentialResult } from '../components/CredentialPrompt.vue'
import type { ConnectionConfig } from '../types/session'
import { useConnectionStore } from '../stores/connectionStore'

// Keep session-only credentials separate from the saved profile, which may
// share the original config object with the panel.
export async function applyRetryCredentials(config: ConnectionConfig, result: CredentialResult): Promise<ConnectionConfig> {
  const updated = {
    ...config,
    user: result.user || config.user,
    password: result.password,
  }
  if (result.action === 'save_and_connect') {
    await useConnectionStore().update(config.id, { user: updated.user, password: updated.password })
  }
  return updated
}
