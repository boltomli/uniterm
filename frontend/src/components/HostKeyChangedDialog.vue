<template>
  <el-dialog
    v-model="visible"
    :title="t('session.hostKeyChangedTitle')"
    width="560px"
    :close-on-click-modal="false"
    :close-on-press-escape="accepting === ''"
    align-center
  >
    <div class="hk-warning">
      <el-icon class="hk-warning-icon" :size="20"><TriangleAlert /></el-icon>
      <span>{{ t('session.hostKeyChangedBody', { host: current?.host ?? '' }) }}</span>
    </div>

    <div class="hk-rows">
      <div class="hk-row">
        <span class="hk-label">{{ t('session.hostKeyHost') }}</span>
        <span class="hk-value">{{ hostPort }}</span>
      </div>
      <div class="hk-row">
        <span class="hk-label">{{ t('session.hostKeyType') }}</span>
        <span class="hk-value mono">{{ current?.keyType }}</span>
      </div>
      <div class="hk-row">
        <span class="hk-label">{{ t('session.hostKeyOldFp') }}</span>
        <span class="hk-value mono hk-fp" :title="t('common.copy')" @click="copy(current?.oldFp)">{{ current?.oldFp }}</span>
      </div>
      <div class="hk-row">
        <span class="hk-label">{{ t('session.hostKeyNewFp') }}</span>
        <span class="hk-value mono hk-fp" :title="t('common.copy')" @click="copy(current?.newFp)">{{ current?.newFp }}</span>
      </div>
    </div>

    <template #footer>
      <el-button @click="closeCurrent">{{ t('common.cancel') }}</el-button>
      <el-button :loading="accepting === 'once'" @click="onAccept('once')">
        {{ t('session.hostKeyConnectOnce') }}
      </el-button>
      <el-button type="primary" :loading="accepting === 'save'" @click="onAccept('save')">
        {{ t('session.hostKeyAcceptReconnect') }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import { TriangleAlert } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { useI18n } from '../i18n'
import { AcceptHostKeyChange, AcceptHostKeyOnce } from '../../bindings/github.com/ys-ll/uniterm/app'
import { writeClipboard } from '../composables/useClipboardWrite'
import { msg } from '../services/message'

// Global host key change dialog. The backend emits session:hostkey-changed
// whenever a TOFU-verified SSH connection rejects a changed server key; this
// singleton queues the events and walks the user through each one. The user
// can save the new key (and reconnect), connect once without saving it
// (a one-shot bypass without saving it), or cancel.

interface HostKeyChange {
  host: string
  port: string
  sessionId?: string
  panelId?: string
  keyType: string
  oldFp: string
  newFp: string
  newKeyB64: string
}

const { t } = useI18n()

const visible = ref(false)
const accepting = ref<'' | 'save' | 'once'>('')
const queue = ref<HostKeyChange[]>([])
const current = computed(() => queue.value[0] ?? null)
const hostPort = computed(() => {
  const c = current.value
  if (!c) return ''
  return c.port && c.port !== '22' ? `[${c.host}]:${c.port}` : c.host
})

let unlisten: (() => void) | null = null

function onEvent(ev: any) {
  const d = ev?.data
  if (!d?.host || !d?.newFp) return
  queue.value.push({
    host: String(d.host),
    port: String(d.port ?? '22'),
    sessionId: d.sessionId ? String(d.sessionId) : undefined,
    panelId: d.panelId ? String(d.panelId) : undefined,
    keyType: String(d.keyType ?? ''),
    oldFp: String(d.oldFp ?? ''),
    newFp: String(d.newFp ?? ''),
    newKeyB64: String(d.newKeyB64 ?? ''),
  })
  if (!visible.value) visible.value = true
}

function closeCurrent() {
  queue.value.shift()
  if (queue.value.length === 0) visible.value = false
}

async function copy(text?: string) {
  if (!text) return
  try {
    await writeClipboard(text)
    msg.success(t('common.copied'))
  } catch (_) {
    // clipboard unavailable — the fingerprint is still fully visible
  }
}

async function onAccept(mode: 'save' | 'once') {
  const c = current.value
  if (!c || accepting.value) return
  accepting.value = mode
  try {
    if (mode === 'save') {
      await AcceptHostKeyChange(c.host, c.keyType, c.newKeyB64, Number(c.port) || 22)
    } else {
      await AcceptHostKeyOnce(c.host, c.keyType, c.newKeyB64, Number(c.port) || 22)
    }
  } catch (e: any) {
    msg.error(`${t('session.hostKeyAcceptFailed')}: ${e?.message ?? e}`)
    accepting.value = ''
    return
  }
  accepting.value = ''
  // Reconnect the panel that hit the change (terminal sessions register
  // their panel binding; tunnels and background dials have no panel here).
  if (c.panelId) {
    window.dispatchEvent(new CustomEvent('panel:reconnect', { detail: { panelId: c.panelId } }))
  }
  closeCurrent()
}

onMounted(() => {
  unlisten = Events.On('session:hostkey-changed', onEvent)
})

onBeforeUnmount(() => {
  unlisten?.()
})
</script>

<style scoped>
.hk-warning {
  display: flex;
  align-items: flex-start;
  gap: 0.5rem;
  margin-bottom: 1rem;
  line-height: 1.5;
  color: var(--text-primary);
}
.hk-warning-icon {
  flex-shrink: 0;
  margin-top: 0.125rem;
  color: var(--warning, #e6a23c);
}
.hk-rows {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  padding: 0.75rem 1rem;
  background: var(--bg-hover, var(--bg-base));
  border-radius: 6px;
}
.hk-row {
  display: flex;
  align-items: baseline;
  gap: 1rem;
}
.hk-label {
  flex-shrink: 0;
  width: 6.5rem;
  text-align: right;
  color: var(--text-secondary);
  font-size: 0.8125rem;
}
.hk-value {
  font-size: 0.8125rem;
  word-break: break-all;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.hk-fp {
  cursor: copy;
}
</style>
