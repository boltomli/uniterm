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
          :show-send-to-other="false"
          :breadcrumb-path="cwd"
          :breadcrumb-saved-paths="settingsStore.sftpBookmarks.remotePaths"
          :files="files"
          :loading="loading"
          :paste-loading="pasteLoading"
          :cut-item-names="cutItemNames"
          :clipboard-count="clipboardCount"
          :clipboard-mode="clipboard?.mode"
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
          @copy-to-clipboard="onCopyToClipboard"
          @cut-to-clipboard="onCutToClipboard"
          @paste="onPaste"
          @clear-clipboard="onClearClipboard"
          @cancel-paste="onCancelPaste"
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
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from '../i18n'
import { msg } from '../services/message'
import { useCompanionStore } from '../stores/companionStore'
import { usePanelStore } from '../stores/panelStore'
import { useSettingsStore } from '../stores/settingsStore'
import {
  SftpListRemote, SftpChangeRemoteDir, SftpOpenExternalEditor, ListSessions,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import {
  useFilePanel, useConflictDialog, useFileDialogs, useFileListing, useChmodDialog,
  useEditorBridge, useDragOver, useNativeFileDrop, remoteFileOps, resolveRemoteTarget, joinPath,
} from '../composables/useFilePanel'
import { bindExtEditUploadedToast } from '../composables/useFilePanel'
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
bindExtEditUploadedToast()
const companionStore = useCompanionStore()
const panelStore = usePanelStore()
const settingsStore = useSettingsStore()

const connecting = ref(false)
const connectError = ref('')
// Transfer panel: default height (px), adjustable by dragging its top edge.
const transferHeight = ref(130)

let refreshTimer: ReturnType<typeof setTimeout> | null = null
let refreshDebounce: ReturnType<typeof setTimeout> | null = null
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
const LIST_TIMEOUT_MS = 20000

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

// Listing / navigation engine (refresh coalescing + retry glue stays here).
const listing = useFileListing({
  sid: () => sessionId.value ?? undefined,
  list: SftpListRemote,
  changeDir: SftpChangeRemoteDir,
  resolveTarget: resolveRemoteTarget,
  listTimeoutMs: LIST_TIMEOUT_MS,
  onListSuccess: () => { connectError.value = '' },
  onListError: (err) => {
    if (/not connected/i.test(err)) { scheduleRefreshRetry(); return true }
    if (/timeout/i.test(err)) { msg.warning(t('companion.refreshTimeout')); return true }
    return false
  },
})
const { cwd, files, loading, onRefresh, onNavigate, onCancelLoad } = listing

// Shared dialog + conflict plumbing and the per-panel file actions live in
// the useFilePanel composable (same implementation as the SFTP tab's panes).
const conflicts = useConflictDialog()
const { conflictVisible, conflictFiles, onConflictResolve } = conflicts
const fileDialogs = useFileDialogs()
const { dlg: genDlg, onGenericConfirm, onGenericCancel } = fileDialogs

// Drag-hover + native (OS) file-drop routing. The Wails native drop owns the
// upload when bound; HTML5 drop handling backs off while it is active.
const { dragOver, onDragEnter, onDragLeave, onDragOver, clearDragState } = useDragOver()
const nativeDrop = useNativeFileDrop({
  elementId: FILE_DROP_ID,
  isActive: () => !!companionStore.filesVisible && !!sessionId.value,
  upload: (paths) => { clearDragState(); uploadPaths(paths) },
})
const bindFileDrop = nativeDrop.bind
const unbindFileDrop = nativeDrop.unbind

async function onDropUpload(e: DragEvent) {
  e.preventDefault()
  clearDragState()
  // When Wails native file-drop is bound, it owns the upload.
  // Handling HTML5 drop as well causes duplicate transfer records.
  if (nativeDrop.isBound()) return
  if (nativeDrop.recentlyDropped()) return
  if (!sessionId.value) return

  const dropped = e.dataTransfer?.files
  if (!dropped?.length) return

  // Prefer native paths if WebView exposes them
  const nativePaths: string[] = []
  for (let i = 0; i < dropped.length; i++) {
    const p = (dropped[i] as any).path as string | undefined
    if (p) nativePaths.push(p)
  }
  if (nativePaths.length === dropped.length) {
    await uploadPaths(nativePaths)
    return
  }

  const fileList = Array.from(dropped).filter(f => f.size > 0 || f.type)
  // Folder drops without native paths aren't supported via FileReader
  if (!fileList.length) {
    msg.warning(t('companion.folderDropHint'))
    return
  }
  await uploadFileObjects(fileList)
}

// Open the current companion's SFTP as a standalone tab, mirroring the SSH
// tab's context-menu action (which reconnects via app:connect-sftp).
function openStandaloneSftp() {
  const panel = panelStore.getPanel(companionStore.activeSshPanelId)
  if (!panel) return
  window.dispatchEvent(new CustomEvent('app:connect-sftp', { detail: panel }))
}

// ── Change-permission dialog (shared FileChmodDialog) ──
const chmod = useChmodDialog({
  target: (item) => {
    const sid = sessionId.value
    return sid ? { sid, path: joinPath(cwd.value, item.name) } : null
  },
  refresh: scheduleRefresh,
})
const { chmodVisible, chmodItem, onChmod, onChmodConfirm } = chmod

// ── Editor dialog bridge (shared FileEditorDialog) ──
const editor = useEditorBridge({ saved: () => onRefresh() })
const { editorVisible, fileEditorRef } = editor

// ── Shared panel logic (clipboard, dialogs, file ops) — mirrors the SFTP tab ──
const {
  clipboard, cutItemNames, clipboardCount, pasteLoading, preparingUpload,
  onCopyToClipboard, onCutToClipboard, onClearClipboard, onCancelPaste, onPaste,
  onRename, onDelete, onMkdir, onNewFile,
  onUpload, onDownloadTo,
  onEditFile, onEditExternal,
  onCancelTransfer, onPauseTransfer, onResumeTransfer, clearFinishedTransfers,
  onSaveBookmark, onRemoveBookmark,
  uploadPaths, uploadFileObjects,
} = useFilePanel({
  sid: () => sessionId.value ?? undefined,
  cwd,
  files,
  refresh: scheduleRefresh,
  ops: remoteFileOps,
  conflicts,
  dialogs: fileDialogs,
  bookmarkMode: 'remote',
  transferTasks: () => transferTasks.value,
  openEditor: (path, title) => editor.openEditor(path, title, 'remote'),
  openExternal: (sid, path, cmd) => SftpOpenExternalEditor(sid, path, cmd),
})

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
    const payload = ev?.data as { sessionId?: string; path?: string; status?: string }
    if (payload?.sessionId !== sessionId.value) return
    if (payload.status === 'uploaded') {
      onRefresh()
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
