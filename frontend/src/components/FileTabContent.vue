<template>
  <div class="sftp-tab-content">
    <div class="panes-area">
      <div
        class="local-pane"
        @dragover.prevent="onDragOver"
        @dragenter.prevent="onDragEnter('local')"
        @dragleave="onDragLeave('local')"
        @drop.capture="onDropLocal"
      >
        <div v-if="dragOverLocal" class="drop-overlay">
          <span>{{ t('sftp.dropHere') }}</span>
        </div>
        <FileList
          mode="local"
          breadcrumb-mode="local"
          :breadcrumb-path="localCwd"
          :breadcrumb-drives="localDrives"
          :breadcrumb-saved-paths="settingsStore.sftpBookmarks.localPaths"
          :files="localFiles"
          :loading="loadingLocal"
          :paste-loading="pasteLoadingLocal"
          :cut-item-names="localCutItemNames"
          :clipboard-count="localClipboardCount"
          :clipboard-mode="localClipboard?.mode"
          @navigate="onLocalNavigate"
          @send-to-other="onSendToRemote"
          @rename="(item: FileItem) => { dialogMode = 'local'; onRename(item) }"
          @delete="(items: FileItem[]) => { dialogMode = 'local'; onDelete(items) }"
          @refresh="onRefreshLocal"
          @mkdir="() => { dialogMode = 'local'; onMkdir() }"
          @edit="onLocalEditFile"
          @edit-external="onLocalEditExternal"
          @new-file="onLocalNewFile"
          @copy-to-clipboard="onLocalCopyToClipboard"
          @cut-to-clipboard="onLocalCutToClipboard"
          @paste="onLocalPaste"
          @cancel-paste="onLocalCancelPaste"
          @clear-clipboard="onLocalClearClipboard"
          @open="onLocalEditFile"
          @cancel-load="onCancelLoadLocal"
          @save-bookmark="onSaveBookmark('local', $event)"
          @remove-bookmark="onRemoveBookmark('local', $event)"
        />
      </div>
      <div
        class="remote-pane"
        :id="remoteDropId"
        data-file-drop-target
        @dragover.prevent="onDragOver"
        @dragenter.prevent="onDragEnter('remote')"
        @dragleave="onDragLeave('remote')"
        @drop.capture="onDropRemote"
      >
        <div v-if="dragOverRemote" class="drop-overlay">
          <span>{{ t('sftp.dropHere') }}</span>
        </div>
        <FileList
          mode="remote"
          breadcrumb-mode="remote"
          :breadcrumb-path="cwd"
          :breadcrumb-saved-paths="settingsStore.sftpBookmarks.remotePaths"
          :files="remoteFiles"
          :loading="loadingRemote"
          :paste-loading="pasteLoadingRemote"
          :cut-item-names="cutItemNames"
          :clipboard-count="clipboardCount"
          :clipboard-mode="clipboard?.mode"
          @navigate="onRemoteNavigate"
          @send-to-other="onSendToLocal"
          @rename="(item: FileItem) => { dialogMode = 'remote'; onRename(item) }"
          @delete="(items: FileItem[]) => { dialogMode = 'remote'; onDelete(items) }"
          @refresh="onRefreshRemote"
          @mkdir="() => { dialogMode = 'remote'; onMkdir() }"
          @chmod="(item: FileItem) => { dialogMode = 'remote'; onChmod(item) }"
          @upload="onUpload"
          @download-to="onDownloadTo"
          @edit="onEditFile"
          @edit-external="onEditExternal"
          @new-file="onNewFile"
          @copy-to-clipboard="onCopyToClipboard"
          @cut-to-clipboard="onCutToClipboard"
          @paste="onPaste"
          @clear-clipboard="onClearClipboard"
          @cancel-paste="onCancelPaste"
          @open="onEditFile"
          @cancel-load="onCancelLoadRemote"
          @save-bookmark="onSaveBookmark('remote', $event)"
          @remove-bookmark="onRemoveBookmark('remote', $event)"
        />
      </div>
    </div>
    <TransferPanel
      v-model:height="transferHeight"
      resizable
      :tasks="transferTasks"
      @cancel="onCancelTransfer"
      @pause="onPauseTransfer"
      @resume="onResumeTransfer"
      @clearCompleted="clearFinishedTransfers"
    />

    <!-- Custom Dialog (shared) -->
    <FileGenericDialog
      v-model:visible="dialogVisible"
      :title="dialogTitle"
      :type="dialogType === 'delete' ? 'message' : 'input'"
      :input-value="dialogInput"
      :placeholder="dialogPlaceholder"
      :message="dialogMessage"
      @update:inputValue="(v: string) => dialogInput = v"
      @confirm="onDialogConfirm"
      @cancel="dialogVisible = false"
      @closed="onDialogClosed"
    />

    <!-- Change permission dialog (shared with the file sidebar) -->
    <FileChmodDialog
      v-model:visible="chmodVisible"
      :name="chmodItem?.name"
      :owner="chmodItem?.owner"
      :group="chmodItem?.group"
      :mode="chmodItem?.mode"
      @confirm="onChmodConfirm"
    />

    <!-- Editor Dialog (shared CodeMirror editor) -->
    <FileEditorDialog
      ref="fileEditorRef"
      v-model:visible="editorVisible"
      :session-id="panel?.sessionId"
      :mode="editorMode"
      @saved="onEditorSaved"
    />

    <!-- Conflict Dialog (shared) -->
    <FileConflictDialog
      v-model:visible="conflictVisible"
      :files="conflictFiles"
      @resolve="onConflictResolve"
    />

    <!-- New File Dialog (shared) -->
    <FileGenericDialog
      v-model:visible="newFileVisible"
      :title="t('sftp.dialog.newFileTitle')"
      type="input"
      :input-value="newFileName"
      :placeholder="t('sftp.dialog.newFilePrompt')"
      :error="newFileError"
      :loading="newFileCreating"
      @update:inputValue="(v: string) => newFileName = v"
      @confirm="onNewFileCreate"
      @cancel="newFileVisible = false"
      @closed="newFileError = ''"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, onActivated, onDeactivated, watch } from 'vue'

