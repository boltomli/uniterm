import { ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connectionStore'
import type { ConnectionConfig } from '../types/session'

// Shared by the normal status bar and fullscreen toolbar of each desktop client.
export function useRemoteDesktopPreferences(config: () => ConnectionConfig | null) {
  const connectionStore = useConnectionStore()
  const saved = config()?.desktopPreferences
  const scaleViewport = ref(saved?.scaleViewport ?? false)
  const viewOnly = ref(saved?.viewOnly ?? false)
  const showDotCursor = ref(saved?.showDotCursor ?? false)

  watch([scaleViewport, viewOnly, showDotCursor], ([scale, readOnly, cursor]) => {
    const connection = config()
    if (!connection) return
    const desktopPreferences = { scaleViewport: scale, viewOnly: readOnly, showDotCursor: cursor }
    // Update the panel too, so moving a cached session preserves its options.
    connection.desktopPreferences = desktopPreferences
    void connectionStore.update(connection.id, { desktopPreferences })
  }, { flush: 'sync' })

  return { scaleViewport, viewOnly, showDotCursor }
}
