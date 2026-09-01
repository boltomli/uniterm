<template>
  <div
    class="companion-sidebar file-sidebar"
  >
    <div v-if="!sessionId" class="companion-empty">
      <span v-if="connecting">{{ t('companion.connecting') }}</span>
      <span v-else-if="connectError">{{ connectError }}</span>
      <span v-else>{{ t('companion.needSsh') }}</span>
    </div>

    <template v-else>
      <div
        class="file-body"
        :class="{ 'drag-active': dragOver }"
        :id="FILE_DROP_ID"
        data-file-drop-target
        @dragenter.prevent="onDragEnter"
        @dragleave.prevent="onDragLeave"
        @dragover.prevent="onDragOver"
        @drop.prevent="onDropUpload"
      >
        <div v-if="dragOver" class="drop-overlay">
          <span>{{ preparingUpload ? t('companion.preparingUpload') : t('sftp.dropHere') }}</span>
        </div>

        <FileList
          mode="remote"
          breadcrumb-mode="remote"
          :breadcrumb-path="cwd"
          :breadcrumb-saved-paths="settingsStore.sftpBookmarks.remotePaths"
          :files="files"
          :loading="loading"
          :paste-loading="false"
          :cut-item-names="[]"
          :clipboard-count="0"
          @navigate="onNavigate"
          @refresh="onRefresh"
          @upload="onUpload"
          @download-to="onDownloadTo"
          @rename="onRename"
          @delete="onDelete"
          @mkdir="onMkdir"
          @chmod="onChmod"
          @send-to-other="onDownloadTo"
          @edit="onEditFile"
          @edit-external="onEditExternal"
          @new-file="onNewFile"
          @copy-to-clipboard="() => {}"
          @cut-to-clipboard="() => {}"
          @paste="() => {}"
          @clear-clipboard="() => {}"
          @cancel-paste="() => {}"
          @open="onEditFile"
          @cancel-load="onCancelLoad"
          @save-bookmark="onSaveBookmark"
          @remove-bookmark="onRemoveBookmark"
        />
      </div>

      <!-- Transfer history / progress panel (pinned at the sidebar bottom) -->
      <TransferPanel
        v-model:height="transferHeight"
        resizable
        :tasks="transferTasks"
        @cancel="onCancelTransfer"
        @pause="onPauseTransfer"
        @resume="onResumeTransfer"
        @clearCompleted="clearFinishedTransfers"
      >
        <template #actions>
          <button
            class="filter-icon-btn"
            :disabled="!sessionId"
            :title="t('companion.openSftpTab')"
            @click="openStandaloneSftp"
          ><el-icon><ExternalLink :size="14" /></el-icon></button>
        </template>
      </TransferPanel>
    </template>

    <!-- Change permission dialog (shared with the full SFTP tab) -->
    <FileChmodDialog
      v-model:visible="chmodVisible"
      :name="chmodItem?.name"
      :owner="chmodItem?.owner"
      :group="chmodItem?.group"
      :mode="chmodItem?.mode"
      @confirm="onChmodConfirm"
    />

    <!-- Generic dialog (rename / new dir / new file / delete) -->
    <FileGenericDialog
      v-model:visible="genDlg.visible"
      :title="genDlg.title"
      :type="genDlg.type"
      :input-value="genDlg.inputValue"
      :placeholder="genDlg.placeholder"
      :message="genDlg.message"
      @update:inputValue="(v: string) => genDlg.inputValue = v"
      @confirm="onGenericConfirm"
      @cancel="onGenericCancel"
    />

    <!-- Overwrite/rename conflict dialog -->
    <FileConflictDialog
      v-model:visible="conflictVisible"
      :files="conflictFiles"
      @resolve="onConflictResolve"
    />

    <!-- Remote file editor (shared CodeMirror dialog) -->
    <FileEditorDialog
      ref="fileEditorRef"
      v-model:visible="editorVisible"
      :session-id="sessionId"
      mode="remote"
      @saved="onRefresh"
    />
  </div>