import { msg } from '../services/message'
import { usePanelStore } from '../stores/panelStore'
import { useSettingsStore } from '../stores/settingsStore'
import { useLocalStateStore } from '../stores/localStateStore'
import { useI18n } from '../i18n'
import {
  SftpListRemote, SftpListLocal, SftpListLocalDrives,
  SftpChangeRemoteDir, SftpChangeLocalDir,
  SftpMakeDir, SftpRemove, SftpRename, SftpChmod,
  SftpLocalRemove, SftpLocalRename, SftpLocalMkdir,
  SftpLocalPutContent, SftpLocalCopy, SftpLocalMove,
  SftpGet, SftpPut, SftpPutContent, SftpCopy, SftpMove,
  SftpOpenExternalEditor, OpenExternalEditorLocal,
  SftpCancelTransfer, SftpPauseTransfer, SftpResumeTransfer, ListSessions,
  OpenMultipleFilesDialog, OpenDirectoryDialog,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import FileList from './FileList.vue'
import TransferPanel from './TransferPanel.vue'
import FileChmodDialog from './FileChmodDialog.vue'
import FileEditorDialog from './FileEditorDialog.vue'
import FileGenericDialog from './FileGenericDialog.vue'
import FileConflictDialog from './FileConflictDialog.vue'
import type { FileItem } from './FileList.vue'
import { Events } from '@wailsio/runtime'
import type { TransferTaskUI } from '../stores/panelStore'
import { useTransferTaskEvents } from '../composables/useTransferTasks'

const props = defineProps<{
  panelId: string
}>()

const panelStore = usePanelStore()
const settingsStore = useSettingsStore()
const localStateStore = useLocalStateStore()
const transferTasks = panelStore.getTransferTasks(props.panelId)
const transferHeight = ref(130)
const transferEvents = useTransferTaskEvents(
  () => transferTasks,
  () => panel.value?.sessionId,
  (status, type) => {
    if (status === 'done') {
      if (type === 'download') onRefreshLocal()
      else onRefreshRemote()
    }
  },
)
const { t } = useI18n()
const panel = computed(() => panelStore.getPanel(props.panelId))

const localCwd = ref('/')
const cwd = ref('/')
const localFiles = ref<FileItem[]>([])
const remoteFiles = ref<FileItem[]>([])
const localDrives = ref<string[]>([])
const loadingLocal = ref(false)
const loadingRemote = ref(false)
let loadVersionLocal = 0
let loadVersionRemote = 0
const pasteLoadingLocal = ref(false)
const pasteLoadingRemote = ref(false)
const dragOverLocal = ref(false)
const dragOverRemote = ref(false)
const dragSource = ref<'local' | 'remote' | null>(null)
let dragEnterLocalCount = 0
let dragEnterRemoteCount = 0
let dragDroppedInternally = false
const dialogMode = ref<'local' | 'remote'>('remote')
let nativeDropUnsub: (() => void) | null = null

// Unique id on this tab's remote-pane drop zone. Wails v3 forwards the id of the
// element a file was dropped on via common:WindowFilesDropped, and the file
// sidebar also acts as a drop target, so only keep drops that landed here.
const remoteDropId = (crypto.randomUUID?.() ||
  `sftp-remote-drop-${Date.now()}-${Math.random().toString(36).slice(2)}`)

function joinPath(base: string, name: string): string {
  if (base.endsWith('/') || base.endsWith('\\')) return base + name
  return base + '/' + name
}


// Dialog state
const dialogVisible = ref(false)
const dialogType = ref<'rename' | 'mkdir' | 'delete'>('rename')
const dialogTitle = ref('')
const dialogMessage = ref('')
const dialogInput = ref('')
const dialogPlaceholder = ref('')
const dialogItem = ref<FileItem | null>(null)
const dialogItems = ref<FileItem[]>([])

// Clipboard state
interface Clipboard {
  items: string[]
  sourceDir: string
  mode: 'copy' | 'cut'
}
const clipboard = ref<Clipboard | null>(null)
const localClipboard = ref<Clipboard | null>(null)
const cutItemNames = computed(() =>
  clipboard.value?.mode === 'cut' ? clipboard.value.items : []
)
const localCutItemNames = computed(() =>
  localClipboard.value?.mode === 'cut' ? localClipboard.value.items : []
)
const clipboardCount = computed(() => clipboard.value?.items.length ?? 0)
const localClipboardCount = computed(() => localClipboard.value?.items.length ?? 0)

// Editor dialog (shared FileEditorDialog with CodeMirror)
const editorVisible = ref(false)
const editorMode = ref<'local' | 'remote'>('remote')
const fileEditorRef = ref<{ open: (path: string, title: string, mode?: 'remote' | 'local') => Promise<void> } | null>(null)

function onEditorSaved() {
  if (editorMode.value === 'local') onRefreshLocal()
  else onRefreshRemote()
}

// New File dialog state
const newFileVisible = ref(false)
const newFileName = ref('newfile.txt')
const newFileMode = ref<'local' | 'remote'>('remote')
const newFileError = ref('')
const newFileCreating = ref(false)

// Conflict dialog state
const conflictVisible = ref(false)
const conflictFiles = ref<string[]>([])
const conflictResolve = ref<((action: 'overwrite' | 'rename' | 'cancel') => void) | null>(null)

// Change-permission dialog state (rendered by the shared FileChmodDialog).
const chmodVisible = ref(false)
const chmodItem = ref<FileItem | null>(null)

let unsubscribe: (() => void) | null = null
let unsubscribeStatus: (() => void) | null = null
let unsubscribeExt: (() => void) | null = null
let initialNavDone = false

onMounted(async () => {
  unsubscribeStatus =Events.On('session:status', (ev) => { const payload: { id: string; status: string } = ev.data; 
    if (payload.id === panel.value?.sessionId) {
      if (payload.status === 'connected') {
        onRefreshLocal()
        onRefreshRemote().then(() => doInitialAutoNav())
      } else if (payload.status === 'error') {
        msg.error(t('sftp.connectError'))
      }
    }
   })

  unsubscribe =Events.On('session:data', (ev) => { const payload: { id: string; data: string } = ev.data;
    if (payload.id !== panel.value?.sessionId) return
    // Connection failed messages from backend (SFTP/FTP async connect errors)
    const connMatch = payload.data.match(/\[Connection failed: ([^\]]+)\]/)
    if (connMatch) {
      msg.error(connMatch[1])
      return
    }
  })

  // Transfer-task bookkeeping is shared with the file sidebar.
  transferEvents.bind()

  // External-editor status events from the backend (started / uploaded / closed)
  unsubscribeExt = Events.On('sftp:extedit', (ev) => {
    const payload = ev?.data as { sessionId?: string; path?: string; status?: string }
    if (!payload?.sessionId || payload.sessionId !== panel.value?.sessionId) return
    if (payload.status === 'uploaded') {
      msg.success(t('sftp.editExternalUploaded'))
      onRefreshRemote()
    } else if (payload.status === 'closed') {
      msg.success(t('sftp.editExternalClosed'))
    }
  })

  // Proactively check if session is already connected (race: event may have fired
  // before the listener registered). The initial load is resumed from the
  // sessionId watch when the panel binds its session id (S3 connects so fast that
  // 'connected' can fire before that binding).
  await probeConnectAndLoad()
})

