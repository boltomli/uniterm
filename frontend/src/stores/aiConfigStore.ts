import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { AIModelConfig } from '../types/settings'
import { DEFAULT_SETTINGS } from '../types/settings'
import { LoadAIModels, SaveAIModels } from '../../bindings/github.com/ys-ll/uniterm/app'

// aiConfigStore backs ai.json — the syncable slice of the AI settings
// (model catalog + agent turn limit). settingsStore composes this into
// settings.ai; the current selection is stored separately in localStateStore.
export const useAIConfigStore = defineStore('aiConfig', () => {
  const maxTurns = ref<number>(DEFAULT_SETTINGS.ai.maxTurns)
  // structuredClone, not [...models]: the array copy still shared the model
  // OBJECTS with DEFAULT_SETTINGS, so editing the default model edited the
  // default itself ("reset" stopped resetting).
  const models = ref<AIModelConfig[]>(structuredClone(DEFAULT_SETTINGS.ai.models))
  const loaded = ref(false)

  async function load() {
    try {
      const cfg = await LoadAIModels()
      if (cfg) {
        maxTurns.value = cfg.maxTurns ?? DEFAULT_SETTINGS.ai.maxTurns
        models.value = cfg.models?.length ? cfg.models : structuredClone(DEFAULT_SETTINGS.ai.models)
      }
    } catch {
      // keep defaults
    } finally {
      loaded.value = true
    }
  }

  async function save() {
    try {
      await SaveAIModels({ maxTurns: maxTurns.value, models: models.value })
    } catch {
      // ignore save errors
    }
  }

  return { maxTurns, models, loaded, load, save }
})