</template>

<script setup lang="ts">
import { ExternalLink } from '@lucide/vue'
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from '../i18n'
import { msg } from '../services/message'
import { useCompanionStore } from '../stores/companionStore'
import { usePanelStore } from '../stores/panelStore'
import { useSettingsStore } from '../stores/settingsStore'
import {
  SftpListRemote, SftpChangeRemoteDir,
  SftpMakeDir, SftpRemove, SftpRename, SftpChmod, SftpPutContent,
  SftpGet, SftpPut, SftpOpenExternalEditor, WriteTempFile, CreateTempUpload, AppendTempUpload,
  SftpCancelTransfer, SftpPauseTransfer, SftpResumeTransfer,
  OpenMultipleFilesDialog, OpenDirectoryDialog, ListSessions,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import { useLocalStateStore } from '../stores/localStateStore'
import FileList from './FileList.vue'
import type { FileItem } from './FileList.vue'
import TransferPanel from './TransferPanel.vue'
import FileChmodDialog from './FileChmodDialog.vue'
import FileEditorDialog from './FileEditorDialog.vue'
import FileGenericDialog from './FileGenericDialog.vue'
import FileConflictDialog from './FileConflictDialog.vue'
import { Events } from '@wailsio/runtime'
import { useTransferTaskEvents } from '../composables/useTransferTasks'

const { t } = useI18n()
const companionStore = useCompanionStore()
const panelStore = usePanelStore()
const settingsStore = useSettingsStore()
const localStateStore = useLocalStateStore()

const connecting = ref(false)
const connectError = ref('')
// Transfer panel: default height (px), adjustable by dragging its top edge.
const transferHeight = ref(130)

const cwd = ref('')
const files = ref<FileItem[]>([])
const loading = ref(false)
const dragOver = ref(false)
const preparingUpload = ref(false)
let dragEnterCount = 0
let loadVersion = 0
let refreshTimer: ReturnType<typeof setTimeout> | null = null
let refreshDebounce: ReturnType<typeof setTimeout> | null = null
let lastWailsDropAt = 0
let fileDropBound = false
let fileDropUnsub: (() => void) | null = null
// Unique id on this component's drop zone. Wails v3 forwards the id of the
// element the file was dropped on via common:WindowFilesDropped, so keep only
// the drops that actually landed on this sidebar (SFTP panes are also targets).
const FILE_DROP_ID = 'file-sidebar-drop'

const sessionId = computed(() => companionStore.currentSftpSessionId)
const transferKey = computed(() => companionStore.transferKey || 'companion-sftp')
const transferTasks = computed(() => panelStore.getTransferTasks(transferKey.value))
const transferEvents = useTransferTaskEvents(
  () => transferTasks.value,
  () => sessionId.value,
  (status) => { if (status === 'done') scheduleRefresh(400) },
)
const activeTransferCount = computed(() =>
  transferTasks.value.filter(t => t.status === 'running' || t.status === 'paused').length
)

const fileCount = computed(() => files.value.filter(f => !f.isDir && f.name !== '..').length)
const folderCount = computed(() => files.value.filter(f => f.isDir && f.name !== '..').length)

const LIST_TIMEOUT_MS = 20000
const REMOVE_TIMEOUT_MS = 60000

function joinPath(base: string, name: string): string {
  if (base.endsWith('/')) return base + name
  return base + '/' + name
}

function withTimeout<T>(promise: Promise<T>, ms: number, label: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${label} timeout`)), ms)
    promise.then(
      (v) => { clearTimeout(timer); resolve(v) },
      (e) => { clearTimeout(timer); reject(e) },
    )
  })
}

function scheduleRefreshRetry() {
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    if (sessionId.value && companionStore.filesVisible && files.value.length === 0) {
      onRefresh()
    }
  }, 800)
}

/** Coalesce bursty refresh (transfer complete + delete + mkdir). */
function scheduleRefresh(delay = 250) {
  if (refreshDebounce) clearTimeout(refreshDebounce)
  refreshDebounce = setTimeout(() => {
    refreshDebounce = null
    onRefresh()
  }, delay)
}

async function ensureConnected() {
  const pid = companionStore.activeSshPanelId
  if (!pid || !companionStore.filesVisible) return
  connecting.value = true
  connectError.value = ''
  try {
    await companionStore.ensureSftp(pid)
  } catch (e: any) {
    connectError.value = e?.toString?.() || t('companion.listFailed')
  } finally {
    connecting.value = false
  }
}

async function onRefresh() {
  const sid = sessionId.value
  if (!sid) return
  const version = ++loadVersion
  loading.value = true
  try {
    const result = await withTimeout(
      SftpListRemote(sid, cwd.value || ''),
      LIST_TIMEOUT_MS,
      'list',
    )
    if (version !== loadVersion) return
    files.value = result.files || []
    if (result.dir) cwd.value = result.dir
    connectError.value = ''
  } catch (e: any) {
    if (version !== loadVersion) return
    const err = e?.toString() || t('companion.listFailed')
    if (/not connected/i.test(err)) {
      scheduleRefreshRetry()
      return
    }
    if (/timeout/i.test(err)) {
      msg.warning(t('companion.refreshTimeout'))
      return
    }
    msg.error(err)
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

function onCancelLoad() {
  loadVersion++
  loading.value = false
}

async function onNavigate(path: string) {
  const sid = sessionId.value
  if (!sid) return
  let fullPath: string
  if (path === '..') {
    fullPath = '/' + cwd.value.split('/').filter(Boolean).slice(0, -1).join('/')
  } else if (!path.startsWith('/')) {
    fullPath = joinPath(cwd.value, path)
  } else {
    fullPath = path
  }
  const version = ++loadVersion
  loading.value = true
  try {
    const result = await SftpChangeRemoteDir(sid, fullPath)
    if (version !== loadVersion) return
    files.value = result.files || []
    cwd.value = result.dir || fullPath
  } catch (e: any) {
    if (version !== loadVersion) return
    msg.error(e?.toString() || t('companion.navFailed'))
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

function onSaveBookmark(path: string) {
  settingsStore.addSftpBookmark('remote', path)
}

function onRemoveBookmark(path: string) {
  settingsStore.removeSftpBookmark('remote', path)
}

async function onUpload() {
  const sid = sessionId.value
  if (!sid) return
  try {
    const localFiles = await OpenMultipleFilesDialog()
    if (!localFiles?.length) return
    const names = localFiles.map(fp => fp.replace(/\\/g, '/').split('/').pop() || 'upload')
    const action = await resolveConflicts(names)
    if (action === 'cancel') return
    const existing = files.value.map(f => f.name)
        for (let i = 0; i < localFiles.length; i++) {
      let name = names[i]
      if (action === 'rename' && existing.includes(name)) {
        name = autoRename(name, existing)
      }
      existing.push(name)
      SftpPut(sid, localFiles[i], joinPath(cwd.value, name), false)
    }
  } catch (e) {
    console.error('upload:', e)
  }
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

// --- Shared dialogs (rename / new dir / new file / delete / conflict) ---
const genDlg = reactive<{
  visible: boolean
  type: 'input' | 'message'
  title: string
  inputValue: string
  placeholder: string
  message: string
  resolve: ((r: { ok: boolean; value?: string }) => void) | null
}>({ visible: false, type: 'input', title: '', inputValue: '', placeholder: '', message: '', resolve: null })

function openGeneric(opts: { type?: 'input' | 'message'; title: string; inputValue?: string; placeholder?: string; message?: string }): Promise<{ ok: boolean; value?: string }> {
  return new Promise((resolve) => {
    genDlg.type = opts.type || 'input'
    genDlg.title = opts.title
    genDlg.inputValue = opts.inputValue || ''
    genDlg.placeholder = opts.placeholder || ''
    genDlg.message = opts.message || ''
    genDlg.resolve = resolve
    genDlg.visible = true
  })
}

function onGenericConfirm() {
  const value = genDlg.inputValue
  const resolve = genDlg.resolve
  genDlg.visible = false
  genDlg.resolve = null
  resolve?.({ ok: true, value })
}

function onGenericCancel() {
  const resolve = genDlg.resolve
  genDlg.visible = false
  genDlg.resolve = null
  resolve?.({ ok: false })
}

const conflictVisible = ref(false)
const conflictFiles = ref<string[]>([])
let conflictResolve: ((a: 'overwrite' | 'rename' | 'cancel') => void) | null = null

function showConflictDialog(conflicts: string[]): Promise<'overwrite' | 'rename' | 'cancel'> {
  return new Promise((resolve) => {
    conflictFiles.value = conflicts
    conflictResolve = resolve
    conflictVisible.value = true
  })
}

function onConflictResolve(action: 'overwrite' | 'rename' | 'cancel') {
  conflictVisible.value = false
  if (conflictResolve) { conflictResolve(action); conflictResolve = null }
}

async function resolveConflicts(fileNames: string[]): Promise<'overwrite' | 'rename' | 'cancel'> {
  const existing = files.value.map(f => f.name)
  const conflicts = fileNames.filter(n => existing.includes(n))
  if (!conflicts.length) return 'overwrite'
  return showConflictDialog(conflicts)
}

function onDragEnter(e: DragEvent) {
  if (!e.dataTransfer?.types?.includes('Files')) return
  dragEnterCount++
  dragOver.value = true
}

function onDragLeave() {
  dragEnterCount--
  if (dragEnterCount <= 0) {
    dragEnterCount = 0
    dragOver.value = false
  }
}

function onDragOver(e: DragEvent) {
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
}

function clearDragState() {
  dragOver.value = false
  dragEnterCount = 0
}

function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  const chunk = 0x8000
  let binary = ''
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}

function yieldToUI(): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, 0))
}

/** Chunked temp write — avoids freezing UI on large drops without native path. */
async function readAndUploadChunked(file: File, remotePath: string) {
  const sid = sessionId.value
  if (!sid) return
  try {
    const tmpPath = await CreateTempUpload(file.name)
    const chunkSize = 512 * 1024
    for (let offset = 0; offset < file.size; offset += chunkSize) {
      const blob = file.slice(offset, Math.min(offset + chunkSize, file.size))
      const buf = await blob.arrayBuffer()
      await AppendTempUpload(tmpPath, arrayBufferToBase64(buf))
      await yieldToUI()
    }
    SftpPut(sid, tmpPath, remotePath, false)
  } catch {
    msg.error(t('companion.uploadFailed'))
  }
}

async function readAndUpload(file: File, remotePath: string): Promise<void> {
  // Small files: single WriteTempFile is fine; larger: chunk to keep UI responsive
  if (file.size > 256 * 1024) {
    return readAndUploadChunked(file, remotePath)
  }
  const sid = sessionId.value
  if (!sid) return
  return new Promise((resolve) => {
    const reader = new FileReader()
    reader.onload = async () => {
      const base64 = (reader.result as string).split(',')[1]
      try {
        const tmpPath = await WriteTempFile(file.name, base64)
        SftpPut(sid, tmpPath, remotePath, false)
      } catch {
        msg.error(t('companion.uploadFailed'))
      } finally {
        resolve()
      }
    }
    reader.onerror = () => {
      msg.error(t('companion.uploadFailed'))
      resolve()
    }
    reader.readAsDataURL(file)
  })
}

async function uploadLocalPaths(localPaths: string[]) {
  const sid = sessionId.value
  if (!sid || !localPaths.length) return
  const names = localPaths.map(fp => fp.replace(/\\/g, '/').replace(/\/$/, '').split('/').pop() || 'upload')
  const action = await resolveConflicts(names)
  if (action === 'cancel') return
  const existing = files.value.map(f => f.name)
    for (let i = 0; i < localPaths.length; i++) {
    let name = names[i]
    if (action === 'rename' && existing.includes(name)) {
      name = autoRename(name, existing)
    }
    existing.push(name)
    // false = single file; backend auto-upgrades to recursive for directories
    SftpPut(sid, localPaths[i], joinPath(cwd.value, name), false)
  }
}

async function onDropUpload(e: DragEvent) {
  e.preventDefault()
  clearDragState()
  // When Wails native file-drop is bound, it owns the upload.
  // Handling HTML5 drop as well causes duplicate transfer records.
  if (fileDropBound) return
  if (Date.now() - lastWailsDropAt < 800) return

  const sid = sessionId.value
  if (!sid) return

  const dropped = e.dataTransfer?.files
  if (!dropped?.length) return

  // Prefer native paths if WebView exposes them
  const nativePaths: string[] = []
  for (let i = 0; i < dropped.length; i++) {
    const p = (dropped[i] as any).path as string | undefined
    if (p) nativePaths.push(p)
  }
  if (nativePaths.length === dropped.length) {
    await uploadLocalPaths(nativePaths)
    return
  }

  const fileList = Array.from(dropped).filter(f => f.size > 0 || f.type)
  // Folder drops without native paths aren't supported via FileReader
  if (!fileList.length) {
    msg.warning(t('companion.folderDropHint'))
    return
  }

  const names = fileList.map(f => f.name)
  const action = await resolveConflicts(names)
  if (action === 'cancel') return

  const existing = files.value.map(f => f.name)
  preparingUpload.value = true
    try {
    for (const f of fileList) {
      let resolvedName = f.name
      if (action === 'rename' && existing.includes(f.name)) {
        resolvedName = autoRename(f.name, existing)
      }
      existing.push(resolvedName)
      const remotePath = joinPath(cwd.value, resolvedName)
      await readAndUpload(f, remotePath)
    }
  } finally {
    preparingUpload.value = false
  }
}

function bindFileDrop() {
  if (fileDropBound) return
  try {
    fileDropUnsub = Events.On('common:WindowFilesDropped', (ev) => {
      const d = ev.data as { x: number; y: number; elementId?: string; filenames: string[] }
      if (!companionStore.filesVisible || !sessionId.value) return
      if (!d?.filenames?.length) return
      // Only react to drops that landed on this sidebar's own drop zone; the
      // SFTP tab's remote pane is also a data-file-drop-target and shares the
      // same event.
      if (d.elementId && d.elementId !== FILE_DROP_ID) return
      lastWailsDropAt = Date.now()
      clearDragState()
      uploadLocalPaths(d.filenames)
    })
    fileDropBound = true
  } catch {
    // runtime may be unavailable in browser preview
  }
}

function unbindFileDrop() {
  if (!fileDropBound) return
  try { fileDropUnsub?.(); fileDropUnsub = null } catch { /* ignore */ }
  fileDropBound = false
}

// Open the current companion's SFTP as a standalone tab, mirroring the SSH
// tab's context-menu action (which reconnects via app:connect-sftp).
function openStandaloneSftp() {
  const panel = panelStore.getPanel(companionStore.activeSshPanelId)
  if (!panel) return
  window.dispatchEvent(new CustomEvent('app:connect-sftp', { detail: panel }))
}

function clearFinishedTransfers() {
  const tasks = transferTasks.value
  for (let i = tasks.length - 1; i >= 0; i--) {
    const st = tasks[i].status
    if (st === 'done' || st === 'error' || st === 'cancelled') {
      tasks.splice(i, 1)
    }
  }
}

async function onDownloadTo(items: FileItem[]) {
  const sid = sessionId.value
  if (!sid) return
  try {
    const dir = await OpenDirectoryDialog()
    if (!dir) return
        for (const item of items) {
      if (item.name === '..') continue
      const remotePath = joinPath(cwd.value, item.name)
      const localPath = (dir + '/' + item.name).replace(/\\/g, '/')
      SftpGet(sid, remotePath, localPath, item.isDir)
    }
  } catch (e) {
    console.error('downloadTo:', e)
  }
}

async function onRename(item: FileItem) {
  const sid = sessionId.value
  if (!sid) return
  const r = await openGeneric({ title: t('sftp.rename'), inputValue: item.name })
  if (!r.ok || !r.value || r.value === item.name) return
  await SftpRename(sid, joinPath(cwd.value, item.name), joinPath(cwd.value, r.value))
  scheduleRefresh()
}

async function onDelete(items: FileItem[]) {
  const sid = sessionId.value
  if (!sid) return
  const targets = items.filter(i => i.name !== '..')
  if (!targets.length) return
  const r = await openGeneric({ type: 'message', title: t('sftp.dialog.deleteTitle'), message: t('sftp.dialog.deleteConfirmMixed', { count: targets.length }) })
  if (!r.ok) return
    const names = new Set(targets.map(i => i.name))
    // Stop any in-flight upload/download of these files first — otherwise
    // SftpRemove can block forever while the transfer holds the SFTP handle.
    for (const task of [...transferTasks.value]) {
      if ((task.status === 'running' || task.status === 'paused') && names.has(task.name)) {
        try { await SftpCancelTransfer(sid, task.id) } catch { /* ignore */ }
        task.status = 'cancelled'
      }
    }

    // Optimistic UI — remove from list immediately so delete never "looks stuck"
    files.value = files.value.filter(f => !names.has(f.name))

    for (const item of targets) {
      try {
        await withTimeout(
          SftpRemove(sid, joinPath(cwd.value, item.name), item.isDir),
          REMOVE_TIMEOUT_MS,
          'remove',
        )
      } catch (e: any) {
        const err = e?.toString?.() || String(e)
        // File may already be gone — ignore not-found; otherwise put back & report
        if (!/no such file|not found|no such file or directory|timeout/i.test(err)) {
          msg.error(err)
          if (!files.value.some(f => f.name === item.name)) {
            files.value = [...files.value, item]
          }
        } else if (/timeout/i.test(err)) {
          // Likely deleted on server but reply stalled — keep optimistic removal
          msg.warning(t('companion.deleteTimeout'))
        }
      }
    }
    scheduleRefresh(300)
}

async function onMkdir() {
  const sid = sessionId.value
  if (!sid) return
  const r = await openGeneric({ title: t('sftp.newDirectory') })
  if (!r.ok || !r.value) return
  await SftpMakeDir(sid, joinPath(cwd.value, r.value))
  scheduleRefresh()
}

async function onNewFile() {
  const sid = sessionId.value
  if (!sid) return
  const r = await openGeneric({ title: t('sftp.newFile') })
  if (!r.ok || !r.value) return
  await SftpPutContent(sid, joinPath(cwd.value, r.value), '', 'utf-8')
  scheduleRefresh()
}

const chmodVisible = ref(false)
const chmodItem = ref<FileItem | null>(null)

function onChmod(item: FileItem) {
  chmodItem.value = item
  chmodVisible.value = true
}

async function onChmodConfirm(octal: string) {
  const sid = sessionId.value
  const item = chmodItem.value
  if (!sid || !item) return
  chmodItem.value = null
  try {
    await SftpChmod(sid, joinPath(cwd.value, item.name), octal)
    scheduleRefresh()
  } catch { /* ignore */ }
}

// ── Remote file editor (shared FileEditorDialog) ──
const editorVisible = ref(false)
const fileEditorRef = ref<{ open: (path: string, title: string, mode?: 'remote' | 'local') => Promise<void> } | null>(null)

async function onEditFile(item: FileItem) {
  if (item.isDir) return
  const sid = sessionId.value
  if (!sid) return
  if (item.size > 5 * 1024 * 1024) {
    msg.warning(t('sftp.edit.fileTooLarge'))
    return
  }
  const path = joinPath(cwd.value, item.name)
  await fileEditorRef.value?.open(path, t('sftp.dialog.editTitle', { path }), 'remote')
}

// onEditExternal opens a remote file in the configured external editor with
// backend auto-upload (same flow as the SFTP tab's remote pane).
async function onEditExternal(item: FileItem) {
  if (item.isDir) return
  const sid = sessionId.value
  if (!sid) return
  if (item.size > 5 * 1024 * 1024) {
    msg.warning(t('sftp.edit.fileTooLarge'))
    return
  }
  const editorCmd = localStateStore.state.externalEditor?.trim()
  if (!editorCmd) {
    msg.warning(t('sftp.editExternalNotConfigured'))
    return
  }
  const path = joinPath(cwd.value, item.name)
  try {
    await SftpOpenExternalEditor(sid, path, editorCmd)
    msg.info(t('sftp.editExternalStart', { path }))
  } catch (e: any) {
    msg.error(e?.toString() || 'Failed to open external editor')
  }
}

async function onCancelTransfer(taskId: string) {
  const sid = sessionId.value
  if (!sid) return
  try { await SftpCancelTransfer(sid, taskId) } catch {}
}
async function onPauseTransfer(taskId: string) {
  const sid = sessionId.value
  if (!sid) return
  try { await SftpPauseTransfer(sid, taskId) } catch {}
}
async function onResumeTransfer(taskId: string) {
  const sid = sessionId.value
  if (!sid) return
  try { await SftpResumeTransfer(sid, taskId) } catch {}
}

let unsubStatus: (() => void) | null = null
let unsubData: (() => void) | null = null
let unsubExtEdit: (() => void) | null = null

function bindListeners() {
  unsubStatus?.()
  unsubData?.()
  unsubExtEdit?.()
  unsubStatus =Events.On('session:status', (ev) => { const payload: { id: string; status: string } = ev.data; 
    if (payload.id !== sessionId.value) return
    if (payload.status === 'connected') {
      onRefresh()
    } else if (payload.status === 'error') {
      connectError.value = t('sftp.connectError')
    }
   })
  unsubData =Events.On('session:data', (ev) => { const payload: { id: string; data: string } = ev.data;
    if (payload.id !== sessionId.value) return
    const connMatch = payload.data.match(/\[Connection failed: ([^\]]+)\]/)
    if (connMatch) {
      connectError.value = connMatch[1]
      msg.error(connMatch[1])
      return
    }
  })

  // External-editor status events (started / uploaded / closed): refresh the
  // listing when edits land back on the remote, like the SFTP tab does.
  unsubExtEdit = Events.On('sftp:extedit', (ev) => {
    const payload = ev?.data as { sessionId?: string; status?: string }
    if (payload?.sessionId !== sessionId.value) return
    if (payload.status === 'uploaded') {
      msg.success(t('sftp.editExternalUploaded'))
      onRefresh()
    } else if (payload.status === 'closed') {
      msg.success(t('sftp.editExternalClosed'))
    }
  })

  // Transfer tasks (start/progress/complete) are tracked by the shared composable.
  transferEvents.bind()
}

/** Restore this panel's cached listing; returns true if a non-empty cache existed. */
function restoreCache(): boolean {
  const pid = companionStore.activeSshPanelId
  const cached = pid ? companionStore.getFileViewCache(pid) : null
  if (!cached || !cached.files.length) return false
  cwd.value = cached.cwd
  files.value = cached.files as FileItem[]
  return true
}

watch(sessionId, async (sid) => {
  // Re-entering an already-visited tab: restore its cached listing instead of
  // re-fetching it, so switching back shows the previous content instantly.
  if (restoreCache()) {
    if (sid) bindListeners()
    return
  }
  files.value = []
  cwd.value = ''
  if (!sid) return
  bindListeners()
  try {
    const sessions = await ListSessions()
    const sess = sessions.find(s => s.id === sid)
    if (sess?.status === 'connected') await onRefresh()
    else scheduleRefreshRetry()
  } catch {
    scheduleRefreshRetry()
  }
})

// Persist the current listing per SSH panel so a later switch-back can restore it.
watch([files, cwd], () => {
  const pid = companionStore.activeSshPanelId
  if (!pid) return
  companionStore.setFileViewCache(pid, { cwd: cwd.value, files: files.value })
})

watch(() => companionStore.filesVisible, (v) => {
  if (v) {
    ensureConnected()
    bindFileDrop()
  } else {
    unbindFileDrop()
  }
})

watch(() => companionStore.activeSshPanelId, () => {
  if (companionStore.filesVisible) ensureConnected()
})

onMounted(() => {
  bindListeners()
  // Re-mounting after the view was hidden (e.g. switching files<->monitor):
  // restore this panel's cached listing since the session didn't change.
  restoreCache()
  if (companionStore.filesVisible) {
    ensureConnected()
    bindFileDrop()
  }
})

onUnmounted(() => {
  unsubStatus?.()
  unsubData?.()
  unsubExtEdit?.()
  unbindFileDrop()
  if (refreshTimer) clearTimeout(refreshTimer)
})
</script>

<style scoped>
.companion-sidebar {
  background: transparent;
  display: flex;
  flex-direction: column;
  position: relative;
  flex: 1 1 0;
  width: 100%;
  height: auto;
  min-height: 0;
  overflow: hidden;
}
.companion-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.companion-actions {
  display: flex;
  gap: 2px;
  align-items: center;
}
.filter-icon-btn {
  width: 26px;
  height: 26px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  flex-shrink: 0;
}
.filter-icon-btn:hover {
  color: var(--text-primary);
  background: var(--bg-hover);
}
.filter-icon-btn:disabled {
  opacity: 0.4;
  cursor: default;
}
.transfer-badge {
  position: absolute;
  top: -2px;
  right: -2px;
  min-width: 14px;
  height: 14px;
  padding: 0 3px;
  border-radius: 999px;
  background: var(--accent, #22d3ee);
  color: #0b1220;
  font-size: 10px;
  font-weight: 700;
  line-height: 14px;
  text-align: center;
}
.companion-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 12px;
  padding: 16px;
  text-align: center;
}
.file-body {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-height: 0;
  position: relative;
}
.file-body.drag-active {
  outline: 1px solid var(--accent, #22d3ee);
  outline-offset: -1px;
}
.drop-overlay {
  position: absolute;
  inset: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--scrim, rgba(0, 0, 0, 0.45));
  pointer-events: none;
}
.drop-overlay span {
  font-size: 14px;
  color: var(--text-primary);
  padding: 12px 24px;
  border: 2px dashed var(--border-hover, var(--accent, #22d3ee));
  border-radius: 8px;
  background: var(--bg-elevated, rgba(0, 0, 0, 0.35));
}
.transfer-panel {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  border-top: 1px solid var(--border-subtle);
  background: var(--bg-surface, var(--bg-elevated));
  min-height: 0;
}
.transfer-panel-resize {
  height: 5px;
  flex-shrink: 0;
  cursor: ns-resize;
  background: transparent;
}
.transfer-panel-resize:hover {
  background: var(--bg-hover);
}
.transfer-panel-head {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  padding: 6px 8px 4px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  flex-shrink: 0;
}
.transfer-panel-actions {
  display: flex;
  align-items: center;
  gap: 2px;
}
.transfer-empty {
  padding: 16px 12px;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
}
.transfer-panel :deep(.transfer-progress-bar) {
  border-top: none;
  max-height: none;
  flex: 1;
  overflow-y: auto;
  padding: 4px 8px 8px;
}
.file-footer {
  flex-shrink: 0;
  padding: 4px 10px;
  font-size: 11px;
  color: var(--text-muted);
  border-top: 1px solid var(--border-subtle);
}
.footer-transfer {
  color: var(--accent, #22d3ee);
  cursor: pointer;
}
.footer-transfer:hover {
  text-decoration: underline;
}
.companion-editor-meta {
  margin-bottom: 8px;
}
.lang-badge {
  display: inline-block;
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--bg-hover, rgba(255,255,255,0.08));
  color: var(--text-secondary);
}
.companion-editor-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
}
.companion-editor-opts {
  display: flex;
  gap: 8px;
}
</style>