watch(() => panel.value?.sessionId, async (newId, oldId) => {
  if (newId && !oldId) {
    fetchLocalDrives()
    await probeConnectAndLoad()
  }
}, { immediate: true })

// A fast-connecting session (e.g. S3) can emit session:status 'connected' before
// this panel binds its sessionId, so the connected-event handler and a mount-time
// probe that runs while sid is still undefined both miss it. Once we know the id,
// check whether the session is already up and, if so, run the initial load.
let probeRan = false
async function probeConnectAndLoad() {
  if (probeRan || initialNavDone) return
  const sid = panel.value?.sessionId
  if (!sid) return
  try {
    const sessions = await ListSessions()
    const sess = sessions.find(s => s.id === sid)
    if (sess && sess.status === 'connected') {
      probeRan = true
      onRefreshLocal()
      onRefreshRemote().then(() => doInitialAutoNav())
    }
  } catch { /* ignore */ }
}

onUnmounted(() => {
  unsubscribe?.()
  unsubscribeStatus?.()
  unsubscribeExt?.()
  nativeDropUnsub?.()
  nativeDropUnsub = null
})

// With KeepAlive, only the active instance should listen for global document
// drag/drop events to avoid cached instances picking up drops from other tabs.
onActivated(() => {
  document.addEventListener('dragstart', onDragStart)
  document.addEventListener('dragend', clearDragState)
  // OS file drops (resource manager) are delivered by Wails via this event, routed
  // to the drop zone whose id matches, then uploaded by absolute local path.
  nativeDropUnsub?.()
  nativeDropUnsub = Events.On('common:WindowFilesDropped', onNativeFileDrop)
})

onDeactivated(() => {
  document.removeEventListener('dragstart', onDragStart)
  document.removeEventListener('dragend', clearDragState)
  nativeDropUnsub?.()
  nativeDropUnsub = null
})

async function fetchLocalDrives() {
  const sid = panel.value?.sessionId
  if (!sid) return
  try {
    const drives = await SftpListLocalDrives(sid)
    localDrives.value = drives.map(d => d.name)
  } catch {}
}

function onCancelLoadLocal() {
  loadVersionLocal++
  loadingLocal.value = false
}

function onCancelLoadRemote() {
  loadVersionRemote++
  loadingRemote.value = false
}

async function onRefreshLocal() {
  const sid = panel.value?.sessionId
  if (!sid) return
  const version = ++loadVersionLocal
  loadingLocal.value = true
  try {
    const result = await SftpListLocal(sid, '')
    if (version !== loadVersionLocal) return
    localFiles.value = result.files
    localCwd.value = result.dir
    if (/^[A-Za-z]:\\$/.test(result.dir)) {
      try {
        const drives = await SftpListLocalDrives(sid)
        if (version !== loadVersionLocal) return
        localDrives.value = drives.map(d => d.name)
      } catch {}
    }
  } catch (e: any) {
    if (version !== loadVersionLocal) return
    msg.error(e?.toString() || 'Failed to list local files')
  } finally {
    if (version === loadVersionLocal) loadingLocal.value = false
  }
}

async function onRefreshRemote() {
  const sid = panel.value?.sessionId
  if (!sid) return
  const version = ++loadVersionRemote
  loadingRemote.value = true
  try {
    const result = await SftpListRemote(sid, '')
    if (version !== loadVersionRemote) return
    remoteFiles.value = result.files
    cwd.value = result.dir
  } catch (e: any) {
    if (version !== loadVersionRemote) return
    msg.error(e?.toString() || 'Failed to list remote files')
  } finally {
    if (version === loadVersionRemote) loadingRemote.value = false
  }
}

// Auto-navigate into the configured share/bucket on initial load.
async function doInitialAutoNav() {
  if (initialNavDone) return
  initialNavDone = true
  const cfg = panel.value?.config
  if (!cfg) return
  let target = ''
  if (cfg.type === 'smb' && cfg.smbShare) {
    target = '/' + cfg.smbShare
  } else if (cfg.type === 's3' && cfg.s3Bucket) {
    target = '/' + cfg.s3Bucket
  }
  if (target) {
    await onRemoteNavigate(target)
  }
}

