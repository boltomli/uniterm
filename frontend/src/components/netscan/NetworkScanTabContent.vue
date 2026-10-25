<template>
  <el-dialog
    append-to-body
    :model-value="visible"
    :title="t('netscan.title')"
    width="85%"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    destroy-on-close
    class="netscan-dialog"
    style="display:flex; flex-direction:column; height:90vh; max-height:90vh; border-radius:var(--radius-sm);"
    @update:model-value="(v: boolean) => emit('update:visible', v)"
  >
    <!-- ── Content root: fixed-height flex column. A fixed height here (rather
         than relying on el-dialog's internal header/body/footer flex, whose
         :deep selectors may not resolve to the dialog root) guarantees the two
         scan sections always get a definite height to lay out inside. ── -->
    <div class="netscan-content">
    <!-- ── Full-width flat toolbar, mirroring the SFTP built-in editor ── -->
    <div class="netscan-toolbar">
      <div class="toolbar-group">
        <span class="toolbar-group-title">{{ t('netscan.ipScan.title') }}</span>
        <el-input
          v-model="ipStart"
          :placeholder="t('netscan.ipScan.startIp')"
          class="ip-input"
          :disabled="ipScanning"
          @keyup.enter="startIPScan"
        />
        <span class="toolbar-sep">→</span>
        <el-input
          v-model="ipEnd"
          :placeholder="t('netscan.ipScan.endIp')"
          class="ip-input"
          :disabled="ipScanning"
          @keyup.enter="startIPScan"
        />
        <el-button v-if="!ipScanning" type="primary" class="go-btn" @click="startIPScan">
          {{ t('netscan.ipScan.start') }}
        </el-button>
        <el-button v-else type="danger" class="go-btn" @click="stopIPScan">
          {{ t('netscan.common.stop') }}
        </el-button>
      </div>

      <span class="toolbar-divider" />

      <div class="toolbar-group">
        <span class="toolbar-group-title">{{ t('netscan.portScan.title') }}</span>
        <el-input
          v-model="portHost"
          :placeholder="t('netscan.portScan.host')"
          class="port-host-input"
          :disabled="portScanning"
          @keyup.enter="startPortScan"
        />
        <el-radio-group v-model="portMode" :disabled="portScanning" class="port-mode">
          <el-radio-button :value="'common'">{{ t('netscan.portScan.common') }}</el-radio-button>
          <el-radio-button :value="'range'">{{ t('netscan.portScan.range') }}</el-radio-button>
        </el-radio-group>
        <template v-if="portMode === 'range'">
          <el-input-number
            v-model="portStart"
            :min="1"
            :max="65535"
            :disabled="portScanning"
            controls-position="right"
            class="port-num"
          />
          <span class="toolbar-sep">→</span>
          <el-input-number
            v-model="portEnd"
            :min="1"
            :max="65535"
            :disabled="portScanning"
            controls-position="right"
            class="port-num"
          />
        </template>
        <el-button v-if="!portScanning" type="primary" class="go-btn" @click="startPortScan">
          {{ t('netscan.portScan.start') }}
        </el-button>
        <el-button v-else type="danger" class="go-btn" @click="stopPortScan">
          {{ t('netscan.common.stop') }}
        </el-button>
      </div>
    </div>

    <!-- ═══════════════ Results region: IP scan (top) / port scan (bottom) ═══════════════ -->
    <div class="netscan-body">
      <div class="scan-section">
        <div class="scan-header">
          <span class="scan-title">{{ t('netscan.ipScan.title') }}</span>
          <div v-if="ipScanning" class="scan-progress">
            <el-progress
              :percentage="ipProgressPercent"
              :status="ipProgressPercent >= 100 ? 'success' : undefined"
              :stroke-width="8"
            />
            <span class="progress-label">{{ ipDone }} / {{ ipTotal }}</span>
          </div>
        </div>
        <div class="table-wrap">
          <el-table
            :data="ipHosts"
            size="small"
            border
            class="scan-table"
            @row-click="onHostRowClick"
            @row-contextmenu="onIpCtxMenu"
          >
            <el-table-column prop="ip" :label="t('netscan.ipScan.columns.ip')" :width="uiPx(150)" sortable :sort-method="compareIp">
              <template #default="{ row }">
                <span class="host-ip">{{ row.ip }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="mac" :label="t('netscan.ipScan.columns.mac')" :width="uiPx(140)">
              <template #default="{ row }">{{ row.mac || '-' }}</template>
            </el-table-column>
            <el-table-column prop="hostname" :label="t('netscan.ipScan.columns.hostname')" :width="uiPx(140)" show-overflow-tooltip>
              <template #default="{ row }">{{ row.hostname || '-' }}</template>
            </el-table-column>
            <el-table-column
              v-for="sp in serviceColumns"
              :key="sp.port"
              :label="`${sp.port} (${sp.name})`"
              align="center"
              :width="sp.name === 'telnet' ? uiPx(120) : uiPx(100)"
            >
              <template #default="{ row }">
                <el-tag :type="row.ports && row.ports[sp.port] ? 'success' : 'info'" size="small" effect="plain">
                  {{ row.ports && row.ports[sp.port] ? t('netscan.ipScan.open') : t('netscan.ipScan.closed') }}
                </el-tag>
              </template>
            </el-table-column>
          </el-table>
          <div v-if="!ipScanning && ipHosts.length === 0 && !ipStarted" class="scan-hint">
            {{ t('netscan.ipScan.ipHint') }}
          </div>
          <div v-if="!ipScanning && ipHosts.length === 0 && ipStarted" class="scan-hint">
            {{ t('netscan.ipScan.noHosts') }}
          </div>
        </div>
      </div>

      <div class="scan-section">
        <div class="scan-header">
          <span class="scan-title">{{ t('netscan.portScan.title') }}</span>
          <div v-if="portScanning" class="scan-progress">
            <el-progress
              :percentage="portProgressPercent"
              :status="portProgressPercent >= 100 ? 'success' : undefined"
              :stroke-width="8"
            />
            <span class="progress-label">{{ portDone }} / {{ portTotal }}</span>
          </div>
        </div>
        <div class="table-wrap">
          <el-table
            :data="portResults"
            size="small"
            border
            class="scan-table"
            @row-contextmenu="onPortCtxMenu"
          >
            <el-table-column prop="host" :label="t('netscan.portScan.columns.host')" :width="uiPx(150)">
              <template #default="{ row }">
                <span class="host-ip">{{ row.host }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="port" :label="t('netscan.portScan.columns.port')" sortable :width="uiPx(90)">
              <template #default="{ row }">
                <span class="port-num-text">#{{ row.port }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="service" :label="t('netscan.portScan.columns.service')" :width="uiPx(160)">
              <template #default="{ row }">{{ row.service || '-' }}</template>
            </el-table-column>
            <el-table-column :label="t('netscan.portScan.columns.state')" :width="uiPx(160)">
              <template #default="{ row }">
                <el-tag type="success" size="small">{{ row.state }}</el-tag>
              </template>
            </el-table-column>
          </el-table>
          <div v-if="!portScanning && portResults.length === 0" class="scan-hint">
            {{ t('netscan.portScan.portHint') }}
          </div>
        </div>
      </div>
    </div>
    </div>
    <!-- end .netscan-content -->

    <!-- ── Right-click export context menu (for both result tables) ── -->
    <Teleport to="body">
      <ul
        v-if="ctxMenu.visible"
        class="ctx-menu"
        :style="{ left: ctxMenu.x + 'px', top: ctxMenu.y + 'px' }"
        @contextmenu.prevent
        @click="hideCtxMenu"
      >
        <li class="ctx-item" @click.stop="copyResults(ctxMenu.kind)">
          <span class="ctx-label">{{ t('netscan.export.copy') }}</span>
        </li>
        <li class="ctx-item" @click.stop="exportResults(ctxMenu.kind)">
          <span class="ctx-label">{{ t('netscan.export.results') }}</span>
        </li>
      </ul>
    </Teleport>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Events } from '@wailsio/runtime'
import { StartIPScan, StopIPScan, StartPortScan, StopPortScan, SaveFileDialogFiltered, WriteFileBase64 } from '../../../bindings/github.com/ys-ll/uniterm/app'
import { msg } from '../../services/message'
import { useI18n } from '../../i18n'
import { uiPx } from '../../utils/uiScale'
import { utf8ToBase64 } from '../../utils/base64'

defineOptions({ name: 'NetworkScanTabContent' })

const props = defineProps<{ visible?: boolean }>()
const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
}>()

