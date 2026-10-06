<template>
  <el-dialog append-to-body
    :model-value="visible"
    @update:model-value="(v: boolean) => !v && onDeny()"
    :title="t('mcp.approvalTitle')"
    width="32rem"
    :close-on-click-modal="false"
    :show-close="false"
  >
    <div class="mcp-approval">
      <p class="mcp-approval-client">
        {{ t('mcp.approvalClient') }}:
        <el-tag size="small" type="warning">{{ request?.client }}</el-tag>
      </p>
      <p v-if="request?.connection" class="mcp-approval-conn">
        {{ t('mcp.approvalConnection') }}:
        <el-tag size="small">{{ request.connection }}</el-tag>
      </p>
      <div v-if="request?.command" class="mcp-approval-command">
        <label class="mcp-approval-label">{{ t('mcp.approvalCommand') }}</label>
        <pre>{{ request.command }}</pre>
      </div>
      <p v-else class="mcp-approval-note">{{ t('mcp.approvalConnectNote') }}</p>
      <el-input
        v-model="reason"
        type="textarea"
        :rows="2"
        :placeholder="t('mcp.approvalReasonPlaceholder')"
      />
      <p class="mcp-approval-timeout">{{ t('mcp.approvalTimeoutHint', { seconds: remaining }) }}</p>
    </div>
    <template #footer>
      <el-button @click="onDeny">{{ t('mcp.deny') }}</el-button>
      <el-button type="primary" @click="onApprove">{{ t('mcp.approve') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from 'vue'
import { useI18n } from '../i18n'
import type { MCPApprovalRequest } from '../types/mcp'

const { t } = useI18n()

const props = defineProps<{
  visible: boolean
  request: MCPApprovalRequest | null
}>()

const emit = defineEmits<{
  (e: 'resolve', approved: boolean, reason: string): void
}>()

const reason = ref('')
// Mirrors the backend's 110s approval timeout; hitting zero resolves the
// request as denied (matching what the backend would do anyway).
const remaining = ref(110)
let timer: ReturnType<typeof setInterval> | null = null

function stopTimer() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

watch(() => props.visible, v => {
  if (v) {
    reason.value = ''
    remaining.value = 110
    stopTimer()
    timer = setInterval(() => {
      remaining.value--
      if (remaining.value <= 0) {
        stopTimer()
        emit('resolve', false, reason.value)
      }
    }, 1000)
  } else {
    stopTimer()
  }
})

onBeforeUnmount(stopTimer)

function onApprove() {
  emit('resolve', true, '')
}

function onDeny() {
  // The reason textarea is the single source of truth: filled = deny with
  // reason, empty = plain deny (backend substitutes its default text).
  emit('resolve', false, reason.value)
}
</script>

<style scoped>
.mcp-approval-client {
  margin: 0 0 0.5rem;
}
.mcp-approval-conn {
  margin: 0 0 0.5rem;
}
.mcp-approval-label {
  display: block;
  font-size: 0.75rem;
  opacity: 0.7;
  margin-bottom: 0.25rem;
}
.mcp-approval-command pre {
  margin: 0 0 0.75rem;
  padding: 0.5rem 0.75rem;
  background: var(--bg-surface);
  color: var(--text-primary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: 0.8rem;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 12rem;
  overflow: auto;
}
.mcp-approval-note {
  opacity: 0.7;
  font-size: 0.8rem;
  margin: 0 0 0.75rem;
}
.mcp-approval-timeout {
  font-size: 0.75rem;
  opacity: 0.6;
  margin: 0.5rem 0 0;
}
</style>