async function onLocalNavigate(path: string) {
  const sid = panel.value?.sessionId
  if (!sid) return
  let fullPath: string
  if (path === '..') {
    const parts = localCwd.value.replace(/\\/g, '/').split('/').filter(Boolean)
    parts.pop()
    if (parts.length === 0) {
      fullPath = localCwd.value
    } else if (/^[A-Za-z]:$/.test(parts[0])) {
      fullPath = parts[0] + '\\' + parts.slice(1).join('\\')
    } else {
      fullPath = '/' + parts.join('/')
    }
  } else if (!path.startsWith('/') && !/^[A-Za-z]:/.test(path)) {
    fullPath = joinPath(localCwd.value, path)
  } else {
    fullPath = path
  }
  const version = ++loadVersionLocal
  loadingLocal.value = true
  try {
    const result = await SftpChangeLocalDir(sid, fullPath)
    if (version !== loadVersionLocal) return
    localFiles.value = result.files
    localCwd.value = result.dir
    if (/^[A-Za-z]:\\$/.test(result.dir)) {
      try {
        const drives = await SftpListLocalDrives(sid)
        if (version !== loadVersionLocal) return
        localDrives.value = drives.map(d => d.name)
      } catch {}
    }
  } catch (e: any) {
    if (version !== loadVersionLocal) return
    msg.error(e?.toString() || 'Failed to navigate')
  } finally {
    if (version === loadVersionLocal) loadingLocal.value = false
  }
}

async function onRemoteNavigate(path: string) {
  const sid = panel.value?.sessionId
  if (!sid) return
  let fullPath: string
  if (path === '..') {
    fullPath = cwd.value.split('/').filter(Boolean).slice(0, -1).join('/')
    fullPath = '/' + fullPath
  } else if (!path.startsWith('/')) {
    fullPath = joinPath(cwd.value, path)
  } else {
    fullPath = path
  }
  const version = ++loadVersionRemote
  loadingRemote.value = true
  try {
    const result = await SftpChangeRemoteDir(sid, fullPath)
    if (version !== loadVersionRemote) return
    remoteFiles.value = result.files
    cwd.value = result.dir
  } catch (e: any) {
    if (version !== loadVersionRemote) return
    msg.error(e?.toString() || 'Failed to navigate')
  } finally {
    if (version === loadVersionRemote) loadingRemote.value = false
  }
}

function onSaveBookmark(mode: 'local' | 'remote', path: string) {
  settingsStore.addSftpBookmark(mode, path)
}

function onRemoveBookmark(mode: 'local' | 'remote', path: string) {
  settingsStore.removeSftpBookmark(mode, path)
}

function clearFinishedTransfers() {
  const tasks = transferTasks
  for (let i = tasks.length - 1; i >= 0; i--) {
    const st = tasks[i].status
    if (st === 'done' || st === 'error' || st === 'cancelled') tasks.splice(i, 1)
  }
}

async function onCancelTransfer(taskId: string) {
  const sid = panel.value?.sessionId
  if (!sid) return
  try {
    await SftpCancelTransfer(sid, taskId)
  } catch (e) {
    console.error('cancel transfer:', e)
  }
}

async function onPauseTransfer(taskId: string) {
  const sid = panel.value?.sessionId
  if (!sid) return
  try {
    await SftpPauseTransfer(sid, taskId)
  } catch (e) {
    console.error('pause transfer:', e)
  }
}

async function onResumeTransfer(taskId: string) {
  const sid = panel.value?.sessionId
  if (!sid) return
  try {
    await SftpResumeTransfer(sid, taskId)
  } catch (e) {
    console.error('resume transfer:', e)
  }
}

async function onSendToRemote(items: FileItem[]) {
  const sid = panel.value?.sessionId
  if (!sid) return

  const fileNames = items.filter(i => i.name !== '..').map(i => i.name)
  const action = await checkRemoteConflicts(fileNames)
  if (action === 'cancel') return

  const existingNames = remoteFiles.value.map(f => f.name)
  for (const item of items) {
    if (item.name === '..') continue
    let resolvedName = item.name
    if (action === 'rename' && existingNames.includes(item.name)) {
      resolvedName = autoRename(item.name, existingNames)
    }
    existingNames.push(resolvedName)
    const localPath = joinPath(localCwd.value, item.name)
    const remotePath = cwd.value + '/' + resolvedName
    SftpPut(sid, localPath, remotePath, item.isDir)
  }
}

async function onSendToLocal(items: FileItem[]) {
  const sid = panel.value?.sessionId
  if (!sid) return

  const fileNames = items.filter(i => i.name !== '..').map(i => i.name)
  const action = await checkLocalConflicts(fileNames)
  if (action === 'cancel') return

  const existingNames = localFiles.value.map(f => f.name)
  for (const item of items) {
    if (item.name === '..') continue
    let resolvedName = item.name
    if (action === 'rename' && existingNames.includes(item.name)) {
      resolvedName = autoRename(item.name, existingNames)
    }
    existingNames.push(resolvedName)
    const remotePath = joinPath(cwd.value, item.name)
    const localPath = joinPath(localCwd.value, resolvedName).replace(/\\/g, '/')
    SftpGet(sid, remotePath, localPath, item.isDir)
  }
}

async function onUpload() {
  const sid = panel.value?.sessionId
  if (!sid) return
  try {
    const files = await OpenMultipleFilesDialog()
    if (!files || files.length === 0) return

    const fileNames = files.map(fp => fp.replace(/\\/g, '/').split('/').pop() || 'upload')
    const action = await checkRemoteConflicts(fileNames)
    if (action === 'cancel') return

    const existingNames = remoteFiles.value.map(f => f.name)
    for (const fp of files) {
      let name = fp.replace(/\\/g, '/').split('/').pop() || 'upload'
      if (action === 'rename' && existingNames.includes(name)) {
        name = autoRename(name, existingNames)
      }
      existingNames.push(name)
      SftpPut(sid, fp, cwd.value + '/' + name, false)
    }
  } catch (e) {
    console.error('upload:', e)
  }
}