const { t } = useI18n()

// Service columns probed per host by the Go backend (ipscan.go ServicePorts).
const serviceColumns = [
  { port: 22, name: 'ssh' },
  { port: 23, name: 'telnet' },
  { port: 3389, name: 'rdp' },
  { port: 5900, name: 'vnc' },
  { port: 21, name: 'ftp' },
  { port: 445, name: 'smb' },
  { port: 80, name: 'http' }
]

// ── IP scan state ──
const ipStart = ref('')
const ipEnd = ref('')
const ipScanning = ref(false)
const ipStarted = ref(false)
const ipScanId = ref('')
const ipDone = ref(0)
const ipTotal = ref(0)
// keyed by ip so re-emitted hosts (with MAC filled) upsert instead of duplicating
const ipHosts = ref<any[]>([])

// ── Port scan state ──
const portHost = ref('')
const portMode = ref<'common' | 'range'>('common')
const portStart = ref(1)
const portEnd = ref(1000)
const portScanning = ref(false)
const portScanId = ref('')
const portDone = ref(0)
const portTotal = ref(0)
const portResults = ref<any[]>([])

// The set of scan ids this component has started. All netscan events are
// global (any open tab receives them), so we filter by ownership before
// mutating any state.
const ownedScans = new Set<string>()
// scanId -> host, to label port results when scanning multiple targets.
const portScanTargets = new Map<string, string>()

