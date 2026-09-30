<template>
  <div class="view-wrap">
    <div class="container-toolbar">
      <span class="overview-title">{{ t('container.overview') }}</span>
      <el-select
        v-if="session?.runtime === 'nerdctl'"
        :model-value="session.namespace"
        size="small"
        style="width: 8.75rem"
        filterable
        allow-create
        @change="onNamespaceChange"
      >
        <el-option v-for="ns in namespaceOptions" :key="ns" :label="ns" :value="ns" />
      </el-select>
      <div class="toolbar-spacer" />
      <el-button size="small" :icon="RefreshCw" @click="loadAll" />
    </div>

    <div class="overview-body">
      <!-- 运行时信息拆三块：运行时/主机/存储 -->
      <div class="ov-info-grid">
        <div class="ov-card">
          <div class="ov-card-title">{{ t('container.overviewRuntime') }}</div>
          <div class="ov-facts">
            <div v-for="f in runtimeFields" :key="f[0]" class="ov-fact">
              <span class="ov-fact-label">{{ f[0] }}</span>
              <span class="ov-fact-value">{{ f[1] || '—' }}</span>
            </div>
          </div>
        </div>
        <div class="ov-card">
          <div class="ov-card-title">{{ t('container.overviewHost') }}</div>
          <div class="ov-facts">
            <div v-for="f in hostFields" :key="f[0]" class="ov-fact">
              <span class="ov-fact-label">{{ f[0] }}</span>
              <span class="ov-fact-value">{{ f[1] || '—' }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 统计：容器 / 镜像 两列 -->
      <div class="ov-stats">
        <div class="ov-card">
          <div class="ov-card-title">{{ t('container.overviewContainers') }}</div>
          <div class="ov-row ov-clickable" @click="$emit('show-state', '')">
            <span class="ov-label">{{ t('container.fTotal') }}</span>
            <span class="ov-value ov-count">{{ containers.length }}</span>
          </div>
          <div
            v-for="st in stateCounts"
            :key="st.state"
            class="ov-row ov-clickable"
            @click="$emit('show-state', st.state)"
          >
            <span class="ov-label"><span class="state-dot" :data-state="st.state" />{{ st.state }}</span>
            <span class="ov-value ov-count">{{ st.count }}</span>
          </div>
          <div v-if="!containers.length" class="ov-empty">—</div>
        </div>

        <div class="ov-card">
          <div class="ov-card-title">{{ t('container.overviewImages') }}</div>
          <div class="ov-row ov-clickable" @click="$emit('show-images')">
            <span class="ov-label">{{ t('container.fTotal') }}</span>
            <span class="ov-value ov-count">{{ images.length }}</span>
          </div>
          <div class="ov-row ov-clickable" @click="$emit('show-images')">
            <span class="ov-label">{{ t('container.tagged') }}</span>
            <span class="ov-value ov-count">{{ taggedCount }}</span>
          </div>
          <div class="ov-row ov-clickable" @click="$emit('show-images')">
            <span class="ov-label">{{ t('container.dangling') }}</span>
            <span class="ov-value ov-count">{{ danglingCount }}</span>
          </div>
          <div v-if="!images.length" class="ov-empty">—</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { RefreshCw } from '@lucide/vue'
import { useContainerStore } from '../stores/containerStore'
import { useI18n } from '../i18n'
import type { RuntimeInfo } from '../types/container'
import type { ContainerSession } from '../stores/containerStore'
import type { ContainerTab } from '../types/container'

const props = defineProps<{ tab: ContainerTab; session: ContainerSession | undefined }>()
defineEmits<{ (e: 'show-state', state: string): void; (e: 'show-images'): void }>()

const { t } = useI18n()
const store = useContainerStore()

const containers = computed(() => props.session?.containers || [])
const images = computed(() => props.session?.images || [])

const runtimeFields = computed<[string, string][]>(() => {
  const s = props.session
  const info = s?.info
  return [
    [t('container.fRuntime'), s?.runtime || ''],
    [t('container.fClientVersion'), withComponent(info?.clientVersion, info?.clientComponent)],
    [t('container.fServerVersion'), withComponent(info?.serverVersion, info?.serverComponent)],
    [t('container.fDriver'), info?.driver || ''],
    [t('container.fCgroup'), cgroupText(info)],
  ]
})

// cgroup 驱动与版本合并展示，如 "cgroupfs v1"。
function cgroupText(info: RuntimeInfo | undefined | null) {
  if (!info) return ''
  return [info.cgroupDriver, info.cgroupVersion].filter(Boolean).join(' ')
}

const hostFields = computed<[string, string][]>(() => {
  const info = props.session?.info
  return [
    [t('container.fOs'), info?.os || ''],
    [t('container.fArch'), info?.arch || ''],
    [t('container.fKernel'), info?.kernelVersion || ''],
    [t('container.fCpus'), info?.ncpu || ''],
    [t('container.fMem'), info?.memTotal || ''],
  ]
})

const stateCounts = computed(() => {
  const map = new Map<string, number>()
  for (const c of containers.value) {
    map.set(c.state, (map.get(c.state) || 0) + 1)
  }
  return [...map.entries()]
    .map(([state, count]) => ({ state, count }))
    .sort((a, b) => b.count - a.count)
})

const taggedCount = computed(() => images.value.filter(i => i.repository !== '<none>').length)
const danglingCount = computed(() => images.value.filter(i => i.repository === '<none>').length)

// 版本后缀组件名：如 "v2.2.1 (nerdctl)"、"v1.7.13 (containerd)"。
function withComponent(version: string | undefined, component: string | undefined) {
  if (!version) return ''
  return component ? `${version} (${component})` : version
}

// 首次进入概览：并行拉取容器/镜像/运行时信息，减少 docker CLI 串行等待。
function loadAll() {
  const id = props.tab.id
  void Promise.all([store.refresh(id), store.loadImages(id), store.loadInfo(id)])
}

onMounted(loadAll)

// ── nerdctl namespace 切换（与容器/镜像视图一致）───────────────
const namespaceOptions = computed(() => {
  const s = props.session
  if (!s) return []
  const set = new Set(s.namespaces.length ? s.namespaces : [s.namespace])
  set.add('default')
  return [...set]
})

async function onNamespaceChange(ns: string) {
  try {
    await store.setNamespace(props.tab.id, ns)
    loadAll()
  } catch (e: any) {
    ElMessage.error(String(e?.message || e))
  }
}
</script>

<style scoped>
.view-wrap {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.container-toolbar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.375rem 0.625rem;
  border-bottom: 1px solid var(--el-border-color-lighter, #333);
  flex-shrink: 0;
}

.overview-title {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--text-primary);
}

.toolbar-spacer {
  flex: 1;
}

.overview-body {
  flex: 1;
  overflow: auto;
  padding: 1rem 1.25rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.ov-card {
  background: var(--bg-elevated);
  border: 1px solid var(--border-subtle, #333);
  border-radius: var(--radius-sm);
  padding: 1rem 1.25rem;
}

.ov-card-title {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 0.75rem;
}

/* 运行时信息：字段分栏，纵向排 label/value，呼吸感更好 */
.ov-facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
  gap: 0.875rem 1.5rem;
}

.ov-fact {
  display: flex;
  flex-direction: column;
  gap: 0.1875rem;
  min-width: 0;
}

.ov-fact-label {
  font-family: var(--font-ui);
  font-size: 0.6875rem;
  color: var(--text-muted);
}

.ov-fact-value {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--text-primary);
  word-break: break-all;
  user-select: text;
}

.ov-info-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr));
  gap: 1rem;
  align-items: start;
}

/* 统计区：容器/镜像两列 */
.ov-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(18rem, 1fr));
  gap: 1rem;
  align-items: start;
}

.ov-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.4375rem 0;
}

.ov-clickable {
  border-radius: var(--radius-sm);
  padding: 0.4375rem 0.5rem;
  margin: 0 -0.5rem;
  transition: background 0.12s ease;
}
.ov-clickable:hover {
  background: var(--bg-hover);
}

.ov-label {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  color: var(--text-muted);
  display: flex;
  align-items: center;
  gap: 0.4375rem;
}

.ov-value {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--text-primary);
  word-break: break-all;
  user-select: text;
  text-align: right;
}

.ov-count {
  font-size: 0.8125rem;
  font-weight: 600;
}

.ov-empty {
  padding: 0.375rem 0;
  color: var(--text-muted);
  font-size: 0.75rem;
}

.state-dot {
  width: 0.5rem;
  height: 0.5rem;
  border-radius: 50%;
  background: var(--text-muted);
  flex-shrink: 0;
}
.state-dot[data-state='running'] {
  background: var(--el-color-success);
}
.state-dot[data-state='paused'] {
  background: var(--el-color-warning);
}
.state-dot[data-state='exited'] {
  background: var(--el-color-info);
}
</style>