async function onDownloadTo(items: FileItem[]) {
  const sid = panel.value?.sessionId
  if (!sid) return
  try {
    const dir = await OpenDirectoryDialog()
    if (!dir) return

    const fileNames = items.filter(i => i.name !== '..').map(i => i.name)
    // Conflict check against the selected directory (may differ from current localCwd).
    let targetNames: string[] = []
    try {
      const result = await SftpListLocal(sid, dir)
      targetNames = result.files.map(f => f.name)
    } catch { /* if listing fails, proceed without conflict prompting */ }
    const conflicts = fileNames.filter(n => targetNames.includes(n))
    let action: 'overwrite' | 'rename' | 'cancel' = 'overwrite'
    if (conflicts.length > 0) {
      action = await showConflictDialog(conflicts)
      if (action === 'cancel') return
    }
    const existingNames = [...targetNames]
    for (const item of items) {
      if (item.name === '..') continue
      let resolvedName = item.name
      if (action === 'rename' && existingNames.includes(item.name)) {
        resolvedName = autoRename(item.name, existingNames)
      }
      existingNames.push(resolvedName)
      const remotePath = joinPath(cwd.value, item.name)
      const localPath = (dir + '/' + resolvedName).replace(/\\/g, '/')
      SftpGet(sid, remotePath, localPath, item.isDir)
    }
  } catch (e) {
    console.error('downloadTo:', e)
  }
}

// --- Clipboard handlers ---

function onCopyToClipboard(items: FileItem[]) {
  clipboard.value = {
    items: items.map(i => i.name),
    sourceDir: cwd.value,
    mode: 'copy'
  }
  msg.success(t('sftp.copy'))
}

function onCutToClipboard(items: FileItem[]) {
  clipboard.value = {
    items: items.map(i => i.name),
    sourceDir: cwd.value,
    mode: 'cut'
  }
  msg.success(t('sftp.cut'))
}

function onClearClipboard() {
  clipboard.value = null
}

function onLocalCopyToClipboard(items: FileItem[]) {
  localClipboard.value = {
    items: items.map(i => i.name),
    sourceDir: localCwd.value,
    mode: 'copy'
  }
  msg.success(t('sftp.copy'))
}

function onLocalCutToClipboard(items: FileItem[]) {
  localClipboard.value = {
    items: items.map(i => i.name),
    sourceDir: localCwd.value,
    mode: 'cut'
  }
  msg.success(t('sftp.cut'))
}

function onLocalClearClipboard() {
  localClipboard.value = null
}

function autoRename(targetName: string, existingNames: string[]): string {
  if (!existingNames.includes(targetName)) return targetName
  const dotIdx = targetName.lastIndexOf('.')
  const base = dotIdx > 0 ? targetName.slice(0, dotIdx) : targetName
  const ext = dotIdx > 0 ? targetName.slice(dotIdx) : ''
  let n = 1
  let candidate: string
  do {
    candidate = `${base} (${n})${ext}`
    n++
  } while (existingNames.includes(candidate))
  return candidate
}

function isPathInside(child: string, parent: string): boolean {
  const c = child.endsWith('/') ? child : child + '/'
  const p = parent.endsWith('/') ? parent : parent + '/'
  return c.startsWith(p)
}

function showConflictDialog(conflicts: string[]): Promise<'overwrite' | 'rename' | 'cancel'> {
  return new Promise((resolve) => {
    conflictFiles.value = conflicts
    conflictResolve.value = resolve
    conflictVisible.value = true
  })
}

async function checkRemoteConflicts(fileNames: string[]): Promise<'overwrite' | 'rename' | 'cancel'> {
  const existingNames = remoteFiles.value.map(f => f.name)
  const conflicts = fileNames.filter(n => existingNames.includes(n))
  if (conflicts.length === 0) return 'overwrite'
  return showConflictDialog(conflicts)
}

async function checkLocalConflicts(fileNames: string[]): Promise<'overwrite' | 'rename' | 'cancel'> {
  const existingNames = localFiles.value.map(f => f.name)
  const conflicts = fileNames.filter(n => existingNames.includes(n))
  if (conflicts.length === 0) return 'overwrite'
  return showConflictDialog(conflicts)
}

function onConflictResolve(action: 'overwrite' | 'rename' | 'cancel') {
  conflictVisible.value = false
  if (conflictResolve.value) {
    conflictResolve.value(action)
    conflictResolve.value = null
  }
}

let pasteCancelled = false

function onCancelPaste() {
  pasteCancelled = true
  pasteLoadingRemote.value = false
}

async function onPaste() {
  const sid = panel.value?.sessionId
  if (!sid || !clipboard.value) return
  const { items, sourceDir, mode } = clipboard.value
  pasteCancelled = false
  pasteLoadingRemote.value = true
  const targetDir = cwd.value

  // Cut to same directory: error immediately, no conflict dialog
  if (mode === 'cut' && sourceDir === targetDir) {
    msg.warning(t('sftp.paste.cutSameDir'))
    pasteLoadingRemote.value = false
    return
  }

  const existingNames = remoteFiles.value.map(f => f.name)

  // Check for name conflicts
  const conflicts = items.filter(name => existingNames.includes(name))
  let resolveAction: 'overwrite' | 'rename' | 'cancel' = 'rename'
  if (conflicts.length > 0) {
    resolveAction = await showConflictDialog(conflicts)
    if (resolveAction === 'cancel') { pasteLoadingRemote.value = false; return }
  }

  let success = 0
  const failed: string[] = []

  for (const name of items) {
    if (pasteCancelled) break
    const source = joinPath(sourceDir, name)
    const target = joinPath(targetDir, name)
    // Same path: copy mode force auto-rename below
    if (source === target) {
      // copy mode (cut already blocked at top level)
    } else if (isPathInside(target, source)) {
    // Circular check (only when source !== target)
      msg.warning(t('sftp.paste.circularWarning'))
      continue
    }
    const forceRename = source === target && mode === 'copy'
    let resolvedName = name
    if (forceRename || (resolveAction === 'rename' && existingNames.includes(name))) {
      resolvedName = autoRename(name, existingNames)
    }
    const resolvedTarget = joinPath(targetDir, resolvedName)
    existingNames.push(resolvedName)
    try {
      if (mode === 'copy') {
        await SftpCopy(sid, source, resolvedTarget)
      } else {
        await SftpMove(sid, source, resolvedTarget)
      }
      success++
    } catch (e: any) {
      failed.push(name + ': ' + (e?.toString() || 'unknown'))
    }
  }

  pasteLoadingRemote.value = false

  if (pasteCancelled) {
    // keep clipboard so user can retry
  } else if (failed.length > 0) {
    msg.error(`Copied/Moved ${success}/${items.length}, ${failed.length} failed`)
  } else {
    msg.success(t('sftp.paste'))
  }

  if (!pasteCancelled) clipboard.value = null
  onRefreshRemote()
}