const ipProgressPercent = computed(() => ipTotal.value > 0 ? Math.round((ipDone.value / ipTotal.value) * 100) : 0)
const portProgressPercent = computed(() => portTotal.value > 0 ? Math.round((portDone.value / portTotal.value) * 100) : 0)

// Compare two IPv4 strings segment-by-segment by numeric value, so that
// 192.168.1.10 sorts after 192.168.1.2 instead of lexicographically first.
function compareIp(a: { ip: string }, b: { ip: string }): number {
  const pa = (a.ip || '').split('.').map(Number)
  const pb = (b.ip || '').split('.').map(Number)
  for (let i = 0; i < 4; i++) {
    const diff = (pa[i] || 0) - (pb[i] || 0)
    if (diff !== 0) return diff
  }
  return 0
}

async function startIPScan() {
  const start = ipStart.value.trim()
  const end = ipEnd.value.trim() || start
  if (!start) {
    msg.warning(t('netscan.ipScan.missingStart'))
    return
  }
  try {
    const id = await StartIPScan(start, end)
    ownedScans.add(id)
    ipScanId.value = id
    ipStarted.value = true
    ipScanning.value = true
    ipHosts.value = []
    ipDone.value = 0
    ipTotal.value = 0
  } catch (e: any) {
    msg.error(e?.message || t('netscan.ipScan.startFailed'))
  }
}

function stopIPScan() {
  if (ipScanId.value) StopIPScan(ipScanId.value)
}

async function startPortScan() {
  const host = portHost.value.trim()
  if (!host) {
    msg.warning(t('netscan.portScan.missingHost'))
    return
  }
  const commonOnly = portMode.value === 'common'
  const startPort = portStart.value || 1
  const endPort = portEnd.value || 65535
  try {
    const id = await StartPortScan(host, commonOnly, startPort, endPort)
    ownedScans.add(id)
    portScanId.value = id
    portScanTargets.set(id, host)
    portScanning.value = true
    // Fresh result set each run — replace, never append to stale data.
    portResults.value = []
    portDone.value = 0
    portTotal.value = 0
  } catch (e: any) {
    msg.error(e?.message || t('netscan.portScan.startFailed'))
  }
}

function stopPortScan() {
  if (portScanId.value) StopPortScan(portScanId.value)
}

// Put the clicked host into the port-scan target field.
function onHostRowClick(row: any) {
  if (portScanning.value) return
  portHost.value = row.ip
  portMode.value = 'common'
}

// ── Right-click export context menu ──
const ctxMenu = ref<{ visible: boolean; x: number; y: number; kind: 'ip' | 'port' }>({
  visible: false,
  x: 0,
  y: 0,
  kind: 'ip'
})

function onIpCtxMenu(row: any, column: any, event: MouseEvent) {
  showCtxMenu(event, 'ip')
}
function onPortCtxMenu(row: any, column: any, event: MouseEvent) {
  showCtxMenu(event, 'port')
}
function showCtxMenu(event: MouseEvent, kind: 'ip' | 'port') {
  event.preventDefault()
  event.stopPropagation()
  const menuW = 140
  const menuH = 34
  const margin = 8
  let x = event.clientX
  let y = event.clientY
  if (x + menuW > window.innerWidth - margin) x = window.innerWidth - menuW - margin
  if (y + menuH > window.innerHeight - margin) y = window.innerHeight - menuH - margin
  ctxMenu.value = { visible: true, x, y, kind }
}
function hideCtxMenu() {
  ctxMenu.value.visible = false
}

