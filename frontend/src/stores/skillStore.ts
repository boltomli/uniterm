import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { ElMessageBox } from 'element-plus'
import { ListSkills, GetSkillBody, SetSkillEnabled, SetSkillLocked, DeleteSkill, CreateSkill, SaveSkill, OpenPathInExplorer } from '../../bindings/github.com/ys-ll/uniterm/app'
import type { SkillMeta } from '../types/skill'
import { t } from '../i18n'

/** Result of an AI-initiated skill save: declined means the user refused the
 * overwrite prompt, so nothing was written. */
export type SaveByAgentResult = 'saved' | 'declined'

/** Asks the user before an existing skill is overwritten. Injectable so tests
 * (and alternative UIs) can supply their own dialog. */
type OverwriteConfirm = (name: string) => boolean | Promise<boolean>

// Default production dialog. Dismissing it (cancel/Esc) resolves to false so
// an existing skill is never overwritten silently.
async function defaultOverwriteConfirm(name: string): Promise<boolean> {
  try {
    await ElMessageBox.confirm(
      t('settings.skillsOverwriteConfirm', { name }),
      t('settings.skills'),
      {
        type: 'warning',
        confirmButtonText: t('common.confirm'),
        cancelButtonText: t('common.cancel'),
      },
    )
    return true
  } catch {
    return false
  }
}

export const useSkillStore = defineStore('skills', () => {
  const skills = ref<SkillMeta[]>([])
  const loaded = ref(false)

  async function load() {
    if (loaded.value) return
    try {
      skills.value = await ListSkills() || []
    } catch (e) {
      console.error('Failed to load skills:', e)
      skills.value = []
    }
    loaded.value = true
  }

  function reload() {
    loaded.value = false
    return load()
  }

  const enabledSkills = computed(() => skills.value.filter(s => s.enabled))

  async function toggleEnabled(name: string) {
    const s = skills.value.find(x => x.name === name)
    if (!s) return
    s.enabled = !s.enabled
    try {
      await SetSkillEnabled(name, s.enabled)
    } catch (e) {
      console.error('Failed to toggle skill enabled:', e)
    }
  }

  async function toggleLocked(name: string) {
    const s = skills.value.find(x => x.name === name)
    if (!s) return
    s.locked = !s.locked
    try {
      await SetSkillLocked(name, s.locked)
    } catch (e) {
      console.error('Failed to toggle skill lock:', e)
    }
  }

  async function remove(name: string) {
    const s = skills.value.find(x => x.name === name)
    if (!s) return
    if (s.locked) {
      return
    }
    try {
      await DeleteSkill(name)
      skills.value = skills.value.filter(x => x.name !== name)
    } catch (e) {
      console.error('Failed to delete skill:', e)
    }
  }

  async function create(name: string, description: string, body: string) {
    try {
      await CreateSkill(name, description, body)
      await reload()
    } catch (e) {
      console.error('Failed to create skill:', e)
      throw e
    }
  }

  async function save(name: string, description: string, body: string) {
    try {
      await SaveSkill(name, description, body)
      await reload()
    } catch (e) {
      console.error('Failed to save skill:', e)
      throw e
    }
  }

  // AI 侧保存：新名直接新建；同名且未锁定的既有 skill 必须先经用户确认
  // 才覆写（prompt injection 不再能静默种下一个用户以后会执行的 skill），
  // 用户拒绝时一个字节都不落盘。同名已锁定的 skill 由后端拒绝，无需打扰用户。
  const confirmOverwrite = ref<OverwriteConfirm>(defaultOverwriteConfirm)

  async function saveByAgent(
    name: string,
    description: string,
    body: string,
  ): Promise<SaveByAgentResult> {
    await load()
    const existing = skills.value.find(s => s.name === name)
    if (existing && !existing.locked) {
      const approved = await confirmOverwrite.value(name)
      if (!approved) return 'declined'
    }
    if (existing) {
      await SaveSkill(name, description, body)
    } else {
      await CreateSkill(name, description, body)
    }
    await reload()
    return 'saved'
  }

  async function getBody(name: string): Promise<string> {
    try {
      return await GetSkillBody(name)
    } catch (e) {
      console.error('Failed to get skill body:', e)
      return ''
    }
  }

  async function openFolder(path: string): Promise<void> {
    if (!path) return
    try {
      await OpenPathInExplorer(path)
    } catch (e) {
      console.error('Failed to open skill folder:', e)
    }
  }

  return {
    skills, loaded, enabledSkills, confirmOverwrite,
    load, reload,
    toggleEnabled, toggleLocked, remove, create, save, saveByAgent, getBody, openFolder,
  }
})