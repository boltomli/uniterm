<template>
  <div class="k8s-overview">
    <!-- KPI 行：小 stat tile（对齐 ES 总览 info-grid 风格） -->
    <div class="ov-tiles">
      <div class="ov-tile" :title="props.contextName">
        <span class="ov-tile-label">{{ t('k8s.overviewCluster') }}</span>
        <span class="ov-tile-value ov-tile-value-sm">{{ version || '—' }}</span>
      </div>
      <div class="ov-tile ov-clickable" @click="$emit('navigate', 'nodes')">
        <span class="ov-tile-label">{{ t('k8s.overviewNodes') }}</span>
        <span class="ov-tile-value">{{ nodes.length }}</span>
        <span class="ov-tile-status" :class="{ 'ov-tile-status-warn': nodesNotReady > 0 }">
          {{ nodesNotReady ? t('k8s.overviewNotReadyCount', { n: nodesNotReady }) : t('k8s.overviewAllReady') }}
        </span>
      </div>
      <div class="ov-tile ov-clickable" @click="$emit('navigate', 'pods')">
        <span class="ov-tile-label">{{ labelOf('pods') }}</span>
        <span class="ov-tile-value">{{ podsTotal ?? '—' }}</span>
        <span class="ov-tile-status" :class="{ 'ov-tile-status-warn': podsNotReady > 0 }">
          {{ podsTotal == null ? '' : (podsNotReady ? t('k8s.overviewNotReadyCount', { n: podsNotReady }) : t('k8s.overviewAllReady')) }}
        </span>
      </div>
      <div class="ov-tile ov-clickable" @click="$emit('navigate', 'namespaces')">
        <span class="ov-tile-label">{{ t('k8s.overviewNamespaces') }}</span>
        <span class="ov-tile-value">{{ nsCount ?? '—' }}</span>
      </div>
    </div>

    <!-- 资源用量：CPU / 内存各一张卡，使用/请求两行带百分比条；点击进节点列表看明细 -->
    <div class="ov-usage-grid">
      <div class="ov-card ov-clickable" @click="$emit('navigate', 'nodes')">
        <div class="ov-card-title">{{ t('k8s.overviewCpu') }}</div>
        <div class="ov-meter-row">
          <span class="ov-meter-label">{{ t('k8s.overviewUsageLabel') }}</span>
          <div class="ov-meter" :class="'m-' + sev(cpuPct)"><div class="ov-meter-fill" :class="sev(cpuPct)" :style="{ width: cpuPct + '%' }" /></div>
          <span class="ov-meter-pct">{{ nodeUsage ? cpuPct + '%' : '—' }}</span>
          <span class="ov-meter-value">{{ nodeUsage ? `${formatCpu(cpuUsed)} / ${formatCpu(cpuAlloc)}` : '—' }}</span>
        </div>
        <div class="ov-meter-row">
          <span class="ov-meter-label">{{ t('k8s.overviewRequestsLabel') }}</span>
          <div class="ov-meter" :class="'m-' + sev(cpuReqPct)"><div class="ov-meter-fill" :class="sev(cpuReqPct)" :style="{ width: cpuReqPct + '%' }" /></div>
          <span class="ov-meter-pct">{{ cpuAlloc ? cpuReqPct + '%' : '—' }}</span>
          <span class="ov-meter-value">{{ cpuAlloc ? `${formatCpu(cpuReq)} / ${formatCpu(cpuAlloc)}` : '—' }}</span>
        </div>
      </div>

      <div class="ov-card ov-clickable" @click="$emit('navigate', 'nodes')">
        <div class="ov-card-title">{{ t('k8s.overviewMemory') }}</div>
        <div class="ov-meter-row">
          <span class="ov-meter-label">{{ t('k8s.overviewUsageLabel') }}</span>
          <div class="ov-meter" :class="'m-' + sev(memPct)"><div class="ov-meter-fill" :class="sev(memPct)" :style="{ width: memPct + '%' }" /></div>
          <span class="ov-meter-pct">{{ nodeUsage ? memPct + '%' : '—' }}</span>
          <span class="ov-meter-value">{{ nodeUsage ? `${formatMemory(memUsed)} / ${formatMemory(memAlloc)}` : '—' }}</span>
        </div>
        <div class="ov-meter-row">
          <span class="ov-meter-label">{{ t('k8s.overviewRequestsLabel') }}</span>
          <div class="ov-meter" :class="'m-' + sev(memReqPct)"><div class="ov-meter-fill" :class="sev(memReqPct)" :style="{ width: memReqPct + '%' }" /></div>
          <span class="ov-meter-pct">{{ memAlloc ? memReqPct + '%' : '—' }}</span>
          <span class="ov-meter-value">{{ memAlloc ? `${formatMemory(memReq)} / ${formatMemory(memAlloc)}` : '—' }}</span>
        </div>
      </div>
    </div>

    <!-- 四张等宽小卡：Pod 状态 / 节点 / 工作负载 / 存储与配置 -->
    <div class="ov-grid4">
      <div class="ov-card">
        <div class="ov-card-title">{{ t('k8s.overviewPodStatus') }}</div>
        <div
          v-for="s in podStatusRows"
          :key="s.phase"
          class="ov-row ov-clickable"
          @click="$emit('navigate', 'pods')"
        >
          <span class="ov-label">
            <span class="state-dot" :data-state="s.tone" />
            {{ s.phase }}
          </span>
          <span class="ov-value ov-count">{{ s.count }}</span>
        </div>
      </div>

      <div class="ov-card">
        <div class="ov-card-title">{{ t('k8s.overviewWorkloads') }}</div>
        <div
          v-for="c in workloadCounts"
          :key="c.key"
          class="ov-row ov-clickable"
          @click="$emit('navigate', c.key)"
        >
          <span class="ov-label">{{ c.label }}</span>
          <span class="ov-value ov-count">{{ c.count }}</span>
        </div>
      </div>

      <div class="ov-card">
        <div class="ov-card-title">{{ t('k8s.overviewNetwork') }}</div>
        <div
          v-for="c in networkCounts"
          :key="c.key"
          class="ov-row ov-clickable"
          @click="$emit('navigate', c.key)"
        >
          <span class="ov-label">{{ c.label }}</span>
          <span class="ov-value ov-count">{{ c.count }}</span>
        </div>
      </div>

      <div class="ov-card">
        <div class="ov-card-title">{{ t('k8s.overviewStorage') }}</div>
        <div
          v-for="c in storageCounts"
          :key="c.key"
          class="ov-row ov-clickable"
          @click="$emit('navigate', c.key)"
        >
          <span class="ov-label">{{ c.label }}</span>
          <span class="ov-value ov-count">{{ c.count }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import * as k8sClient from '../services/k8sClient'
import { RESOURCES } from '../services/k8sResources'
import { fetchNodeMetrics, type Usage } from '../services/k8sMetrics'
import { formatCpu, formatMemory, parseCpu, parseMemory } from '../services/k8sQuantity'
import { useI18n } from '../i18n'

// k8s 总览：KPI tile 行 + 资源用量 meter（使用/请求 vs allocatable）+
// 分组小卡 + Warning 事件。数据全部走现有 REST / metrics.k8s.io 通道，
// 30s 自动刷新，点击各处跳转对应资源列表。
const props = defineProps<{ connId: string; contextName: string }>()
defineEmits<{ (e: 'navigate', resourceKey: string, nameFilter?: string): void }>()

const { t } = useI18n()

const version = ref('')
const nodes = ref<{ name: string; ready: boolean; roles: string[]; allocCpu: number; allocMem: number }[]>([])
const nodeUsage = ref<Map<string, Usage> | null>(null)
const podStats = ref({ running: 0, pending: 0, succeeded: 0, failed: 0, unknown: 0 })
const podsTotal = ref<number | null>(null)
const podRequests = ref({ cpu: 0, mem: 0 })
const podsNotReady = ref(0)
const workloads = ref<{ key: string; label: string; count: number }[]>([])
const networkCounts = ref<{ key: string; label: string; count: number }[]>([])
const storageCounts = ref<{ key: string; label: string; count: number }[]>([])
const nsCount = ref<number | null>(null)

const nodesReady = computed(() => nodes.value.filter(n => n.ready).length)
const nodesNotReady = computed(() => nodes.value.length - nodesReady.value)

const labelOf = (key: string) => RESOURCES.find(r => r.key === key)?.label || key

// ── 资源用量（allocatable 汇总 + metrics 实时用量 + pods requests 汇总）──
const cpuAlloc = computed(() => nodes.value.reduce((s, n) => s + n.allocCpu, 0))
const memAlloc = computed(() => nodes.value.reduce((s, n) => s + n.allocMem, 0))
const cpuUsed = computed(() => nodeUsage.value ? Array.from(nodeUsage.value.values()).reduce((s, u) => s + u.cpu, 0) : 0)
const memUsed = computed(() => nodeUsage.value ? Array.from(nodeUsage.value.values()).reduce((s, u) => s + u.mem, 0) : 0)
const cpuReq = computed(() => podRequests.value.cpu)
const memReq = computed(() => podRequests.value.mem)

function pctOf(used: number, total: number): number {
  return total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0
}
const cpuPct = computed(() => pctOf(cpuUsed.value, cpuAlloc.value))
const memPct = computed(() => pctOf(memUsed.value, memAlloc.value))
const cpuReqPct = computed(() => pctOf(cpuReq.value, cpuAlloc.value))
const memReqPct = computed(() => pctOf(memReq.value, memAlloc.value))

// meter 严重度：>=90% 危险、>=70% 警告，其余正常（accent 色）
function sev(p: number): string {
  return p >= 90 ? 'danger' : p >= 70 ? 'warn' : 'ok'
}

// ── Pod 状态分布 ────────────────────────────────────────────────
const podTotal = computed(() => Object.values(podStats.value).reduce((a, b) => a + b, 0))
const podStatusRows = computed(() => [
  { phase: 'Running', tone: 'running', count: podStats.value.running },
  { phase: 'Pending', tone: 'pending', count: podStats.value.pending },
  { phase: 'Succeeded', tone: 'succeeded', count: podStats.value.succeeded },
  { phase: 'Failed', tone: 'failed', count: podStats.value.failed },
  { phase: 'Unknown', tone: 'unknown', count: podStats.value.unknown },
])

const workloadCounts = computed(() => [
  { key: 'pods', label: labelOf('pods'), count: podsTotal.value ?? podTotal.value },
  ...workloads.value,
])

async function requestCount(path: string): Promise<number | null> {
  try {
    const { status, data } = await k8sClient.requestJSON<any>(props.connId, 'GET', path)
    if (status !== 200) return null
    const remaining = data?.metadata?.remainingItemCount
    const base = (data?.items || []).length
    return typeof remaining === 'number' ? base + remaining : base
  } catch {
    return null
  }
}

async function loadAll() {
  if (!props.connId) return
  try {
    const ver = await k8sClient.requestJSON<any>(props.connId, 'GET', '/version')
    if (ver.status === 200) version.value = ver.data?.gitVersion || ''
  } catch { /* 概览允许缺数据 */ }

  try {
    const { status, data } = await k8sClient.requestJSON<any>(props.connId, 'GET', '/api/v1/nodes')
    if (status === 200 && data?.items) {
      nodes.value = data.items.map((n: any) => {
        const cond = (n.status?.conditions || []).find((c: any) => c.type === 'Ready')
        const roles = Object.keys(n.metadata?.labels || {})
          .filter(l => l.startsWith('node-role.kubernetes.io/'))
          .map(l => l.slice('node-role.kubernetes.io/'.length))
        return {
          name: n.metadata?.name || '',
          ready: cond?.status === 'True',
          roles,
          allocCpu: parseCpu(n.status?.allocatable?.cpu || ''),
          allocMem: parseMemory(n.status?.allocatable?.memory || ''),
        }
      })
    }
  } catch { /* keep previous */ }

  try {
    nodeUsage.value = await fetchNodeMetrics(props.connId)
  } catch { nodeUsage.value = null }

  // pods 一次请求同时出计数、状态分布和 requests 汇总
  try {
    const { status, data } = await k8sClient.requestJSON<any>(props.connId, 'GET', '/api/v1/pods?limit=500')
    if (status === 200) {
      const items: any[] = data?.items || []
      const remaining = typeof data?.metadata?.remainingItemCount === 'number' ? data.metadata.remainingItemCount : 0
      podsTotal.value = items.length + remaining
      const stats = { running: 0, pending: 0, succeeded: 0, failed: 0, unknown: 0 }
      let reqCpu = 0, reqMem = 0, notReady = 0
      for (const p of items) {
        // K8s phase 首字母大写（Running/Pending/...），统计表 key 为小写
        const phase = typeof p.status?.phase === 'string' ? p.status.phase.toLowerCase() as keyof typeof stats : undefined
        if (phase && phase in stats) stats[phase]++
        else stats.unknown++
        // 未就绪 = Ready condition 非 True（Pending/Failed/Running 但容器未就绪）
        if (p.status?.conditions?.find((c: any) => c.type === 'Ready')?.status !== 'True') notReady++
        for (const c of p.spec?.containers || []) {
          reqCpu += parseCpu(c.resources?.requests?.cpu || '')
          reqMem += parseMemory(c.resources?.requests?.memory || '')
        }
      }
      podStats.value = stats // 超过 500 时状态分布/requests 只统计首页
      podRequests.value = { cpu: reqCpu, mem: reqMem }
      podsNotReady.value = notReady
    }
  } catch { /* keep previous */ }

  const otherDefs: { key: string; path: string }[] = [
    { key: 'deployments', path: '/apis/apps/v1/deployments?limit=500' },
    { key: 'statefulsets', path: '/apis/apps/v1/statefulsets?limit=500' },
    { key: 'daemonsets', path: '/apis/apps/v1/daemonsets?limit=500' },
  ]
  const wl = await Promise.all(otherDefs.map(async d => ({ key: d.key, label: labelOf(d.key), count: await requestCount(d.path) })))
  workloads.value = wl.filter(r => r.count !== null).map(r => ({ ...r, count: r.count as number }))

  const networkDefs: { key: string; path: string }[] = [
    { key: 'services', path: '/api/v1/services?limit=500' },
    { key: 'ingresses', path: '/apis/networking.k8s.io/v1/ingresses?limit=500' },
    { key: 'endpoints', path: '/api/v1/endpoints?limit=500' },
  ]
  const nw = await Promise.all(networkDefs.map(async d => ({ key: d.key, label: labelOf(d.key), count: await requestCount(d.path) })))
  networkCounts.value = nw.filter(r => r.count !== null).map(r => ({ ...r, count: r.count as number }))

  const storageDefs: { key: string; path: string }[] = [
    { key: 'persistentvolumeclaims', path: '/api/v1/persistentvolumeclaims?limit=500' },
    { key: 'persistentvolumes', path: '/api/v1/persistentvolumes?limit=500' },
    { key: 'configmaps', path: '/api/v1/configmaps?limit=500' },
    { key: 'secrets', path: '/api/v1/secrets?limit=500' },
  ]
  const st = await Promise.all(storageDefs.map(async d => ({ key: d.key, label: labelOf(d.key), count: await requestCount(d.path) })))
  storageCounts.value = st.filter(r => r.count !== null).map(r => ({ ...r, count: r.count as number }))

  nsCount.value = await requestCount('/api/v1/namespaces?limit=500')
}

let refreshTimer: number | null = null
let loading = false

onMounted(() => {
  loadAll().catch(e => ElMessage.error(String(e?.message || e)))
  refreshTimer = window.setInterval(() => {
    if (loading) return
    loading = true
    loadAll().catch(() => { /* 刷新失败静默，下个周期重试 */ }).finally(() => { loading = false })
  }, 30000)
})

onBeforeUnmount(() => {
  if (refreshTimer != null) { clearInterval(refreshTimer); refreshTimer = null }
})
</script>

<style scoped>
.k8s-overview {
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
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.ov-card-title {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 0.75rem;
}

.ov-sub-title {
  font-family: var(--font-ui);
  font-size: 0.6875rem;
  color: var(--text-muted);
  margin: 0.75rem 0 0.375rem;
}

.ov-clickable {
}

.ov-empty {
  padding: 0.375rem 0;
  color: var(--text-muted);
  font-size: 0.75rem;
}

/* ── KPI tile 行（对齐 ES info-grid：label 上、值下，等宽等高）── */
.ov-tiles {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(9.5rem, 1fr));
  gap: 1rem;
}
.ov-tile {
  background: var(--bg-elevated);
  border: 1px solid var(--border-subtle, #333);
  border-radius: var(--radius-sm);
  padding: 0.75rem 1rem;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  min-width: 0;
}
.ov-tile-label {
  font-family: var(--font-ui);
  font-size: 0.6875rem;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ov-tile-value {
  font-family: var(--font-mono);
  font-size: 1.375rem;
  font-weight: 600;
  color: var(--text-primary);
  line-height: 1.2;
}
.ov-tile-status {
  font-family: var(--font-ui);
  font-size: 0.6875rem;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ov-tile-status-warn {
  color: var(--warning);
}
/* 长文本值（版本号等）用小一号，避免撑破 tile */
.ov-tile-value-sm {
  font-size: 1rem;
}
/* ── meter：fill 按严重度变色，track 为同色浅阶（同 ramp）────── */
.ov-meter {
  flex: 1;
  height: 0.375rem;
  border-radius: var(--radius-sm);
  overflow: hidden;
  min-width: 0;
}
.ov-meter.m-ok { background: color-mix(in srgb, var(--el-color-primary) 16%, var(--bg-elevated)); }
.ov-meter.m-warn { background: color-mix(in srgb, var(--warning) 16%, var(--bg-elevated)); }
.ov-meter.m-danger { background: color-mix(in srgb, var(--el-color-danger) 16%, var(--bg-elevated)); }
.ov-meter-fill {
  height: 100%;
  border-radius: var(--radius-sm);
  transition: width 0.3s ease;
}
.ov-meter-fill.ok { background: var(--el-color-primary); }
.ov-meter-fill.warn { background: var(--warning); }
.ov-meter-fill.danger { background: var(--el-color-danger); }

/* ── 资源用量：CPU / 内存两张卡 ───────────────────────────── */
.ov-usage-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}
@media (max-width: 56rem) {
  .ov-usage-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
/* 固定四列 grid（标签/条/百分比/数值），跨行严格对齐 */
.ov-meter-row {
  display: grid;
  grid-template-columns: 4rem minmax(0, 1fr) 3rem 9rem;
  gap: 0.625rem;
  align-items: center;
  padding: 0.3125rem 0;
}
.ov-meter-label {
  font-family: var(--font-ui);
  font-size: 0.6875rem;
  color: var(--text-muted);
}
.ov-meter-pct {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--text-primary);
  text-align: right;
}
.ov-meter-value {
  font-family: var(--font-mono);
  font-size: 0.6875rem;
  color: var(--text-secondary);
  text-align: right;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ── 四张等宽小卡 ─────────────────────────────────────────── */
.ov-grid4 {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 1rem;
}
@media (max-width: 64rem) {
  .ov-grid4 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 38rem) {
  .ov-grid4 {
    grid-template-columns: minmax(0, 1fr);
  }
}

.ov-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.3125rem 0.375rem;
  border-radius: var(--radius-sm);
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
  min-width: 0;
}
.ov-value {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--text-primary);
  text-align: right;
}
.ov-count {
  font-weight: 600;
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
.state-dot[data-state='pending'] {
  background: var(--warning);
}
.state-dot[data-state='succeeded'] {
  background: var(--text-muted);
}
.state-dot[data-state='failed'] {
  background: var(--el-color-danger);
}
.state-dot[data-state='unknown'] {
  background: var(--text-muted);
  opacity: 0.5;
}
</style>