// --- Editor handlers ---

async function onEditFile(item: FileItem) {
  if (item.isDir) return
  const sid = panel.value?.sessionId
  if (!sid) return

  if (item.size > 5 * 1024 * 1024) {
    msg.warning(t('sftp.edit.fileTooLarge'))
    return
  }

  editorMode.value = 'remote'
  const path = joinPath(cwd.value, item.name)
  await fileEditorRef.value?.open(path, t('sftp.dialog.editTitle', { path }), 'remote')
}

// openExternalEditor launches the configured external editor on a remote file
// and starts background auto-upload. Returns false (without opening) when no
// editor is configured.
async function openExternalEditor(remotePath: string): Promise<boolean> {
  const sid = panel.value?.sessionId
  if (!sid) return false
  const editorCmd = localStateStore.state.externalEditor?.trim()
  if (!editorCmd) {
    msg.warning(t('sftp.editExternalNotConfigured'))
    return false
  }
  try {
    await SftpOpenExternalEditor(sid, remotePath, editorCmd)
    msg.info(t('sftp.editExternalStart', { path: remotePath }))
    return true
  } catch (e: any) {
    msg.error(e?.toString() || 'Failed to open external editor')
    return false
  }
}

async function onEditExternal(item: FileItem) {
  if (item.isDir) return
  if (item.size > 5 * 1024 * 1024) {
    msg.warning(t('sftp.edit.fileTooLarge'))
    return
  }
  await openExternalEditor(joinPath(cwd.value, item.name))
}

// openLocalExternalEditor launches the configured external editor directly on a
// local file (SFTP "local" pane). The file lives on disk, so edits save in
// place — no temp copy or auto-upload involved.
async function openLocalExternalEditor(localPath: string): Promise<boolean> {
  const editorCmd = localStateStore.state.externalEditor?.trim()
  if (!editorCmd) {
    msg.warning(t('sftp.editExternalNotConfigured'))
    return false
  }
  try {
    await OpenExternalEditorLocal(localPath, editorCmd)
    msg.info(t('sftp.editExternalStart', { path: localPath }))
    return true
  } catch (e: any) {
    msg.error(e?.toString() || 'Failed to open external editor')
    return false
  }
}

async function onLocalEditExternal(item: FileItem) {
  if (item.isDir) return
  if (item.size > 5 * 1024 * 1024) {
    msg.warning(t('sftp.edit.fileTooLarge'))
    return
  }
  await openLocalExternalEditor(joinPath(localCwd.value, item.name))
}

// --- Local file handlers ---

async function onLocalEditFile(item: FileItem) {
  if (item.isDir) return
  const sid = panel.value?.sessionId
  if (!sid) return
  if (item.size > 5 * 1024 * 1024) {
    msg.warning(t('sftp.edit.fileTooLarge'))
    return
  }
  editorMode.value = 'local'
  const path = joinPath(localCwd.value, item.name)
  await fileEditorRef.value?.open(path, t('sftp.dialog.editTitle', { path }), 'local')
}

function onLocalNewFile() {
  newFileName.value = 'newfile.txt'
  newFileMode.value = 'local'
  newFileError.value = ''
  newFileVisible.value = true
}

let localPasteCancelled = false

function onLocalCancelPaste() {
  localPasteCancelled = true
  pasteLoadingLocal.value = false
}

async function onLocalPaste() {
  const sid = panel.value?.sessionId
  if (!sid || !localClipboard.value) return
  const { items, sourceDir, mode } = localClipboard.value
  localPasteCancelled = false
  pasteLoadingLocal.value = true
  const targetDir = localCwd.value
  if (mode === 'cut' && sourceDir === targetDir) {
    msg.warning(t('sftp.paste.cutSameDir'))
    pasteLoadingLocal.value = false
    return
  }
  const existingNames = localFiles.value.map(f => f.name)
  const conflicts = items.filter(n => existingNames.includes(n))
  let resolveAction: 'overwrite' | 'rename' | 'cancel' = 'rename'
  if (conflicts.length > 0) {
    resolveAction = await showConflictDialog(conflicts)
    if (resolveAction === 'cancel') { pasteLoadingLocal.value = false; return }
  }
  let success = 0
  const failed: string[] = []
  for (const name of items) {
    if (localPasteCancelled) break
    const source = joinPath(sourceDir, name)
    const target = joinPath(targetDir, name)
    if (source === target) { /* copy: auto-rename below */ }
    const forceRename = source === target && mode === 'copy'
    let resolvedName = name
    if (forceRename || (resolveAction === 'rename' && existingNames.includes(name))) {
      resolvedName = autoRename(name, existingNames)
    }
    const resolvedTarget = joinPath(targetDir, resolvedName)
    existingNames.push(resolvedName)
    try {
      if (mode === 'copy') await SftpLocalCopy(sid, source, resolvedTarget)
      else await SftpLocalMove(sid, source, resolvedTarget)
      success++
    } catch (e: any) { failed.push(name + ': ' + (e?.toString() || 'unknown')) }
  }
  pasteLoadingLocal.value = false
  if (!localPasteCancelled) {
    if (failed.length > 0) msg.error(`Copied/Moved ${success}/${items.length}, ${failed.length} failed`)
    else msg.success(t('sftp.paste'))
  }
  if (!localPasteCancelled) localClipboard.value = null
  onRefreshLocal()
}

// --- New File handlers ---

function onNewFile() {
  newFileName.value = 'newfile.txt'
  newFileMode.value = 'remote'
  newFileError.value = ''
  newFileVisible.value = true
}

