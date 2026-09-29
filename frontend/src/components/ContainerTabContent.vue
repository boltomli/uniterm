<template>
  <div class="container-tab">
    <div v-if="session?.error" class="container-error">{{ session.error }}</div>
    <div v-else-if="!session || session.loading" class="container-loading">{{ t('container.loading') }}</div>

    <div v-else class="db-main">
      <div class="db-left" :style="{ width: leftWidth + 'px' }">
        <ContainerTree v-model="view" />
      </div>
      <div class="db-resizer" @mousedown="onResizeStart" />
      <div class="db-right">
        <OverviewView
          v-if="view === 'overview'"
          :tab="tab"
          :session="session"
          @show-state="onShowState"
          @show-images="view = 'images'"
        />
        <!-- v-show 常驻：切换视图不销毁组件，进行中的拉取/推送任务面板跨视图存活 -->
        <ContainersView
          v-show="view === 'containers'"
          :tab="tab"
          :session="session"
          :image-filter="imageFilter"
          :state-filter="stateFilter"
          @clear-image-filter="imageFilter = ''"
          @clear-state-filter="stateFilter = ''"
        />
        <ImagesView
          v-show="view === 'images'"
          :tab="tab"
          :session="session"
          @view-related="onViewRelated"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useContainerStore } from '../stores/containerStore'
import { useConnectionStore } from '../stores/connectionStore'
import { useTunnelCredentials } from '../composables/useTunnelCredentials'
import { useI18n } from '../i18n'
import ContainerTree from './ContainerTree.vue'
import OverviewView from './OverviewView.vue'
import ContainersView from './ContainersView.vue'
import ImagesView from './ImagesView.vue'
import type { ContainerTab } from '../types/container'

const props = defineProps<{ tab: ContainerTab }>()

const { t } = useI18n()
const store = useContainerStore()
const connectionStore = useConnectionStore()
const { ensureConnectionCredentials } = useTunnelCredentials()
const session = computed(() => store.sessions[props.tab.id])

// SSH 传输的容器连接先走统一的凭据补全（含跳板机链），被引用的 SSH 主机
// 未保存密码时弹窗补全而不是直接报认证错误；取消则中止连接。无需补全时
// ensureConnectionCredentials 原样返回，不影响正常连接。
async function openWithCredentials() {
  const conn = connectionStore.connections.find(c => c.id === props.tab.connectionId)
  if (conn && conn.containerTransport === 'ssh' && conn.containerSSHConnId) {
    const sshCfg = connectionStore.connections.find(c => c.id === conn.containerSSHConnId)
    if (sshCfg) {
      const resolved = await ensureConnectionCredentials(sshCfg)
      if (!resolved) {
        await store.open(props.tab, { error: t('credential.cancelled') })
        return
      }
      await store.open(props.tab, {
        sshUser: resolved.user,
        sshPassword: resolved.password,
        tunnelUser: resolved.tunnelSSHUser,
        tunnelPassword: resolved.tunnelSSHPassword,
      })
      return
    }
  }
  await store.open(props.tab)
}

// ── view switch（左树：总览分组 + 概览/容器/镜像）──────────────
const view = ref<'overview' | 'containers' | 'images'>('overview')
const imageFilter = ref('')
const stateFilter = ref('')

// 镜像列表「关联容器」点击：切到容器页并按镜像过滤。
function onViewRelated(ref: string) {
  imageFilter.value = ref
  stateFilter.value = ''
  view.value = 'containers'
}

// 概览页状态统计点击：切到容器页并按状态过滤。
function onShowState(state: string) {
  stateFilter.value = state
  imageFilter.value = ''
  view.value = 'containers'
}

// ── 左侧宽度 + resizer（抄 K8sTabContent / DBTabContent）───────
const leftWidth = ref(180)
let resizeStartX = 0
let resizeStartWidth = 0
let resizing = false
function onResizeStart(e: MouseEvent) {
  resizeStartX = e.clientX
  resizeStartWidth = leftWidth.value
  resizing = true
  document.addEventListener('mousemove', onResizeMove)
  document.addEventListener('mouseup', onResizeEnd)
}
function onResizeMove(e: MouseEvent) {
  const dx = e.clientX - resizeStartX
  leftWidth.value = Math.max(120, Math.min(360, resizeStartWidth + dx))
}
function onResizeEnd() {
  resizing = false
  document.removeEventListener('mousemove', onResizeMove)
  document.removeEventListener('mouseup', onResizeEnd)
}

function onReconnectEvent(e: Event) {
  const panelId = (e as CustomEvent)?.detail?.panelId
  if (panelId && panelId === props.tab.panelId) reconnect()
}

// Force-reconnect: close the current container connection, then re-open it
// (open() re-initializes loading/error state and reconnects the client).
async function reconnect() {
  try { store.close(props.tab.id) } catch (_) {}
  await openWithCredentials()
}

onMounted(() => {
  openWithCredentials()
  // Tab right-click 「重连」(Reconnect) menu → forced reconnect of this tab.
  window.addEventListener('panel:reconnect', onReconnectEvent)
})
onBeforeUnmount(() => {
  window.removeEventListener('panel:reconnect', onReconnectEvent)
  if (resizing) {
    document.removeEventListener('mousemove', onResizeMove)
    document.removeEventListener('mouseup', onResizeEnd)
  }
  store.close(props.tab.id)
})
</script>

<style scoped>
/* 直接抄 K8sTabContent 的骨架 CSS，class 同名 */
.container-tab {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  position: relative;
}

.db-main {
  flex: 1;
  display: flex;
  overflow: hidden;
}
.db-left {
  flex-shrink: 0;
  border-right: 1px solid var(--border-subtle, #333);
  overflow: hidden;
}
.db-resizer {
  width: 0.25rem;
  cursor: col-resize;
  background: transparent;
  flex-shrink: 0;
  transition: background 0.15s ease;
}
.db-resizer:hover {
  background: var(--border-subtle, #333);
}
.db-right {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.container-error {
  color: var(--el-color-danger, #f56);
  padding: 0.75rem;
  white-space: pre-wrap;
}

.container-loading {
  padding: 0.75rem;
  opacity: 0.7;
}
</style>
