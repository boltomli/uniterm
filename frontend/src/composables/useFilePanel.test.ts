// Regression tests for the security.md finding "remote file names can write
// outside the chosen download folder": a listing name such as
// `..\..\AppData\Roaming\Startup\x.bat` must never be joined onto the chosen
// local download folder - backslashes passed the old `name === '..'` filter
// and the backend's filepath.Join treats them as separators on Windows.

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const { openDirectoryDialogMock, sftpListLocalMock, getMock } = vi.hoisted(() => ({
  openDirectoryDialogMock: vi.fn(),
  sftpListLocalMock: vi.fn(),
  getMock: vi.fn(),
}))

vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

// The composable statically imports every Sftp* binding; stub the whole
// module so the graph loads in the node test environment.
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  SftpCopy: vi.fn(),
  SftpMove: vi.fn(),
  SftpRename: vi.fn(),
  SftpRemove: vi.fn(),
  SftpMakeDir: vi.fn(),
  SftpPutContent: vi.fn(),
  SftpPut: vi.fn(),
  SftpGet: vi.fn(),
  SftpChmod: vi.fn(),
  SftpSymlink: vi.fn(),
  SftpLocalCopy: vi.fn(),
  SftpLocalMove: vi.fn(),
  SftpLocalRename: vi.fn(),
  SftpLocalRemove: vi.fn(),
  SftpLocalMkdir: vi.fn(),
  SftpLocalPutContent: vi.fn(),
  SftpCancelTransfer: vi.fn(),
  SftpPauseTransfer: vi.fn(),
  SftpResumeTransfer: vi.fn(),
  SftpRetryTransfer: vi.fn(),
  SftpDismissTransfer: vi.fn(),
  SftpListLocal: sftpListLocalMock,
  OpenMultipleFilesDialog: vi.fn(async () => []),
  OpenDirectoryDialog: openDirectoryDialogMock,
}))

vi.mock('../services/message', () => ({
  msg: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), copyable: vi.fn() },
}))

// The stores need pinia + window.matchMedia; the composable only reads
// state/bookmark helpers from them at call time.
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: () => ({ addSftpBookmark: vi.fn(), removeSftpBookmark: vi.fn() }),
}))
vi.mock('../stores/localStateStore', () => ({
  // dialogStartDirs (picker start-dir memory) awaits init() and
  // rememberDialogDir() fires update(); the mock must satisfy both so the
  // download path reaches its traversal guard instead of throwing.
  useLocalStateStore: () => ({
    state: { externalEditor: '', sftpDialogDir: '' },
    init: vi.fn(async () => {}),
    update: vi.fn(async () => {}),
  }),
}))

import { t as i18nT } from '../i18n'
import { msg } from '../services/message'
import type { FileItem } from '../components/FileList.vue'
import type { FilePanelOptions } from './useFilePanel'
import {
  createSendToOther, isSafeDownloadName, useConflictDialog, useFileDialogs, useFilePanel,
} from './useFilePanel'

function item(name: string, isDir = false): FileItem {
  return { name, size: 0, modTime: '', mode: '', isDir, isHidden: false, owner: '', group: '' }
}

function makeOps() {
  return {
    copy: vi.fn(),
    move: vi.fn(),
    rename: vi.fn(),
    remove: vi.fn(),
    makeDir: vi.fn(),
    putContent: vi.fn(),
    put: vi.fn(),
    get: getMock,
    chmod: vi.fn(),
  }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('isSafeDownloadName', () => {
  it.each(['', '.', '..', 'a/b', 'a\\b', '/abs', 'C:\\x', 'C:x', 'a\u0000b'])(
    'rejects %j',
    (name) => {
      expect(isSafeDownloadName(name)).toBe(false)
    },
  )

  it.each(['file.txt', '..hidden', 'a b.txt', '中文名.txt'])(
    'accepts %j',
    (name) => {
      expect(isSafeDownloadName(name)).toBe(true)
    },
  )
})

describe('createSendToOther (toLocal)', () => {
  it('skips traversal names with a warning and downloads only the safe file', async () => {
    const ops = makeOps()
    const handler = createSendToOther({
      sid: () => 's1',
      direction: 'toLocal',
      sourceCwd: { value: '/remote' },
      targetCwd: { value: '/local/dl' },
      targetFiles: { value: [] },
      conflicts: useConflictDialog(),
      ops,
    })

    await handler([item('..\\..\\AppData\\x.bat'), item('ok.txt')])

    expect(msg.warning).toHaveBeenCalledTimes(1)
    expect(msg.warning).toHaveBeenCalledWith(i18nT('sftp.dialog.newFileInvalid'))
    // The malicious name must never reach the download op; the safe file goes
    // to the chosen folder under its own name.
    expect(ops.get).toHaveBeenCalledTimes(1)
    expect(ops.get).toHaveBeenCalledWith('s1', '/remote/ok.txt', '/local/dl/ok.txt', false)
  })
})

describe('useFilePanel().onDownloadTo', () => {
  it('refuses to write a traversal name outside the chosen folder', async () => {
    openDirectoryDialogMock.mockResolvedValue('/tmp/dl')
    sftpListLocalMock.mockResolvedValue({ files: [] })
    const ops = makeOps()
    const opts: FilePanelOptions = {
      sid: () => 's1',
      cwd: ref('/remote'),
      files: ref<FileItem[]>([]),
      refresh: vi.fn(),
      ops,
      conflicts: useConflictDialog(),
      dialogs: useFileDialogs(),
      transferTasks: () => [],
      bookmarkMode: 'remote',
    }
    const panel = useFilePanel(opts)

    await panel.onDownloadTo([
      item('..\\..\\AppData\\Roaming\\Startup\\x.bat'),
      item('ok.txt'),
    ])

    expect(msg.warning).toHaveBeenCalledTimes(1)
    expect(msg.warning).toHaveBeenCalledWith(i18nT('sftp.dialog.newFileInvalid'))
    expect(ops.get).toHaveBeenCalledTimes(1)
    expect(ops.get).toHaveBeenCalledWith('s1', '/remote/ok.txt', '/tmp/dl/ok.txt', false)
  })
})
