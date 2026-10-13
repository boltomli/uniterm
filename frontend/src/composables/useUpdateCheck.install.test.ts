// Regression tests for the update install flow: the asset list must NOT be
// handed to the backend from the webview. The backend derives it from its own
// CheckForUpdate result, so a compromised renderer can't make the updater
// fetch and run an arbitrary URL.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref } from 'vue'

const { downloadUpdateMock, applyUpdateMock, checkForUpdateMock } = vi.hoisted(() => ({
  downloadUpdateMock: vi.fn(),
  applyUpdateMock: vi.fn(),
  checkForUpdateMock: vi.fn(),
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  CheckForUpdate: checkForUpdateMock,
  GetAppInfo: vi.fn(async () => ({ name: 'uniTerm', version: '1.0.0' })),
  DownloadUpdate: downloadUpdateMock,
  ApplyUpdate: applyUpdateMock,
}))

vi.mock('../i18n', () => ({
  useI18n: () => ({ t: (k: string) => k }),
  locale: ref('en-US'),
}))

vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: () => ({
    loaded: true,
    settings: { autoCheckUpdate: true, updateSource: 'auto' },
    save: vi.fn(),
  }),
}))

vi.mock('../services/message', () => ({
  msg: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), copyable: vi.fn() },
}))

vi.mock('element-plus', () => ({ ElMessage: vi.fn() }))

vi.mock('@wailsio/runtime', () => ({ Events: { On: vi.fn(() => () => {}) } }))

vi.mock('../utils/platform', () => ({ isMobilePlatform: () => true }))

import { useUpdateCheck } from './useUpdateCheck'

beforeEach(() => {
  downloadUpdateMock.mockReset().mockResolvedValue(undefined)
  applyUpdateMock.mockReset().mockResolvedValue(undefined)
  checkForUpdateMock.mockReset()
})

describe('startUpdate asset ownership', () => {
  it('calls DownloadUpdate with no arguments, ignoring frontend-supplied assets', async () => {
    const state = useUpdateCheck()
    // A hostile renderer could stuff any URL into updateInfo.assets; the
    // backend must not receive it.
    state.updateInfo = {
      hasUpdate: true,
      current: '1.0.0',
      latest: '1.1.0',
      releaseUrl: 'https://example.invalid/release',
      changelog: '',
      assets: [
        { name: 'evil.tar.gz', url: 'https://evil.example.com/evil.tar.gz', sha256: '', source: 'gitee' },
      ],
    }

    await state.startUpdate()

    expect(downloadUpdateMock).toHaveBeenCalledTimes(1)
    expect(downloadUpdateMock).toHaveBeenCalledWith()
  })

  it('surfaces the backend error when the download is refused', async () => {
    const state = useUpdateCheck()
    state.updateInfo = {
      hasUpdate: true,
      current: '1.0.0',
      latest: '1.1.0',
      releaseUrl: '',
      changelog: '',
      assets: [{ name: 'a.tar.gz', url: 'https://github.com/a.tar.gz', sha256: 'ab', source: 'github' }],
    }
    downloadUpdateMock.mockRejectedValue(new Error('missing sha256 checksum'))

    await state.startUpdate()

    expect(state.updatePhase).toBe('error')
    expect(state.updateError).toContain('missing sha256 checksum')
    expect(applyUpdateMock).not.toHaveBeenCalled()
  })
})
