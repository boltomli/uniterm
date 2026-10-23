import { defineStore } from 'pinia'
import { ref } from 'vue'
import { LoadLocalState, SaveLocalState } from '../../bindings/github.com/ys-ll/uniterm/app'
import type { MCPSettings } from '../types/mcp'
import { DEFAULT_MCP_SETTINGS } from '../types/mcp'
import type { SavedTabsSnapshot } from '../types/tabSnapshot'
import type { AILocalState, AIReasoningPreference } from '../types/ai'
import { normalizeReasoning } from '../services/aiReasoning'

// Shape of the backend's LocalState store (bindings are untyped JS, so the
// type is declared here and must stay in sync with backend/store).
interface LocalState {
  ai: AILocalState | null
  sidebarVisible: boolean
  aiSidebarVisible: boolean
  // Persisted sidebar widths in px (0 = old config, keep component default).
  sidebarWidth: number
  aiSidebarWidth: number
  collapsedGroupIds: string[]
  collapsedQuickCommandGroupIds: string[]
  windowX: number
  windowY: number
  windowWidth: number
  windowHeight: number
  windowMaximised: boolean
  backgroundEnabled: boolean
  backgroundImage: string
  backgroundOpacity: number
  backgroundBlur: number
  backgroundFit: string
  systemTitleBar: boolean
  externalEditor: string
  // SFTP file-list columns the user hid via the header context menu. Columns
  // absent from the list stay visible.
  sftpHiddenColumns: string[]
  // MCP server config (external AI agents). Per device — never synced.
  mcp: MCPSettings
  // Open-tabs snapshot for "reopen last session's tabs". Opaque shape
  // (types/tabSnapshot.ts); local-only, never synced.
  lastTabsSnapshot: SavedTabsSnapshot | null
}

const DEFAULT: LocalState = {
  ai: null,
  sidebarVisible: true,
  aiSidebarVisible: true,
  sidebarWidth: 240,
  aiSidebarWidth: 360,
  collapsedGroupIds: [],
  collapsedQuickCommandGroupIds: [],
  windowX: 0,
  windowY: 0,
  windowWidth: 0,
  windowHeight: 0,
  windowMaximised: false,
  backgroundEnabled: false,
  backgroundImage: '',
  backgroundOpacity: 60,
  backgroundBlur: 3,
  backgroundFit: 'cover',
  systemTitleBar: false,
  externalEditor: '',
  sftpHiddenColumns: [],
  mcp: { ...DEFAULT_MCP_SETTINGS },
  lastTabsSnapshot: null,
} as LocalState

export const useLocalStateStore = defineStore('localState', () => {
  const state = ref<LocalState>({ ...DEFAULT })
  const loaded = ref(false)
  let initPromise: Promise<void> | null = null
  let saveQueue: Promise<void> = Promise.resolve()
  let savedAIJson = JSON.stringify(state.value.ai)

  async function init() {
    if (loaded.value) return
    if (!initPromise) {
      initPromise = (async () => {
        try {
          const s = await LoadLocalState()
          state.value = { ...DEFAULT, ...s } as LocalState
          savedAIJson = JSON.stringify(state.value.ai)
          if (state.value.ai) {
            state.value.ai.modelPreferences = Object.fromEntries(
              Object.entries(state.value.ai.modelPreferences || {}).map(([id, preference]) => [id, normalizeReasoning(preference)])
            )
          }
        } catch {
          // keep defaults
        } finally {
          loaded.value = true
        }
      })()
    }
    return initPromise
  }

  // Debounced persistence: slider drags fire update() on every step, so
  // writes coalesce into a single trailing save 300ms after the last patch.
  // The patch itself lands in state synchronously (the UI reacts at once),
  // and the returned promise resolves only once the merged state has
  // actually been persisted. Flushed saves are chained through saveQueue so
  // a later write never overtakes an in-flight one.
  const SAVE_DELAY_MS = 300
  let saveTimer: NodeJS.Timeout | undefined
  let flushWaiters: Array<(ok: boolean) => void> = []

  function flushPendingSave() {
    saveTimer = undefined
    const waiters = flushWaiters
    flushWaiters = []
    // Preserve save order so a slower earlier write cannot undo a new
    // selection; deep-copy so later patches cannot mutate the snapshot.
    const snapshot = JSON.parse(JSON.stringify(state.value)) as LocalState
    const saved = saveQueue.then(async () => {
      await SaveLocalState(snapshot)
      savedAIJson = JSON.stringify(snapshot.ai)
    })
    saveQueue = saved.catch(() => {})
    const ok = saved.then(() => true, () => false)
    for (const resolve of waiters) ok.then(resolve)
  }

  async function update(patch: Partial<LocalState>) {
    if (!loaded.value) await init()
    state.value = { ...state.value, ...patch } as LocalState
    const flushed = new Promise<boolean>((resolve) => flushWaiters.push(resolve))
    clearTimeout(saveTimer)
    saveTimer = setTimeout(flushPendingSave, SAVE_DELAY_MS)
    return flushed
  }

  async function ensureAISelection(modelIds: string[], legacyId = '') {
    await init()
    const previous = state.value.ai
    const requested = previous?.activeModelId ?? legacyId
    const activeModelId = modelIds.includes(requested) ? requested : (modelIds[0] || '')
    const ai = { activeModelId, modelPreferences: previous?.modelPreferences || {} }
    if (JSON.stringify(ai) !== savedAIJson) {
      if (!await update({ ai })) throw new Error('Could not save AI selection')
    }
  }

  async function setActiveAIModel(id: string) {
    await init()
    if (!await update({ ai: { activeModelId: id, modelPreferences: state.value.ai?.modelPreferences || {} } })) throw new Error('Could not save AI selection')
  }

  async function setAIReasoning(modelId: string, preference: AIReasoningPreference) {
    await init()
    const saved = await update({ ai: {
      activeModelId: state.value.ai?.activeModelId || modelId,
      modelPreferences: { ...state.value.ai?.modelPreferences, [modelId]: normalizeReasoning(preference) },
    } })
    if (!saved) throw new Error('Could not save AI reasoning preferences')
  }

  return { state, loaded, init, update, ensureAISelection, setActiveAIModel, setAIReasoning }
})
