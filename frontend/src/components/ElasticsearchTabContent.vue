<template>
  <div class="es-tab-content">
    <!-- Cluster status bar -->
    <div class="es-cluster-bar">
      <span class="health-dot" :class="healthStatus" />
      <span class="cluster-name">{{ clusterInfo?.clusterName || clusterHealth?.clusterName || '—' }}</span>
      <span class="cluster-meta" v-if="clusterInfo?.version">v{{ clusterInfo.version }}</span>
      <span class="cluster-meta" v-if="clusterHealth">
        {{ clusterHealth.status }} · {{ clusterHealth.numberOfNodes }} {{ t('es.nodes') }} · {{ clusterHealth.activeShards }} {{ t('es.shards') }}
      </span>
      <button class="btn btn-ghost btn-icon btn-sm" style="margin-left:auto" :title="t('es.refresh')" @click="refreshAll">
        <RefreshCw :size="14" />
      </button>
    </div>

    <div class="es-main">
      <!-- Left: index tree -->
      <div class="es-left" :style="{ width: leftWidth + 'px' }">
        <div class="search-wrap">
          <input v-model="treeSearchQuery" class="search-input" :placeholder="t('es.searchIndices')" />
        </div>
        <div class="tree-toolbar">
          <label class="hide-system">
            <input type="checkbox" v-model="hideSystemIndices" />
            {{ t('es.hideSystem') }}
          </label>
          <button class="btn btn-ghost btn-sm" @click="openCreateIndex">
            <Plus :size="14" /> {{ t('es.newIndex') }}
          </button>
        </div>
        <div class="tree-content" @contextmenu.prevent>
          <div v-if="treeLoading" class="tree-loading">{{ t('db.loading') }}</div>
          <template v-else>
            <div
              v-for="idx in filteredIndices"
              :key="idx.name"
              class="index-item"
              :class="{ selected: activeIndex === idx.name }"
              @click="selectIndex(idx.name)"
              @contextmenu.prevent="onIndexContextMenu($event, idx)"
            >
              <span class="health-dot" :class="idx.health || 'unknown'" />
              <span class="index-name" :title="idx.name">{{ idx.name }}</span>
              <span class="index-meta">{{ formatDocs(idx.docsCount) }} · {{ idx.storeSize || '—' }}</span>
            </div>
            <div v-if="filteredIndices.length === 0" class="empty-hint">{{ t('es.noIndices') }}</div>
          </template>
        </div>
      </div>

      <div class="es-resizer" @mousedown="onResizeStart" />

      <!-- Right content -->
      <div class="es-right">
        <div class="es-breadcrumb" v-if="activeIndex">
          <span class="crumb current">{{ activeIndex }}</span>
          <span class="crumb-meta" v-if="activeIndexInfo">
            {{ activeIndexInfo.status }} · {{ formatDocs(activeIndexInfo.docsCount) }} docs · {{ activeIndexInfo.storeSize || '—' }}
          </span>
        </div>

        <div class="es-tabs">
          <button class="es-tab" :class="{ active: activeSubTab === 'docs' }" @click="activeSubTab = 'docs'" :disabled="!activeIndex">
            {{ t('es.docsTab') }}
          </button>
          <button class="es-tab" :class="{ active: activeSubTab === 'mapping' }" @click="switchToMapping" :disabled="!activeIndex">
            {{ t('es.mappingTab') }}
          </button>
          <button class="es-tab" :class="{ active: activeSubTab === 'settings' }" @click="switchToSettings" :disabled="!activeIndex">
            {{ t('es.settingsTab') }}
          </button>
          <button class="es-tab" :class="{ active: activeSubTab === 'rest' }" @click="activeSubTab = 'rest'">
            {{ t('es.restTab') }}
          </button>
        </div>

        <!-- Documents -->
        <div v-if="activeSubTab === 'docs' && activeIndex" class="docs-section">
          <div class="editor-top" :style="{ height: topHeight + 'px' }">
            <div class="query-mode-row">
              <el-radio-group v-model="queryMode" size="small">
                <el-radio-button label="simple">{{ t('es.querySimple') }}</el-radio-button>
                <el-radio-button label="dsl">{{ t('es.queryDsl') }}</el-radio-button>
              </el-radio-group>
            </div>
            <div v-if="queryMode === 'simple'" class="simple-query">
              <input
                v-model="simpleQuery"
                class="search-input"
                :placeholder="t('es.simpleQueryPlaceholder')"
                @keydown.enter="runSearch"
              />
            </div>
            <div v-else class="query-editor-wrap">
              <SyntaxEditor
                v-model="dslBody"
                lang="json"
                compact
                @execute="runSearch"
              />
            </div>
            <div class="docs-actions">
              <button class="btn btn-primary btn-sm" @click="runSearch">{{ t('es.search') }}</button>
              <button class="btn btn-default btn-sm" @click="openNewDocument">
                <Plus :size="14" /> {{ t('es.newDocument') }}
              </button>
              <span class="took-hint" v-if="searchResult">{{ searchResult.took }}ms · {{ searchResult.total }} hits</span>
            </div>
          </div>

          <div class="editor-resizer" @mousedown="onTopResizeStart" />

          <div class="editor-bottom">
            <div v-if="queryError" class="error-msg">{{ queryError }}</div>
            <div v-if="queryLoading" class="loading-overlay">
              <div class="loading-box">
                <div class="spinner" />
                <span class="loading-text">{{ t('db.loading') }}</span>
              </div>
            </div>
            <div v-if="columns.length > 0" class="result-grid">
              <div class="result-table-wrap">
                <el-table
                  :data="tableData"
                  border
                  size="small"
                  style="width:100%"
                  class="db-result-table"
                  :empty-text="t('db.noData')"
                  @row-click="onRowClick"
                  @row-dblclick="onRowDblClick"
                >
                  <el-table-column
                    v-for="col in columns"
                    :key="col"
                    :prop="col"
                    :label="col"
                    min-width="120"
                    show-overflow-tooltip
                  >
                    <template #default="{ row }">
                      <span
                        class="cell-value"
                        :class="{ 'cell-null': row[col] === null || row[col] === undefined }"
                      >{{ formatCellValue(row[col]) }}</span>
                    </template>
                  </el-table-column>
                  <el-table-column width="80" fixed="right">
                    <template #default="{ row }">
                      <button class="btn btn-ghost btn-icon btn-sm" style="color:var(--text-secondary)" @click.stop="onRowDblClick(row)">
                        <Pencil :size="14" />
                      </button>
                      <button class="btn btn-ghost btn-icon btn-sm" style="color:var(--error)" @click.stop="deleteDocument(row)">
                        <Trash2 :size="14" />
                      </button>
                    </template>
                  </el-table-column>
                </el-table>
              </div>
              <div class="pagination">
                <el-pagination
                  background
                  layout="sizes, prev, pager, next, total"
                  :page-sizes="[10, 20, 50, 100]"
                  :page-size="pageSize"
                  :total="searchResult?.total || 0"
                  :current-page="currentPage"
                  :pager-count="5"
                  small
                  @size-change="onPageSizeChange"
                  @current-change="onPageChange"
                />
              </div>
            </div>
            <div v-else-if="!queryLoading && !queryError" class="select-hint">{{ t('es.selectHint') }}</div>
          </div>
        </div>

        <!-- Mapping -->
        <div v-else-if="activeSubTab === 'mapping' && activeIndex" class="json-pane">
          <div v-if="mappingLoading" class="tree-loading">{{ t('db.loading') }}</div>
          <pre v-else class="json-pre">{{ mappingText || t('es.noData') }}</pre>
        </div>

        <!-- Settings -->
        <div v-else-if="activeSubTab === 'settings' && activeIndex" class="json-pane">
          <div v-if="settingsLoading" class="tree-loading">{{ t('db.loading') }}</div>
          <pre v-else class="json-pre">{{ settingsText || t('es.noData') }}</pre>
        </div>

        <!-- REST console -->
        <div v-else-if="activeSubTab === 'rest'" class="rest-section">
          <div class="rest-toolbar">
            <el-select v-model="restMethod" style="width:110px" size="small">
              <el-option v-for="m in restMethods" :key="m" :label="m" :value="m" />
            </el-select>
            <input v-model="restPath" class="rest-path" placeholder="/_cluster/health" @keydown.enter="runRest" />
            <button class="btn btn-primary btn-sm" @click="runRest" :disabled="restLoading">{{ t('es.send') }}</button>
          </div>
          <SyntaxEditor
            v-model="restBody"
            lang="json"
            class="rest-body"
            :readonly="restMethod === 'GET' || restMethod === 'DELETE'"
          />
          <div class="rest-result">
            <div class="rest-status" v-if="restResult">HTTP {{ restResult.status }}</div>
            <div v-if="restError" class="error-msg">{{ restError }}</div>
            <pre class="json-pre">{{ restResult?.body || '' }}</pre>
          </div>
        </div>

        <div v-else class="select-hint">{{ t('es.selectHint') }}</div>
      </div>
    </div>

    <!-- Index context menu -->
    <Menu ref="ctxMenuRef" v-model:visible="ctxMenuVisible" v-slot="{ current }">
      <MenuItem @click="ctxRefresh(current)">{{ t('es.refresh') }}</MenuItem>
      <MenuItem @click="ctxOpenIndex(current)">{{ t('es.openIndex') }}</MenuItem>
      <MenuItem @click="ctxCloseIndex(current)">{{ t('es.closeIndex') }}</MenuItem>
      <MenuDivider />
      <MenuItem class="danger" @click="ctxDeleteIndex(current)">{{ t('es.deleteIndex') }}</MenuItem>
    </Menu>

    <!-- Document editor dialog -->
    <el-dialog
      v-model="docDialogVisible"
      :title="docDialogMode === 'create' ? t('es.newDocument') : t('es.editDocument')"
      width="640px"
      destroy-on-close
    >
      <el-form v-if="docDialogMode === 'create'" label-position="top">
        <el-form-item :label="t('es.documentId')">
          <el-input v-model="docEditId" :placeholder="t('es.documentIdAuto')" />
        </el-form-item>
      </el-form>
      <SyntaxEditor v-model="docEditText" lang="json" class="doc-editor" />
      <template #footer>
        <button class="btn btn-default" @click="docDialogVisible = false">{{ t('settings.cancel') }}</button>
        <button class="btn btn-primary" @click="saveDocument" :disabled="docSaving">{{ t('redis.save') }}</button>
      </template>
    </el-dialog>

    <!-- Create index dialog -->
    <el-dialog v-model="createIndexVisible" :title="t('es.newIndex')" width="520px" destroy-on-close>
      <el-form label-position="top">
        <el-form-item :label="t('es.indexName')" required>
          <el-input v-model="newIndexName" />
        </el-form-item>
        <el-form-item :label="t('es.indexBody')">
          <SyntaxEditor v-model="newIndexBody" lang="json" class="doc-editor index-body" />
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="btn btn-default" @click="createIndexVisible = false">{{ t('settings.cancel') }}</button>
        <button class="btn btn-primary" @click="createIndex" :disabled="!newIndexName.trim()">{{ t('redis.save') }}</button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from '../i18n'
