import { computed, ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connectionStore'
import { DEFAULT_RDP_ZOOM, parseRdpZoom, serializeRdpZoom } from '../utils/rdpZoom'
import type { RdpZoom } from '../utils/rdpZoom'
import type { ConnectionConfig } from '../types/session'

// Shared by the normal status bar and fullscreen toolbar of each desktop
// client (RDP / VNC / SPICE).
export function useRemoteDesktopPreferences(config: () => ConnectionConfig | null) {
  const connectionStore = useConnectionStore()
  const saved = config()?.desktopPreferences
  // rdpZoom is the storage format ('adaptive' or a factor); fall back to the
  // legacy boolean scaleViewport (true → fit) for connections saved before
  // the zoom selector existed.
  const zoom = ref<RdpZoom>(parseRdpZoom(saved?.rdpZoom ?? (saved?.scaleViewport ? 'adaptive' : DEFAULT_RDP_ZOOM)))
  // Fit-mode flag kept for protocols/components that branch on it (noVNC
  // scaleViewport property, canvas fit classes).
  const scaleViewport = computed(() => zoom.value === 'adaptive')
  const viewOnly = ref(saved?.viewOnly ?? false)
  const showDotCursor = ref(saved?.showDotCursor ?? false)

  watch([zoom, viewOnly, showDotCursor], ([z, readOnly, cursor]) => {
    const connection = config()
    if (!connection) return
    const desktopPreferences = {
      scaleViewport: z === 'adaptive',
      rdpZoom: serializeRdpZoom(z),
      viewOnly: readOnly,
      showDotCursor: cursor,
    }
    // Update the panel too, so moving a cached session preserves its options.
    connection.desktopPreferences = desktopPreferences
    void connectionStore.update(connection.id, { desktopPreferences })
  }, { flush: 'sync' })

  return { zoom, scaleViewport, viewOnly, showDotCursor }
}
