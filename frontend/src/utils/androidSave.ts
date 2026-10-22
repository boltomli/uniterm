// Android "save to" flow. Android has no save dialog that returns a
// filesystem path — Wails' SaveFileDialog fails there — so exports are
// staged in the app sandbox (GetSaveTempPath + WriteFileBase64) and then
// handed to the system document picker (ACTION_CREATE_DOCUMENT) via the
// wails.saveDocument JS bridge. MainActivity streams the staged file into
// the user-chosen destination and reports back as a "common:saveDocument"
// event. Desktop platforms never take this path; callers fall through to
// the native save dialog.
import { Events } from '@wailsio/runtime'
import { GetSaveTempPath, WriteFileBase64 } from '../../bindings/github.com/ys-ll/uniterm/app'
import { detectPlatformSync } from './platform'

export type AndroidSaveOutcome = 'saved' | 'cancelled' | 'fallback'

interface SaveDocumentEvent {
  saved?: boolean
  cancelled?: boolean
  error?: string
}

// The bridge exists only on Android builds (injected by MainActivity).
function saveBridge(): ((json: string) => void) | null {
  if (detectPlatformSync() !== 'android') return null
  const w = window as unknown as { wails?: { saveDocument?: (json: string) => void } }
  return typeof w.wails?.saveDocument === 'function' ? w.wails.saveDocument.bind(w.wails) : null
}

// True when the Android save flow is usable — callers branch on this before
// picking a destination path.
export function androidSaveAvailable(): boolean {
  return saveBridge() !== null
}

// Stage the export file: returns a sandbox path the caller (or the Go backend)
// writes the payload to. Pair with finishAndroidSave.
export async function stageAndroidSave(fileName: string): Promise<string> {
  return GetSaveTempPath(fileName)
}

// Hand a staged file to the system save picker and stream it to the chosen
// destination. Resolves with the outcome; rejects on bridge/IO errors so the
// caller's existing error handling surfaces them.
export async function finishAndroidSave(tempPath: string, fileName: string, mime: string): Promise<AndroidSaveOutcome> {
  const bridge = saveBridge()
  if (!bridge) throw new Error('save bridge unavailable')
  return await new Promise<AndroidSaveOutcome>((resolve, reject) => {
    // Once auto-disposes after the single event, so no manual unsubscribe.
    Events.Once('common:saveDocument', (ev: { data?: SaveDocumentEvent }) => {
      const d = ev?.data ?? {}
      if (d.saved) resolve('saved')
      else if (d.cancelled) resolve('cancelled')
      else reject(new Error(d.error || 'save failed'))
    })
    bridge(JSON.stringify({ tempPath, name: fileName, mime }))
  })
}

// One-shot save of an in-memory payload through the Android system picker.
// Returns 'fallback' when not on Android (or the bridge is missing) so the
// caller runs its desktop save-dialog path; otherwise the flow was handled
// here ('cancelled' included) and the caller is done.
export async function tryAndroidSave(fileName: string, mime: string, base64: string): Promise<AndroidSaveOutcome> {
  const bridge = saveBridge()
  if (!bridge) return 'fallback'
  const tempPath = await GetSaveTempPath(fileName)
  await WriteFileBase64(tempPath, base64)
  return await finishAndroidSave(tempPath, fileName, mime)
}
