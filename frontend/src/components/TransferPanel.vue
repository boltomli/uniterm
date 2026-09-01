<template>
  <div
    ref="panelRef"
    class="transfer-panel"
    :style="resizable && height != null ? { height: height + 'px' } : undefined"
  >
    <div v-if="resizable" class="transfer-panel-resize" @mousedown.prevent="onResizeStart" />
    <div class="transfer-panel-head">
      <div class="transfer-panel-actions">
        <span v-if="title" class="transfer-panel-title">{{ title }}</span>
        <button
          class="filter-icon-btn"
          :disabled="!hasFinished"
          :title="t('companion.clearTransfers')"
          @click="emit('clearCompleted')"
        ><el-icon><BrushCleaning :size="14" /></el-icon></button>
      </div>
      <div class="transfer-panel-actions">
        <slot name="actions" />
      </div>
    </div>
    <div v-if="!tasks.length" class="transfer-empty">{{ t('companion.noTransfers') }}</div>
    <div v-else class="transfer-progress-bar">
      <div v-for="task in tasks" :key="task.id" class="transfer-task">
        <span class="task-type"><ArrowUp v-if="task.type === 'upload'" :size="12" /><ArrowDown v-else :size="12" /></span>
        <span class="task-name">{{ task.name }}</span>
        <span class="task-eta" v-if="task.eta">{{ task.eta }}</span>
        <span class="task-speed" v-if="task.status === 'running' || task.status === 'paused'">{{ task.speed || '--' }}</span>
        <el-progress
          :percentage="task.percentage"
          :status="task.status === 'error' ? 'exception' : task.status === 'cancelled' ? 'warning' : undefined"
          :stroke-width="4"
          style="flex: 1"
        />
        <div class="task-actions">
          <button
            v-if="task.status === 'running'"
            class="btn btn-ghost btn-icon btn-sm"
            :title="t('sftp.pauseTransfer')"
            @click="emit('pause', task.id)"
          ><Pause :size="14" /></button>
          <button
            v-else-if="task.status === 'paused'"
            class="btn btn-ghost btn-icon btn-sm"
            :title="t('sftp.resumeTransfer')"
            @click="emit('resume', task.id)"
          ><Play :size="14" /></button>
          <button
            v-if="task.status === 'running' || task.status === 'paused'"
            class="btn btn-ghost btn-icon btn-sm danger"
            :title="t('sftp.cancelTransfer')"
            @click="emit('cancel', task.id)"
          ><X :size="14" /></button>
          <span v-else-if="task.status === 'cancelled'" class="status-text">{{ t('sftp.cancelled') }}</span>
          <span v-else-if="task.status === 'done'" class="status-text done" :title="t('sftp.done')"><Check :size="14" /></span>
          <span v-else-if="task.status === 'error'" class="status-text error">{{ t('sftp.error') }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { X, Pause, Play, ArrowUp, ArrowDown, Check, BrushCleaning } from '@lucide/vue'
import { useI18n } from '../i18n'
import type { TransferTaskUI } from '../stores/panelStore'

const props = defineProps<{
  tasks: TransferTaskUI[]
  height?: number
  resizable?: boolean
  title?: string
}>()

const emit = defineEmits<{
  (e: 'cancel', taskId: string): void
  (e: 'pause', taskId: string): void
  (e: 'resume', taskId: string): void
  (e: 'clearCompleted'): void
  (e: 'update:height', h: number): void
}>()

const { t } = useI18n()
const panelRef = ref<HTMLElement | null>(null)

const hasFinished = computed(() =>
  props.tasks.some(t => t.status === 'done' || t.status === 'error' || t.status === 'cancelled')
)

function onResizeStart(e: MouseEvent) {
  const el = panelRef.value
  if (!el) return
  const startY = e.clientY
  const startH = el.offsetHeight
  const maxH = el.parentElement ? Math.max(el.parentElement.clientHeight - 60, 120) : 720
  function onMove(ev: MouseEvent) {
    emit('update:height', Math.min(Math.max(startH + (startY - ev.clientY), 100), maxH))
  }
  function onUp() {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}
</script>

<style scoped>
.transfer-panel {
  position: relative;
  display: flex;
  flex-direction: column;
  min-height: 100px;
  border-top: 1px solid var(--border-subtle);
  background: var(--bg-elevated);
  flex-shrink: 0;
}
.transfer-panel-resize {
  height: 4px;
  cursor: ns-resize;
  flex-shrink: 0;
  background: transparent;
}
.transfer-panel-resize:hover {
  background: var(--accent);
  opacity: 0.5;
}
.transfer-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 8px 4px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  flex-shrink: 0;
}
.transfer-panel-title {
  font-weight: 600;
  color: var(--text-primary);
}
.transfer-panel-actions {
  display: flex;
  align-items: center;
  gap: 2px;
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
.transfer-empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 12px;
  pointer-events: none;
  z-index: 0;
}
.transfer-panel :deep(.transfer-progress-bar) {
  border-top: none;
  max-height: none;
  flex: 1;
  overflow-y: auto;
}
.transfer-progress-bar {
  padding: 4px 12px;
  background: var(--bg-elevated);
  border-top: 1px solid var(--border-subtle);
  max-height: 200px;
  overflow-y: auto;
}
.transfer-task {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0;
  height: 26px;
}
.task-type {
  display: inline-flex;
  align-items: center;
  color: var(--accent);
  flex-shrink: 0;
}
.task-name {
  font-size: 11px;
  line-height: 1;
  font-family: var(--font-mono);
  color: var(--text-secondary);
  min-width: 90px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.task-eta {
  font-size: 10px;
  line-height: 1;
  font-family: var(--font-mono);
  color: var(--text-disabled);
  min-width: 48px;
  flex-shrink: 0;
}
.task-speed {
  font-size: 10px;
  line-height: 1;
  font-family: var(--font-mono);
  color: var(--text-disabled);
  min-width: 56px;
  flex-shrink: 0;
}
.task-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  min-width: 52px;
  height: 24px;
}
.status-text {
  font-size: 10px;
  line-height: 1;
  color: var(--text-disabled);
  flex-shrink: 0;
}
.status-text.done {
  color: var(--accent);
}
.status-text.error {
  color: var(--error);
}
</style>

<style>
/* Progress percentage text — not scoped so it penetrates el-progress */
.transfer-progress-bar .el-progress__text {
  font-size: 11px !important;
  font-family: var(--font-mono);
}
</style>