async function onNewFileCreate() {
  const name = newFileName.value.trim()
  if (!name) { newFileError.value = t('sftp.dialog.newFileEmpty'); return }
  if (name.includes('/') || name.includes('\\')) { newFileError.value = t('sftp.dialog.newFileInvalid'); return }
  const sid = panel.value?.sessionId
  if (!sid) return
  const isLocal = newFileMode.value === 'local'
  const existingNames = (isLocal ? localFiles.value : remoteFiles.value).map(f => f.name)
  const finalName = autoRename(name, existingNames)
  const targetPath = joinPath(isLocal ? localCwd.value : cwd.value, finalName)
  newFileCreating.value = true
  try {
    if (isLocal) {
      await SftpLocalPutContent(sid, targetPath, '')
      onRefreshLocal()
    } else {
      await SftpPutContent(sid, targetPath, '')
      onRefreshRemote()
    }
    msg.success(t('sftp.dialog.confirm'))
    newFileVisible.value = false
  } catch (e: any) {
    msg.error(e?.toString() || 'Failed to create file')
  } finally {
    newFileCreating.value = false
  }
}

// Dialog helpers
function openDialog(
  type: 'rename' | 'mkdir' | 'delete',
  title: string,
  inputValue: string = '',
  placeholder: string = '',
  message: string = ''
) {
  dialogType.value = type
  dialogTitle.value = title
  dialogInput.value = inputValue
  dialogPlaceholder.value = placeholder
  dialogMessage.value = message
  dialogVisible.value = true
}

function onDialogClosed() {
  dialogInput.value = ''
  dialogPlaceholder.value = ''
  dialogMessage.value = ''
  dialogItem.value = null
  dialogItems.value = []
}

async function onDialogConfirm() {
  dialogVisible.value = false
  const sid = panel.value?.sessionId
  if (!sid) return
  const isLocal = dialogMode.value === 'local'
  const baseDir = isLocal ? localCwd.value : cwd.value
  switch (dialogType.value) {
    case 'rename':
      if (dialogInput.value && dialogInput.value !== dialogItem.value?.name) {
        const oldPath = joinPath(baseDir, dialogItem.value!.name)
        const newPath = joinPath(baseDir, dialogInput.value)
        try {
          if (isLocal) {
            await SftpLocalRename(sid, oldPath, newPath)
            onRefreshLocal()
          } else {
            await SftpRename(sid, oldPath, newPath)
            onRefreshRemote()
          }
        } catch (e) { console.error('rename:', e) }
      }
      break
    case 'mkdir':
      if (dialogInput.value) {
        try {
          if (isLocal) {
            await SftpLocalMkdir(sid, joinPath(baseDir, dialogInput.value))
            onRefreshLocal()
          } else {
            await SftpMakeDir(sid, joinPath(baseDir, dialogInput.value))
            onRefreshRemote()
          }
        } catch (e) { console.error('mkdir:', e) }
      }
      break
    case 'delete':
      // Cancel any in-flight transfer of these files first — otherwise
      // SftpRemove can block forever while a transfer holds the SFTP handle.
      {
        const names = new Set(dialogItems.value.filter(i => i.name !== '..').map(i => i.name))
        for (const task of [...transferTasks]) {
          if ((task.status === 'running' || task.status === 'paused') && names.has(task.name)) {
            try { await SftpCancelTransfer(sid, task.id) } catch { /* ignore */ }
            task.status = 'cancelled'
          }
        }
      }
      for (const item of dialogItems.value) {
        const itemPath = joinPath(baseDir, item.name)
        try {
          if (isLocal) {
            await SftpLocalRemove(sid, itemPath, item.isDir)
          } else {
            await SftpRemove(sid, itemPath, item.isDir)
          }
        } catch (e) { console.error('delete item:', item.name, e) }
      }
      if (isLocal) {
        onRefreshLocal()
      } else {
        onRefreshRemote()
      }
      break
  }
}

function onRename(item: FileItem) {
  dialogItem.value = item
  openDialog(
    'rename',
    t('sftp.dialog.renameTitle'),
    item.name,
    t('sftp.dialog.renamePrompt', { name: item.name })
  )
}
function onDelete(items: FileItem[]) {
  dialogItems.value = items
  const hasDir = items.some(i => i.isDir)
  const hasFile = items.some(i => !i.isDir)
  let msg: string
  if (hasDir && hasFile) {
    msg = t('sftp.dialog.deleteConfirmMixed', { count: items.length })
  } else if (hasDir) {
    msg = t('sftp.dialog.deleteConfirmDir', { count: items.length })
  } else {
    msg = t('sftp.dialog.deleteConfirmFile', { count: items.length })
  }
  openDialog('delete', t('sftp.dialog.deleteTitle'), '', '', msg)
}
function onMkdir() {
  openDialog('mkdir', t('sftp.dialog.mkdirTitle'), '', t('sftp.dialog.mkdirPrompt'))
}
function onChmod(item: FileItem) {
  chmodItem.value = item
  chmodVisible.value = true
}

async function onChmodConfirm(octal: string) {
  const sid = panel.value?.sessionId
  const item = chmodItem.value
  if (!sid || !item) return
  chmodItem.value = null
  const isLocal = dialogMode.value === 'local'
  const baseDir = isLocal ? localCwd.value : cwd.value
  try {
    await SftpChmod(sid, joinPath(baseDir, item.name), octal)
    if (isLocal) onRefreshLocal()
    else onRefreshRemote()
  } catch (e) { console.error('chmod:', e) }
}