import { msg } from '../services/message'
import { ElMessageBox } from 'element-plus'
import Menu from './Menu.vue'
import SyntaxEditor from './SyntaxEditor.vue'
import MenuItem from './MenuItem.vue'
import MenuDivider from './MenuDivider.vue'
import {
  EsClusterInfo,
  EsClusterHealth,
  EsListIndices,
  EsSearch,
  EsGetMapping,
  EsGetSettings,
  EsIndexDoc,
  EsUpdateDoc,
  EsDeleteDoc,
  EsCreateIndex,
  EsDeleteIndex,
  EsOpenIndex,
  EsCloseIndex,
  EsRest,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import type { EsIndexInfo, EsClusterHealth as EsHealth, EsClusterInfo as EsInfo, EsSearchResult, EsRestResult } from '../types/elasticsearch'
import { Plus, Pencil, Trash2, RefreshCw } from '@lucide/vue'

const props = defineProps<{ sessionId: string }>()
const { t } = useI18n()

const leftWidth = ref(280)
const topHeight = ref(160)
const treeLoading = ref(false)
const treeSearchQuery = ref('')
const hideSystemIndices = ref(true)
const indices = ref<EsIndexInfo[]>([])
const activeIndex = ref('')
const activeSubTab = ref<'docs' | 'mapping' | 'settings' | 'rest'>('docs')

const clusterInfo = ref<EsInfo | null>(null)
const clusterHealth = ref<EsHealth | null>(null)
const healthStatus = computed(() => (clusterHealth.value?.status || 'unknown').toLowerCase())

const queryMode = ref<'simple' | 'dsl'>('simple')
const simpleQuery = ref('')
const dslBody = ref('{\n  "query": { "match_all": {} }\n}')
const queryLoading = ref(false)
const queryError = ref('')
const searchResult = ref<EsSearchResult | null>(null)
const pageFrom = ref(0)
const pageSize = ref(50)
const selectedRowId = ref('')

const mappingText = ref('')
const mappingLoading = ref(false)
const settingsText = ref('')
const settingsLoading = ref(false)

const restMethods = ['GET', 'POST', 'PUT', 'DELETE', 'HEAD']
const restMethod = ref('GET')
const restPath = ref('/_cluster/health')
const restBody = ref('')
const restLoading = ref(false)
const restError = ref('')
const restResult = ref<EsRestResult | null>(null)

const ctxMenuVisible = ref(false)
const ctxMenuRef = ref<InstanceType<typeof Menu> | null>(null)

const docDialogVisible = ref(false)
const docDialogMode = ref<'create' | 'edit'>('create')
const docEditId = ref('')
const docEditText = ref('{\n  \n}')
const docSaving = ref(false)

const createIndexVisible = ref(false)
const newIndexName = ref('')
const newIndexBody = ref('')

const filteredIndices = computed(() => {
  const q = treeSearchQuery.value.trim().toLowerCase()
  return indices.value.filter(idx => {
    if (hideSystemIndices.value && idx.name.startsWith('.')) return false
    if (q && !idx.name.toLowerCase().includes(q)) return false
    return true
  })
})

const activeIndexInfo = computed(() => indices.value.find(i => i.name === activeIndex.value) || null)

const tableData = computed(() => {
  if (!searchResult.value) return []
  return searchResult.value.hits.map(h => {
    try { return JSON.parse(h) } catch { return { _raw: h } }
  })
})

const columns = computed(() => {
  const cols = new Set<string>()
  cols.add('_id')
  for (const row of tableData.value) {
    for (const k of Object.keys(row)) {
      if (k !== '_index' && k !== '_score') cols.add(k)
    }
  }
  // Prefer _id first, then sorted field names (cap columns)
  const rest = [...cols].filter(c => c !== '_id').sort()
  return ['_id', ...rest].slice(0, 40)
})

const currentPage = computed(() => Math.floor(pageFrom.value / pageSize.value) + 1)

watch(() => props.sessionId, () => {
  if (props.sessionId) refreshAll()
})

onMounted(() => {
  if (props.sessionId) refreshAll()
})

async function refreshAll() {
  await Promise.all([loadCluster(), loadIndices()])
}

async function loadCluster() {
  if (!props.sessionId) return
  try {
    const [info, health] = await Promise.all([
      EsClusterInfo(props.sessionId),
      EsClusterHealth(props.sessionId),
    ])
    clusterInfo.value = info as EsInfo
    clusterHealth.value = health as EsHealth
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

async function loadIndices() {
  if (!props.sessionId) return
  treeLoading.value = true
  try {
    indices.value = (await EsListIndices(props.sessionId)) as EsIndexInfo[]
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
  treeLoading.value = false
}

function selectIndex(name: string) {
  activeIndex.value = name
  if (activeSubTab.value === 'rest') {
    // keep rest
  } else {
    activeSubTab.value = 'docs'
  }
  pageFrom.value = 0
  runSearch()
}

function buildSearchBody(): string {
  if (queryMode.value === 'dsl') {
    return dslBody.value.trim() || '{}'
  }
  const q = simpleQuery.value.trim()
  if (!q) return '{"query":{"match_all":{}}}'
  return JSON.stringify({
    query: {
      query_string: { query: q, default_field: '*' },
    },
  })
}

async function runSearch() {
  if (!props.sessionId || !activeIndex.value) return
  queryLoading.value = true
  queryError.value = ''
  try {
    searchResult.value = await EsSearch(
      props.sessionId,
      activeIndex.value,
      buildSearchBody(),
      pageFrom.value,
      pageSize.value,
    ) as EsSearchResult
  } catch (e: any) {
    queryError.value = e?.message || String(e)
    searchResult.value = null
  }
  queryLoading.value = false
}

function onPageChange(page: number) {
  pageFrom.value = (page - 1) * pageSize.value
  runSearch()
}

function onPageSizeChange(size: number) {
  pageSize.value = size
  pageFrom.value = 0
  runSearch()
}

function formatCellValue(v: unknown): string {
  if (v === null || v === undefined) return 'null'
  if (typeof v === 'object') {
    const s = JSON.stringify(v)
    return s.length > 120 ? s.slice(0, 117) + '…' : s
  }
  const s = String(v)
  return s.length > 120 ? s.slice(0, 117) + '…' : s
}

function formatDocs(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n ?? 0)
}

function onRowClick(row: any) {
  selectedRowId.value = row?._id || ''
}

function onRowDblClick(row: any) {
  docDialogMode.value = 'edit'
  docEditId.value = row?._id || ''
  const copy = { ...row }
  delete copy._score
  delete copy._index
  docEditText.value = JSON.stringify(copy, null, 2)
  docDialogVisible.value = true
}

function openNewDocument() {
  docDialogMode.value = 'create'
  docEditId.value = ''
  docEditText.value = '{\n  \n}'
  docDialogVisible.value = true
}

async function saveDocument() {
  if (!props.sessionId || !activeIndex.value) return
  let parsed: any
  try {
    parsed = JSON.parse(docEditText.value)
  } catch {
    msg.error(t('es.invalidJSON'))
    return
  }
  const id = docDialogMode.value === 'edit' ? docEditId.value : (docEditId.value || parsed._id || '')
  const body = { ...parsed }
  delete body._id
  delete body._index
  delete body._score
  docSaving.value = true
  try {
    if (docDialogMode.value === 'edit') {
      if (!id) throw new Error('document _id required')
      await EsUpdateDoc(props.sessionId, activeIndex.value, id, JSON.stringify(body))
      msg.success(t('es.updateSuccess'))
    } else {
      await EsIndexDoc(props.sessionId, activeIndex.value, id, JSON.stringify(body))
      msg.success(t('es.insertSuccess'))
    }
    docDialogVisible.value = false
    await runSearch()
    await loadIndices()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
  docSaving.value = false
}

async function deleteDocument(row: any) {
  const id = row?._id
  if (!id || !props.sessionId || !activeIndex.value) return
  try {
    await ElMessageBox.confirm(t('es.deleteConfirm'))
  } catch {
    return
  }
  try {
    await EsDeleteDoc(props.sessionId, activeIndex.value, id)
    msg.success(t('es.deleteSuccess'))
    await runSearch()
    await loadIndices()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

async function switchToMapping() {
  activeSubTab.value = 'mapping'
  await loadMapping()
}

async function switchToSettings() {
  activeSubTab.value = 'settings'
  await loadSettings()
}

async function loadMapping() {
  if (!props.sessionId || !activeIndex.value) return
  mappingLoading.value = true
  try {
    mappingText.value = await EsGetMapping(props.sessionId, activeIndex.value)
  } catch (e: any) {
    mappingText.value = e?.message || String(e)
  }
  mappingLoading.value = false
}

async function loadSettings() {
  if (!props.sessionId || !activeIndex.value) return
  settingsLoading.value = true
  try {
    settingsText.value = await EsGetSettings(props.sessionId, activeIndex.value)
  } catch (e: any) {
    settingsText.value = e?.message || String(e)
  }
  settingsLoading.value = false
}

async function runRest() {
  if (!props.sessionId) return
  restLoading.value = true
  restError.value = ''
  try {
    restResult.value = await EsRest(
      props.sessionId,
      restMethod.value,
      restPath.value,
      restMethod.value === 'GET' || restMethod.value === 'DELETE' || restMethod.value === 'HEAD' ? '' : restBody.value,
    ) as EsRestResult
  } catch (e: any) {
    restError.value = e?.message || String(e)
    restResult.value = null
  }
  restLoading.value = false
}

function openCreateIndex() {
  newIndexName.value = ''
  newIndexBody.value = ''
  createIndexVisible.value = true
}

async function createIndex() {
  if (!props.sessionId || !newIndexName.value.trim()) return
  try {
    await EsCreateIndex(props.sessionId, newIndexName.value.trim(), newIndexBody.value.trim())
    msg.success(t('es.indexCreated'))
    createIndexVisible.value = false
    await loadIndices()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

function onIndexContextMenu(e: MouseEvent, idx: EsIndexInfo) {
  ctxMenuRef.value?.openAt(e.clientX, e.clientY, idx)
}

async function ctxRefresh(_current: unknown) {
  ctxMenuVisible.value = false
  await loadIndices()
}

async function ctxOpenIndex(current: unknown) {
  const name = (current as EsIndexInfo | null)?.name
  ctxMenuVisible.value = false
  if (!name || !props.sessionId) return
  try {
    await EsOpenIndex(props.sessionId, name)
    msg.success(t('es.indexOpened'))
    await loadIndices()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

async function ctxCloseIndex(current: unknown) {
  const name = (current as EsIndexInfo | null)?.name
  ctxMenuVisible.value = false
  if (!name || !props.sessionId) return
  try {
    await EsCloseIndex(props.sessionId, name)
    msg.success(t('es.indexClosed'))
    await loadIndices()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

async function ctxDeleteIndex(current: unknown) {
  const name = (current as EsIndexInfo | null)?.name
  ctxMenuVisible.value = false
  if (!name || !props.sessionId) return
  try {
    await ElMessageBox.confirm(t('es.deleteIndexConfirm', { name }), { type: 'warning' })
  } catch {
    return
  }
  try {
    await EsDeleteIndex(props.sessionId, name)
    msg.success(t('es.indexDeleted'))
    if (activeIndex.value === name) {
      activeIndex.value = ''
      searchResult.value = null
    }
    await loadIndices()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

function onResizeStart(e: MouseEvent) {
  e.preventDefault()
  const startX = e.clientX
  const startW = leftWidth.value
  const onMove = (ev: MouseEvent) => {
    leftWidth.value = Math.max(180, Math.min(480, startW + ev.clientX - startX))
  }
  const onUp = () => {
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseup', onUp)
  }
  window.addEventListener('mousemove', onMove)
  window.addEventListener('mouseup', onUp)
}

function onTopResizeStart(e: MouseEvent) {
  e.preventDefault()
  const startY = e.clientY
  const startH = topHeight.value
  const onMove = (ev: MouseEvent) => {
    topHeight.value = Math.max(100, Math.min(400, startH + ev.clientY - startY))
  }
  const onUp = () => {
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseup', onUp)
  }
  window.addEventListener('mousemove', onMove)
  window.addEventListener('mouseup', onUp)
}
</script>

<style scoped>
.es-tab-content {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-base);
  color: var(--text-primary);
  overflow: hidden;
}
.es-cluster-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--bg-elevated);
  flex-shrink: 0;
  font-family: var(--font-ui);
  font-size: 12px;
}
.cluster-name {
  font-weight: 600;
  color: var(--text-primary);
}
.cluster-meta {
  color: var(--text-muted);
}
.health-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--text-disabled);
}
.health-dot.green { background: #22c55e; }
.health-dot.yellow { background: #eab308; }
.health-dot.red { background: #ef4444; }
.health-dot.open { background: #22c55e; }
.health-dot.close, .health-dot.closed { background: var(--text-disabled); }

.es-main {
  display: flex;
  flex: 1;
  min-height: 0;
}
.es-left {
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--border-subtle);
  background: var(--bg-elevated);
  flex-shrink: 0;
  min-width: 0;
}
.search-wrap {
  padding: 8px;
  border-bottom: 1px solid var(--border-subtle);
}
.search-input {
  width: 100%;
  font-family: var(--font-ui);
  font-size: 12px;
  padding: 6px 8px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--bg-base);
  color: var(--text-primary);
  outline: none;
}
.search-input:focus { border-color: var(--accent); }
.tree-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 8px;
  border-bottom: 1px solid var(--border-subtle);
  gap: 8px;
}
.hide-system {
  display: flex;
  align-items: center;
  gap: 4px;
  font-family: var(--font-ui);
  font-size: 11px;
  color: var(--text-muted);
  cursor: pointer;
}
.tree-content {
  flex: 1;
  overflow: auto;
}
.tree-loading {
  padding: 12px;
  text-align: center;
  color: var(--text-secondary);
  font-size: 12px;
}
.index-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  cursor: pointer;
  user-select: none;
}
.index-item:hover { background: var(--bg-hover); }
.index-item.selected { background: var(--bg-hover); }
.index-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-ui);
  font-size: 13px;
}
.index-meta {
  font-size: 11px;
  color: var(--text-muted);
  flex-shrink: 0;
}
.empty-hint, .select-hint {
  padding: 16px;
  color: var(--text-muted);
  font-size: 13px;
  text-align: center;
}
.es-resizer {
  width: 4px;
  cursor: col-resize;
  background: transparent;
  flex-shrink: 0;
}
.es-resizer:hover { background: var(--border-subtle); }

.es-right {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}
.es-breadcrumb {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 12px;
  font-family: var(--font-mono);
  font-size: 12px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--bg-elevated);
  flex-shrink: 0;
}
.crumb.current { font-weight: 600; color: var(--text-primary); }
.crumb-meta { color: var(--text-muted); font-family: var(--font-ui); }

.es-tabs {
  display: flex;
  gap: 0;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
  padding: 0 8px;
}
.es-tab {
  font-family: var(--font-ui);
  font-size: 12px;
  padding: 8px 12px;
  background: transparent;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-secondary);
  cursor: pointer;
}
.es-tab:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.es-tab.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
}

.docs-section {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.editor-top {
  padding: 8px;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  overflow: hidden;
}
.query-mode-row { display: flex; }
.simple-query { flex: 1; }
.query-editor-wrap { flex: 1; min-height: 0; display: flex; }
.docs-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.took-hint {
  margin-left: auto;
  font-size: 11px;
  color: var(--text-muted);
}
.editor-resizer {
  height: 4px;
  cursor: row-resize;
  flex-shrink: 0;
}
.editor-resizer:hover { background: var(--border-subtle); }
.editor-bottom {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 8px;
  position: relative;
  overflow: hidden;
}
.result-grid {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.result-table-wrap {
  flex: 1;
  min-height: 0;
  overflow: auto;
}
/* 结果表格主题变量与 DBResultGrid 保持一致 */
.db-result-table {
  --el-table-header-bg-color: var(--bg-elevated, var(--bg-surface));
  --el-table-tr-bg-color: var(--bg-surface);
  --el-table-row-hover-bg-color: var(--bg-hover);
  --el-table-border-color: var(--border-subtle);
  --el-table-header-text-color: var(--text-secondary);
  --el-table-text-color: var(--text-primary);
  --el-table-bg-color: var(--bg-surface);
  font-size: 12px;
}
.cell-value {
  font-family: var(--font-mono);
  font-size: 12px;
}
.cell-null {
  color: var(--text-muted);
  font-style: italic;
}
.pagination {
  padding-top: 8px;
  display: flex;
  justify-content: flex-end;
  flex-shrink: 0;
}
.error-msg {
  color: var(--error);
  padding: 8px;
  background: var(--error-subtle);
  border-radius: var(--radius-sm);
  margin-bottom: 8px;
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: pre-wrap;
}
.loading-overlay {
  position: absolute;
  inset: 0;
  background: var(--scrim);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10;
}
.loading-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}
.spinner {
  width: 24px;
  height: 24px;
  border: 2px solid var(--border-subtle);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
.loading-text { font-size: 12px; color: var(--text-secondary); }

.json-pane {
  flex: 1;
  overflow: auto;
  padding: 12px;
}
.json-pre {
  margin: 0;
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
  user-select: text;
}

.rest-section {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: 8px;
  gap: 8px;
}
.rest-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-shrink: 0;
}
.rest-path {
  flex: 1;
  font-family: var(--font-mono);
  font-size: 13px;
  padding: 6px 8px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--bg-base);
  color: var(--text-primary);
  outline: none;
}
.rest-body {
  height: 140px;
  flex-shrink: 0;
}
.rest-result {
  flex: 1;
  min-height: 0;
  overflow: auto;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: 8px;
  background: var(--bg-elevated);
}
.rest-status {
  font-size: 12px;
  font-weight: 600;
  margin-bottom: 6px;
  color: var(--text-secondary);
}

.doc-editor {
  width: 100%;
  height: 320px;
}
.doc-editor.index-body {
  height: 180px;
}

</style>
