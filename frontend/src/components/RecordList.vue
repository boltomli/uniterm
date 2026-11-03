<template>
  <el-dialog
    v-model="visible"
    :title="t('record.list')"
    width="720px"
    append-to-body
    :close-on-click-modal="false"
    @open="loadList"
  >
    <div class="record-list-toolbar">
      <el-button size="small" @click="loadList" :loading="loading">{{ t('common.refresh') }}</el-button>
      <el-button size="small" @click="openRecordsDir" :disabled="recordings.length === 0">{{ t('record.openDir') }}</el-button>
      <el-button size="small" type="danger" plain @click="deleteAllRecordings" :disabled="recordings.length === 0" :loading="deletingAll">
        {{ t('record.deleteAll') }}
      </el-button>
      <span class="record-count">{{ recordings.length }} {{ t('record.list') }}</span>
    </div>

    <el-table
      :data="recordings"
      v-loading="loading"
      empty-text=""
      size="small"
      style="width: 100%"
      :row-key="(r: RecordingInfo) => r.filePath"
    >
      <el-table-column :label="t('record.connection')" min-width="160" show-overflow-tooltip>
        <template #default="{ row }">
          {{ row.title || row.sessionId }}
        </template>
      </el-table-column>
      <el-table-column :label="t('record.startTime')" width="160">
        <template #default="{ row }">
          {{ formatStartTime(row.startTime) }}
        </template>
      </el-table-column>
      <el-table-column :label="t('record.duration')" width="90">
        <template #default="{ row }">
          {{ formatDuration(row.duration) }}
        </template>
      </el-table-column>
      <el-table-column :label="t('record.size')" width="90">
        <template #default="{ row }">
          {{ formatSize(row.size) }}
        </template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="140" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" link @click="playRecording(row)">
            {{ t('record.play') }}
          </el-button>
          <el-button size="small" type="danger" link @click="deleteRecording(row)">
            {{ t('record.delete') }}
          </el-button>
        </template>
      </el-table-column>
      <template #empty>
        <div class="record-empty">{{ t('record.emptyList') }}</div>
      </template>
    </el-table>

    <!-- 回放器弹窗 -->
    <TerminalPlayer
      v-model:visible="playerVisible"
      :file-path="playerFilePath"
    />
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { ElMessageBox } from 'element-plus'
import { useI18n } from '../i18n'
import { msg } from '../services/message'
import { RecorderList, RecorderDelete, RecorderOpenDir } from '../../bindings/github.com/ys-ll/uniterm/app'
import TerminalPlayer from './TerminalPlayer.vue'

interface RecordingInfo {
  filePath: string
  fileName: string
  sessionId: string
  title: string
  startTime: number
  duration: number
  size: number
}

const { t } = useI18n()

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ 'update:visible': [boolean] }>()

const visible = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v),
})

const recordings = ref<RecordingInfo[]>([])
const loading = ref(false)
const deletingAll = ref(false)
const playerVisible = ref(false)
const playerFilePath = ref('')

async function loadList() {
  loading.value = true
  try {
    const list = await RecorderList()
    recordings.value = (list || []) as RecordingInfo[]
  } catch (e: any) {
    msg.error(String(e?.message ?? e))
  } finally {
    loading.value = false
  }
}

function playRecording(row: RecordingInfo) {
  playerFilePath.value = row.filePath
  playerVisible.value = true
}

async function deleteRecording(row: RecordingInfo) {
  try {
    await ElMessageBox.confirm(t('record.deleteConfirm'), t('common.confirm'), {
      type: 'warning',
    })
  } catch {
    return
  }
  try {
    await RecorderDelete(row.filePath)
    msg.success(t('record.delete'))
    await loadList()
  } catch (e: any) {
    msg.error(t('record.deleteFailed', { error: String(e?.message ?? e) }))
  }
}

async function deleteAllRecordings() {
  try {
    await ElMessageBox.confirm(t('record.deleteAllConfirm'), t('common.confirm'), {
      type: 'warning',
    })
  } catch {
    return
  }
  deletingAll.value = true
  try {
    // The list is bounded by what RecorderList returned; deletions run
    // sequentially so a busy backend never sees concurrent file removals.
    for (const row of recordings.value) {
      try {
        await RecorderDelete(row.filePath)
      } catch (e: any) {
        msg.error(t('record.deleteFailed', { error: String(e?.message ?? e) }))
      }
    }
    await loadList()
  } finally {
    deletingAll.value = false
  }
}

async function openRecordsDir() {
  try {
    await RecorderOpenDir()
  } catch (e: any) {
    msg.error(String(e?.message ?? e))
  }
}

function formatStartTime(ts: number): string {
  if (!ts) return '-'
  try {
    return new Date(ts * 1000).toLocaleString()
  } catch {
    return '-'
  }
}

function formatDuration(sec: number): string {
  if (!sec || sec <= 0) return '-'
  const mm = Math.floor(sec / 60)
  const ss = Math.floor(sec % 60)
  if (mm > 0) return `${mm}m${ss.toString().padStart(2, '0')}s`
  return `${ss}s`
}

function formatSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '-'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}
</script>

<style scoped>
.record-list-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}
.record-count {
  font-size: 12px;
  color: var(--text-muted);
}
.record-empty {
  padding: 32px 0;
  color: var(--text-muted);
  font-size: 13px;
  text-align: center;
}
</style>