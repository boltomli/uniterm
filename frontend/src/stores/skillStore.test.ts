// Regression test for security.md「前端核心」Medium, extended by F-12.6:
// save_skill must ask before doing anything — both when overwriting an
// existing UNLOCKED skill AND when creating a brand-new one. A prompt
// injection could otherwise plant a skill (it joins the system-prompt index
// for every future session) without the user ever seeing a dialog.
//
// The confirms are injected at the store layer so the production dialog
// (ElMessageBox) never loads in tests.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const { mockListSkills, mockSaveSkill, mockCreateSkill } = vi.hoisted(() => ({
  mockListSkills: vi.fn(),
  mockSaveSkill: vi.fn().mockResolvedValue(undefined),
  mockCreateSkill: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  ListSkills: mockListSkills,
  GetSkillBody: vi.fn().mockResolvedValue(''),
  SetSkillEnabled: vi.fn(),
  SetSkillLocked: vi.fn(),
  DeleteSkill: vi.fn(),
  CreateSkill: mockCreateSkill,
  SaveSkill: mockSaveSkill,
  OpenPathInExplorer: vi.fn(),
}))

import { useSkillStore } from './skillStore'
import type { SkillMeta } from '../types/skill'

function skill(name: string, extra: Partial<SkillMeta> = {}): SkillMeta {
  return {
    name,
    description: '',
    enabled: true,
    locked: false,
    ...extra,
  } as SkillMeta
}

describe('saveByAgent overwrite confirmation (security.md FE-Medium)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('prompts before creating a brand-new skill (F-12.6)', async () => {
    mockListSkills.mockResolvedValue([skill('existing')])
    const store = useSkillStore()
    const overwriteConfirm = vi.fn().mockResolvedValue(true)
    const createConfirm = vi.fn().mockResolvedValue(true)
    store.confirmOverwrite = overwriteConfirm
    store.confirmCreate = createConfirm

    const result = await store.saveByAgent('fresh', 'desc', 'body')

    expect(result).toBe('saved')
    expect(createConfirm).toHaveBeenCalledWith('fresh')
    // The create path must not fall through to the overwrite dialog.
    expect(overwriteConfirm).not.toHaveBeenCalled()
    expect(mockCreateSkill).toHaveBeenCalledWith('fresh', 'desc', 'body')
    expect(mockSaveSkill).not.toHaveBeenCalled()
  })

  it('writes nothing when the user declines a creation (F-12.6)', async () => {
    mockListSkills.mockResolvedValue([skill('existing')])
    const store = useSkillStore()
    store.confirmCreate = vi.fn().mockResolvedValue(false)

    const result = await store.saveByAgent('planted-skill', 'desc', 'body')

    expect(result).toBe('declined')
    expect(mockCreateSkill).not.toHaveBeenCalled()
    expect(mockSaveSkill).not.toHaveBeenCalled()
  })

  it('prompts before overwriting an existing unlocked skill', async () => {
    mockListSkills.mockResolvedValue([skill('existing')])
    const store = useSkillStore()
    const confirm = vi.fn().mockResolvedValue(true)
    store.confirmOverwrite = confirm

    const result = await store.saveByAgent('existing', 'desc', 'body')

    expect(result).toBe('saved')
    expect(confirm).toHaveBeenCalledWith('existing')
    expect(mockSaveSkill).toHaveBeenCalledWith('existing', 'desc', 'body')
    expect(mockCreateSkill).not.toHaveBeenCalled()
  })

  it('writes nothing when the user declines the overwrite', async () => {
    mockListSkills.mockResolvedValue([skill('existing')])
    const store = useSkillStore()
    const confirm = vi.fn().mockResolvedValue(false)
    store.confirmOverwrite = confirm

    const result = await store.saveByAgent('existing', 'desc', 'body')

    expect(result).toBe('declined')
    expect(confirm).toHaveBeenCalledWith('existing')
    expect(mockSaveSkill).not.toHaveBeenCalled()
    expect(mockCreateSkill).not.toHaveBeenCalled()
  })

  it('does not prompt for an existing locked skill (the backend rejects the write anyway)', async () => {
    mockListSkills.mockResolvedValue([skill('locked-one', { locked: true })])
    const store = useSkillStore()
    const confirm = vi.fn().mockResolvedValue(true)
    store.confirmOverwrite = confirm

    await store.saveByAgent('locked-one', 'desc', 'body')

    expect(confirm).not.toHaveBeenCalled()
    expect(mockSaveSkill).toHaveBeenCalledWith('locked-one', 'desc', 'body')
  })
})