// ── Export the current result table as CSV via the native save dialog ──
async function exportResults(kind: 'ip' | 'port') {
  hideCtxMenu()
  const sep = ','
  const now = new Date()
  const stamp = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}_${String(now.getHours()).padStart(2, '0')}-${String(now.getMinutes()).padStart(2, '0')}-${String(now.getSeconds()).padStart(2, '0')}`

  let fileName: string
  let content: string

  if (kind === 'ip') {
    fileName = `ip_scan_${stamp}.csv`
    const header = [
      t('netscan.ipScan.columns.ip'),
      t('netscan.ipScan.columns.mac'),
      t('netscan.ipScan.columns.hostname'),
      ...serviceColumns.map(sp => `${sp.port} (${sp.name})`)
    ].join(sep)
    const openTag = t('netscan.ipScan.open')
    const closedTag = t('netscan.ipScan.closed')
    const body = ipHosts.value.map(h =>
      [
        h.ip || '',
        h.mac || '',
        h.hostname || '',
        ...serviceColumns.map(sp => (h.ports && h.ports[sp.port] ? openTag : closedTag))
      ].join(sep)
    )
    content = [header, ...body].join('\n')
  } else {
    fileName = `port_scan_${stamp}.csv`
    const header = [
      t('netscan.portScan.columns.host'),
      t('netscan.portScan.columns.port'),
      t('netscan.portScan.columns.service'),
      t('netscan.portScan.columns.state')
    ].join(sep)
    const body = portResults.value.map(r =>
      [r.host || '', r.port ?? '', r.service || '', r.state || ''].join(sep)
    )
    content = [header, ...body].join('\n')
  }

  // Escape fields so the CSV stays valid even if a value contains a delimiter.
  const escapeCell = (cell: string) => {
    const s = String(cell ?? '')
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const lines = content.split('\n').map(line => line.split(sep).map(escapeCell).join(','))
  content = '\uFEFF' + lines.join('\n') // BOM helps Excel open UTF-8 CSV

  // Ask the user where to save via the native save dialog, then write the file.
  try {
    const path = await SaveFileDialogFiltered(t('netscan.export.title'), fileName, t('netscan.export.filterCsv'), '*.csv')
    if (!path) return // user dismissed the dialog
    await WriteFileBase64(path, utf8ToBase64(content))
    msg.success(t('netscan.export.saved'))
  } catch (e) {
    msg.error(`${t('netscan.export.failed')} ${(e as Error)?.message ?? ''}`)
  }
}

// Copy the visible result table (IP or port) to the clipboard as
// tab-separated text so it pastes cleanly into spreadsheets/editors.
async function copyResults(kind: 'ip' | 'port') {
  hideCtxMenu()
  const sep = '\t'
  let lines: string[][]
  if (kind === 'ip') {
    const header = [
      t('netscan.ipScan.columns.ip'),
      t('netscan.ipScan.columns.mac'),
      t('netscan.ipScan.columns.hostname'),
      ...serviceColumns.map(sp => `${sp.port} (${sp.name})`)
    ]
    const openTag = t('netscan.ipScan.open')
    const closedTag = t('netscan.ipScan.closed')
    const body = ipHosts.value.map(h => [
      h.ip || '',
      h.mac || '',
      h.hostname || '',
      ...serviceColumns.map(sp => (h.ports && h.ports[sp.port] ? openTag : closedTag))
    ])
    lines = [header, ...body]
  } else {
    const header = [
      t('netscan.portScan.columns.host'),
      t('netscan.portScan.columns.port'),
      t('netscan.portScan.columns.service'),
      t('netscan.portScan.columns.state')
    ]
    const body = portResults.value.map(r => [r.host || '', r.port ?? '', r.service || '', r.state || ''])
    lines = [header, ...body]
  }
  try {
    await navigator.clipboard.writeText(lines.map(row => row.join(sep)).join('\n'))
    msg.success(t('netscan.export.copied'))
  } catch (e) {
    msg.error(`${t('netscan.export.copyFailed')} ${(e as Error)?.message ?? ''}`)
  }
}

// A netscan event belongs to us if we have registered its id, OR (as a
// race-proof fallback for events that can arrive just before ownedScans.add
// runs after the await) it matches the id of the scan we most recently
// started. This prevents the occasional first run from dropping everything.
function isOwnedScan(id: string): boolean {
  if (ownedScans.has(id)) return true
  return !!portScanId.value && id === portScanId.value || !!ipScanId.value && id === ipScanId.value
}

function handleHostFound(ev: any) {
  const d = ev?.data
  if (!d || !isOwnedScan(d.scanId)) return
  const host = d.host
  if (!host?.ip) return
  const idx = ipHosts.value.findIndex(h => h.ip === host.ip)
  if (idx >= 0) ipHosts.value[idx] = host
  else ipHosts.value.push(host)
}

function handleProgress(ev: any) {
  const d = ev?.data
  if (!d || !isOwnedScan(d.scanId)) return
  const done = Number(d.done) || 0
  const total = Number(d.total) || 0
  if (d.kind === 'ip') {
    ipDone.value = done
    ipTotal.value = total
  } else if (d.kind === 'port') {
    portDone.value = done
    portTotal.value = total
  }
}

function handlePortOpen(ev: any) {
  const d = ev?.data
  if (!d || !isOwnedScan(d.scanId)) return
  const host = portScanTargets.get(d.scanId) || portHost.value || ''
  // Avoid duplicate rows for the same host+port.
  if (!portResults.value.some(r => r.host === host && r.port === d.port)) {
    portResults.value.push({ host, port: d.port, service: d.service, state: d.state })
  }
}

function handleDone(ev: any) {
  const d = ev?.data
  if (!d || !isOwnedScan(d.scanId)) return
  ownedScans.delete(d.scanId)
  if (d.kind === 'ip') {
    ipScanning.value = false
  } else if (d.kind === 'port') {
    portScanning.value = false
  }
}

let unsubs: (() => void)[] = []

onMounted(() => {
  unsubs = [
    Events.On('netscan:host-found', handleHostFound),
    Events.On('netscan:progress', handleProgress),
    Events.On('netscan:port-open', handlePortOpen),
    Events.On('netscan:done', handleDone)
  ]
  // Close the export context menu on any outside click.
  document.addEventListener('click', hideCtxMenu)
})

onUnmounted(() => {
  // Stop any in-flight scans this component started, then detach listeners.
  if (ipScanId.value) StopIPScan(ipScanId.value)
  if (portScanId.value) StopPortScan(portScanId.value)
  unsubs.forEach(u => u())
  document.removeEventListener('click', hideCtxMenu)
  ownedScans.clear()
  portScanTargets.clear()
})
</script>

<style scoped>
/* Aligned with the SFTP built-in editor / file-list look: a dialog whose header
   is a single full-width flat toolbar, with CSS-variable theming so the whole
   scan UI follows dark / deep-blue / light automatically. */
/* The el-dialog root carries class="netscan-dialog" directly, so style it with
   that selector (NOT ".netscan-dialog :deep(.el-dialog)" — that descendant form
   never matches because they are the same element, which silently disabled the
   fixed-height flex column and collapsed the tables). */
.netscan-dialog {
  display: flex;
  flex-direction: column;
  height: 90vh;
  max-height: 90vh;
  background: var(--bg-base);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
}
.netscan-dialog :deep(.el-dialog__header) {
  padding: 0.625rem 1rem;
  border-bottom: 1px solid var(--border-subtle);
  margin: 0;
  background: var(--bg-elevated);
  flex-shrink: 0;
}
.netscan-dialog :deep(.el-dialog__title) {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--text-primary);
  font-family: var(--font-ui);
}
.netscan-dialog :deep(.el-dialog__body) {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 0.75rem;
  overflow: hidden;
}
.netscan-dialog :deep(.el-dialog__footer) {
  padding: 0.5rem 1rem;
  border-top: 1px solid var(--border-subtle);
  background: var(--bg-elevated);
  flex-shrink: 0;
}

/* ── Full-width flat toolbar ── */
.netscan-toolbar {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 0.625rem 0.75rem;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--bg-elevated);
  flex-shrink: 0;
  flex-wrap: wrap;
}
.toolbar-group {
  display: flex;
  align-items: center;
  gap: 0.4375rem;
  flex-wrap: wrap;
}
.toolbar-group-title {
  font-size: 0.6875rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  color: var(--text-secondary);
  font-family: var(--font-ui);
  margin-right: 0.125rem;
}
.toolbar-divider {
  width: 1px;
  align-self: stretch;
  background: var(--border-subtle);
  flex-shrink: 0;
}
.toolbar-sep {
  color: var(--text-muted);
  font-size: 0.75rem;
}

/* ── Flat inputs: drop Element's default chrome for a slim underlying field
       that reads like the SFTP filter input. ── */
.netscan-toolbar .el-input,
.netscan-toolbar .el-input-number {
  --el-input-bg-color: var(--bg-base);
  --el-input-border-color: var(--border-subtle);
  --el-input-hover-border-color: var(--accent);
  --el-input-focus-border-color: var(--accent);
  --el-input-text-color: var(--text-primary);
  --el-input-placeholder-color: var(--text-muted);
  --el-input-height: 1.875rem;
  --el-input-border-radius: var(--radius-sm);
}
.netscan-toolbar .el-input__wrapper,
.netscan-toolbar .el-input-number .el-input__wrapper {
  background: var(--bg-base);
  box-shadow: 0 0 0 1px var(--border-subtle) inset;
  transition: box-shadow 0.12s ease;
}
.netscan-toolbar .el-input__wrapper:hover {
  box-shadow: 0 0 0 1px var(--bg-hover) inset;
}
.netscan-toolbar .el-input__wrapper.is-focus {
  box-shadow: 0 0 0 1px var(--accent) inset;
}

.ip-input { width: 9rem; }
.port-host-input { width: 9.5rem; }
.port-num { width: 6.25rem; }

/* ── Mode toggle as a flat segmented control ── */
.netscan-toolbar .el-radio-group.port-mode {
  display: inline-flex;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  overflow: hidden;
  flex-shrink: 0;
}
.netscan-toolbar .port-mode .el-radio-button__inner {
  border: none;
  background: transparent;
  color: var(--text-muted);
  font-family: var(--font-ui);
  font-size: 0.75rem;
  padding: 0.3rem 0.625rem;
  box-shadow: none;
  transition: all 0.12s ease;
}
.netscan-toolbar .port-mode .el-radio-button__original-radio:checked + .el-radio-button__inner {
  background: var(--accent-subtle);
  color: var(--accent);
}

/* ── Buttons: flat, accent-primary / danger-secondary ── */
.netscan-toolbar .el-button {
  --el-button-border-radius: var(--radius-sm);
  font-family: var(--font-ui);
}
.go-btn.el-button--primary {
  --el-button-bg-color: var(--accent);
  --el-button-border-color: var(--accent);
  --el-button-text-color: #fff;
  --el-button-hover-bg-color: color-mix(in srgb, var(--accent) 85%, #000);
  --el-button-hover-border-color: color-mix(in srgb, var(--accent) 85%, #000);
  --el-button-active-bg-color: color-mix(in srgb, var(--accent) 70%, #000);
  --el-button-active-border-color: color-mix(in srgb, var(--accent) 70%, #000);
}
.go-btn.el-button--danger {
  --el-button-bg-color: transparent;
  --el-button-border-color: var(--el-color-danger);
  --el-button-text-color: var(--el-color-danger);
  --el-button-hover-bg-color: color-mix(in srgb, var(--el-color-danger) 12%, transparent);
}

/* Fixed-height content root ensures the two sections always have room to lay
   out, independent of el-dialog's internal header/body/footer sizing. */
.netscan-content {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: calc(90vh - 7.5rem);
  max-height: calc(90vh - 7.5rem);
  overflow: hidden;
}
/* ── Results region: two stacked scan sections, each locked to exactly half
   the body height so each table scrolls independently and neither gets pushed
   out of the dialog. ── */
.netscan-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.scan-section {
  flex: 1 1 0;         /* both sections share the body height equally */
  min-height: 0;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  overflow: hidden;
  background: var(--bg-elevated);
}

/* Each table lives in its own well. .table-wrap is the scroll container
   (overflow-y: auto) for its own half; the el-table inside renders at natural
   height and the wrapper scrolls it — so the two tables scroll independently
   without relying on el-table's fixed-height mode. */
.table-wrap {
  flex: 1;
  min-height: 0;
  position: relative;
  display: flex;
  flex-direction: column;
  overflow-y: auto;
  overflow-x: hidden;
}
.scan-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.4375rem 0.75rem;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.scan-title {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--text-primary);
  font-family: var(--font-ui);
  flex-shrink: 0;
  letter-spacing: 0.01em;
}

/* ── Progress bar fits the flat header ── */
.scan-progress {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  flex: 1;
  min-width: 12rem;
}
.scan-progress .el-progress { flex: 1; margin: 0; }
.progress-label {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-secondary);
  white-space: nowrap;
}

/* ── Result tables: compact, hairline rows, mono numerals — mirrors the
   SFTP file list instead of Element's boxy default grid.
   The el-table height="100%" prop resolves against .table-wrap, giving each
   table its own internal scrollbar. user-select: text lets cell contents be
   mouse-selected and copied. ── */
.scan-table {
  width: 100%;
  flex-shrink: 0;
  --el-table-border-color: var(--border-subtle);
}
/* Keep the header visible while .table-wrap scrolls the rows. */
.scan-table :deep(.el-table__header-wrapper th.el-table__cell) {
  position: sticky;
  top: 0;
  z-index: 2;
  background: var(--bg-elevated);
}
.scan-table :deep(.el-table__header-wrapper) {
  position: sticky;
  top: 0;
  z-index: 3;
}
.scan-table :deep(.el-table__inner-wrapper::before),
.scan-table :deep(.el-table__border-left-patch),
.scan-table :deep(.el-table::after),
.scan-table :deep(.el-table__inner-wrapper::after) { background: var(--border-subtle); }

.scan-table :deep(.el-table__body td.el-table__cell),
.scan-table :deep(.el-table__header-wrapper th.el-table__cell) {
  user-select: text;
  -webkit-user-select: text;
}

.scan-table :deep(.el-table__header-wrapper th.el-table__cell) {
  background: var(--bg-elevated);
  color: var(--text-secondary);
  font-size: 0.6875rem;
  font-weight: 600;
  letter-spacing: 0.02em;
  text-transform: uppercase;
  border-bottom: 1px solid var(--border-subtle);
  padding: 0.375rem 0.5rem;
}
.scan-table :deep(.el-table__body td.el-table__cell) {
  padding: 0.3125rem 0.5rem;
  font-size: 0.75rem;
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-subtle);
}
.scan-table :deep(.el-table__row) {
  cursor: pointer;
  transition: background 0.1s ease;
}
.scan-table :deep(.el-table__row:hover > td.el-table__cell) {
  background: var(--bg-hover);
}
.scan-table :deep(.el-table__row.current-row > td.el-table__cell) {
  background: var(--accent-subtle);
}

.host-ip,
.port-num-text {
  font-family: var(--font-mono);
  font-weight: 600;
}
.host-ip { color: var(--accent); user-select: text; }
.port-num-text { color: var(--success); }

.scan-table :deep(.el-table__empty-block) { background: var(--bg-elevated); }
.scan-table :deep(.el-table__empty-text) { color: var(--text-muted); font-size: 0.75rem; }

/* Open/closed status tags: subtle, tinted like the editor's status colors. */
.scan-table :deep(.el-tag) {
  border: none;
  font-size: 0.6875rem;
  font-weight: 500;
}
.scan-table :deep(.el-tag--success) {
  background: var(--success-subtle);
  color: var(--success);
}
.scan-table :deep(.el-tag--info) {
  background: var(--accent-subtle);
  color: var(--text-muted);
}

.scan-hint {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  text-align: center;
  padding: 1rem;
  color: var(--text-muted);
  font-size: 0.75rem;
  pointer-events: none;
}

/* ── Right-click export context menu (teleported to <body>) ── */
.ctx-menu {
  position: fixed;
  z-index: 99999;
  list-style: none;
  margin: 0;
  padding: 0.25rem;
  min-width: 9rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  box-shadow: 0 0.375rem 1.25rem rgba(0, 0, 0, 0.25);
  font-family: var(--font-ui);
}
.ctx-item {
  display: flex;
  align-items: center;
  padding: 0.375rem 0.625rem;
  border-radius: calc(var(--radius-sm) * 0.6);
  font-size: 0.75rem;
  color: var(--text-primary);
  cursor: pointer;
  white-space: nowrap;
  user-select: none;
}
.ctx-item:hover {
  background: var(--accent-subtle);
  color: var(--accent);
}

</style>