// OS file drops (resource manager / desktop) arrive via Wails v3's native pipe.
// Wails forwards the absolute local paths, which we upload directly from disk
// via SftpPut — memory-bounded and recursive for folders — instead of reading
// the file content into the webview and base64-ing it (which crashed on very
// large files; issue #699).
async function onNativeFileDrop(ev: any) {
  const d = ev?.data as { x: number; y: number; elementId?: string; filenames: string[] }
  if (!d?.filenames?.length) return
  // Only react to drops that landed on this tab's own remote pane; the file
  // sidebar is also a data-file-drop-target sharing this same event.
  if (d.elementId && d.elementId !== remoteDropId) return
  const sid = panel.value?.sessionId
  if (!sid) return

  const fileNames = d.filenames.map(fp =>
    fp.replace(/\\/g, '/').replace(/\/$/, '').split('/').pop() || 'upload')
  const action = await checkRemoteConflicts(fileNames)
  if (action === 'cancel') return
  const existing = remoteFiles.value.map(f => f.name)
  for (let i = 0; i < d.filenames.length; i++) {
    let name = fileNames[i]
    if (action === 'rename' && existing.includes(name)) name = autoRename(name, existing)
    existing.push(name)
    // recursive=false: SftpPut auto-upgrades to recursive for directories.
    SftpPut(sid, d.filenames[i], joinPath(cwd.value, name), false)
  }
}

function onDragOver(e: DragEvent) {
  if (dragSource.value === null) {
    e.dataTransfer!.dropEffect = 'copy'
  } else {
    e.dataTransfer!.dropEffect = 'move'
  }
}

function onDragStart(e: DragEvent) {
  const target = e.target as HTMLElement | null
  dragDroppedInternally = false
  if (target?.closest('.local-pane')) {
    dragSource.value = 'local'
  } else if (target?.closest('.remote-pane')) {
    dragSource.value = 'remote'
  }
}

function onDragEnter(mode: 'local' | 'remote') {
  // Internal drag: skip overlay on source pane
  if (dragSource.value !== null && dragSource.value === mode) return
  // External drag (from desktop): only show overlay on remote pane
  if (dragSource.value === null && mode === 'local') return
  if (mode === 'local') {
    dragEnterLocalCount++
    dragOverLocal.value = true
  } else {
    dragEnterRemoteCount++
    dragOverRemote.value = true
  }
}

function onDragLeave(mode: 'local' | 'remote') {
  if (mode === 'local') {
    dragEnterLocalCount--
    if (dragEnterLocalCount <= 0) {
      dragEnterLocalCount = 0
      dragOverLocal.value = false
    }
  } else {
    dragEnterRemoteCount--
    if (dragEnterRemoteCount <= 0) {
      dragEnterRemoteCount = 0
      dragOverRemote.value = false
    }
  }
}

function clearDragState() {
  dragOverLocal.value = false
  dragOverRemote.value = false
  dragEnterLocalCount = 0
  dragEnterRemoteCount = 0
  dragSource.value = null
}

async function onDropLocal(e: DragEvent) {
  e.preventDefault()
  dragDroppedInternally = true
  clearDragState()
  const data = e.dataTransfer?.getData('application/sftp-file')
  if (!data) return
  try {
    const parsed = JSON.parse(data)
    const items = parsed.items ? parsed.items : (parsed.name ? [{ name: parsed.name, isDir: !!parsed.isDir }] : [])
    if (items.length === 0 || parsed.mode !== 'remote') return
    const sid = panel.value?.sessionId
    if (!sid) return
    const fileNames = items.map((i: any) => i.name)
    const action = await checkLocalConflicts(fileNames)
    if (action === 'cancel') return
    const existingNames = localFiles.value.map(f => f.name)
    for (const item of items) {
      let resolvedName = item.name
      if (action === 'rename' && existingNames.includes(item.name)) {
        resolvedName = autoRename(item.name, existingNames)
      }
      existingNames.push(resolvedName)
      const remotePath = joinPath(cwd.value, item.name)
      const localPath = joinPath(localCwd.value, resolvedName).replace(/\\/g, '/')
      SftpGet(sid, remotePath, localPath, item.isDir)
    }
  } catch (e) { console.error('onDropLocal:', e) }
}

async function onDropRemote(e: DragEvent) {
  e.preventDefault()
  dragDroppedInternally = true
  clearDragState()

  // OS file drops (resource manager) are handled natively via
  // common:WindowFilesDropped (onNativeFileDrop) so we upload by absolute path;
  // skip the HTML5 content path that base64-escapes whole files into memory.
  const dt = e.dataTransfer
  const hasFileItems = !!(dt && dt.items && [...dt.items].some(it => it.kind === 'file'))
  const hasRawFiles = !!(dt && dt.files && dt.files.length > 0)
  if (hasFileItems || hasRawFiles) return

  // Internal SFTP file drag
  const data = e.dataTransfer?.getData('application/sftp-file')
  if (!data) return
  try {
    const parsed = JSON.parse(data)
    const items = parsed.items ? parsed.items : (parsed.name ? [{ name: parsed.name, isDir: !!parsed.isDir }] : [])
    if (items.length === 0 || parsed.mode !== 'local') return
    const fileNames = items.map((i: any) => i.name)
    const action = await checkRemoteConflicts(fileNames)
    if (action === 'cancel') return
    const existingNames = remoteFiles.value.map(f => f.name)
    for (const item of items) {
      let resolvedName = item.name
      if (action === 'rename' && existingNames.includes(item.name)) {
        resolvedName = autoRename(item.name, existingNames)
      }
      existingNames.push(resolvedName)
      const localPath = joinPath(localCwd.value, item.name)
      const remotePath = cwd.value + '/' + resolvedName
      SftpPut(panel.value?.sessionId!, localPath, remotePath, item.isDir)
    }
  } catch (e) { console.error('onDropRemote:', e) }
}
</script>

<style scoped>
.sftp-tab-content {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
}
.panes-area {
  flex: 1;
  display: flex;
  overflow: hidden;
}
.local-pane, .remote-pane {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border-right: 1px solid var(--border-subtle);
  position: relative;
}
.remote-pane {
  border-right: none;
}
.drop-overlay {
  position: absolute;
  inset: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--scrim);
  pointer-events: none;
}
.drop-overlay span {
  font-size: 14px;
  color: var(--text-primary);
  padding: 12px 24px;
  border: 2px dashed var(--border-hover);
  border-radius: var(--radius-md);
}

</style>
