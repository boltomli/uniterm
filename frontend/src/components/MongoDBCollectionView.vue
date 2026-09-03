<template>
  <div class="mongo-collection-view">
    <!-- Sub-tabs -->
    <div class="mongo-tabs">
      <button class="mongo-tab" :class="{ active: activeSubTab === 'query' }" @click="activeSubTab = 'query'">
        {{ t('mongodb.queryTab') }}
      </button>
      <button class="mongo-tab" :class="{ active: activeSubTab === 'indexes' }" @click="activeSubTab = 'indexes'; loadIndexes()">
        {{ t('mongodb.indexesTab') }}
      </button>
    </div>

    <!-- Query sub-tab -->
    <div v-if="activeSubTab === 'query'" class="query-section">
      <div class="editor-top" :style="{ height: topHeight + 'px' }">
        <div class="editor-row">
          <div class="nl-panel">
            <textarea
              v-model="nlInput"
              class="nl-textarea"
              :placeholder="t('mongodb.aiPlaceholder')"
              @keydown="onNLKeydown"
            />
            <div class="nl-btn-wrapper">
              <button class="btn btn-primary nl-generate-btn" @click="generateFilter" :disabled="aiGenerating || !nlInput.trim()">
                <Sparkles :size="14" :class="{ 'ai-pulse': aiGenerating }" />
                {{ aiGenerating ? '...' : t('mongodb.generateQuery') }}
              </button>
            </div>
          </div>
          <div class="query-panel">
            <div class="query-editor-wrap">
              <SyntaxEditor
                v-model="filterText"
                lang="json"
                compact
                @execute="executeQuery"
              />
              <div class="exec-btn-wrapper">
                <button class="btn btn-primary exec-btn-overlay" @click="executeQuery">
                  {{ t('mongodb.executeQuery') }}
                </button>
                <span class="shortcut-hint">Ctrl+Enter</span>
              </div>
            </div>
          </div>
        </div>
        <div style="display:flex;align-items:center;justify-content:space-between;margin-top:8px">
          <button class="btn btn-default btn-sm" @click="openNewDocument">
            <Plus :size="14" /> {{ t('mongodb.newDocument') }}
          </button>
        </div>
      </div>

      <div class="editor-resizer" @mousedown="onTopResizeStart" />

      <div class="editor-bottom">
        <div v-if="queryError" class="error-msg">{{ queryError }}</div>

        <div v-if="queryLoading" class="loading-overlay">
          <div class="loading-box">
            <div class="spinner" />
            <span class="loading-text">{{ t('db.loading') }}</span>
            <button class="btn btn-default" @click="cancelQuery">{{ t('common.cancel') }}</button>
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
                  <button class="btn btn-ghost btn-icon btn-sm" style="color:var(--text-secondary)" @click.stop="onRowClick(row)">
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
              :page-size="queryLimit"
              :total="totalDocs"
              :current-page="currentPage"
              :pager-count="5"
              small
              @size-change="onPageSizeChange"
              @current-change="onPageChange"
            />
          </div>
        </div>
        <div v-else-if="!queryLoading" class="db-placeholder">
          <span>{{ t('db.noData') }}</span>
        </div>
      </div>
    </div>

    <!-- Indexes sub-tab -->
    <div v-if="activeSubTab === 'indexes'" class="indexes-section">
      <div v-if="indexLoading" class="loading-overlay">
        <div class="loading-box">
          <div class="spinner" />
          <span class="loading-text">{{ t('db.loading') }}</span>
        </div>
      </div>
      <div style="margin-bottom:8px">
        <button class="btn btn-default btn-sm" @click="openNewIndexDialog">
          <Plus :size="14" /> {{ t('db.addIndex') }}
        </button>
      </div>
      <el-table :data="indexes" border size="small" style="width:100%" :empty-text="t('db.noData')">
        <el-table-column prop="name" :label="t('db.colName')" show-overflow-tooltip />
        <el-table-column label="Fields">
          <template #default="{ row }">
            {{ (row as MongoIndexInfo).keys.join(', ') }}
          </template>
        </el-table-column>
        <el-table-column prop="type" :label="t('db.colType')" />
        <el-table-column prop="unique" label="Unique" width="80">
          <template #default="{ row }">
            {{ row.unique ? '✓' : '' }}
          </template>
        </el-table-column>
        <el-table-column width="60">
          <template #default="{ row }">
            <button v-if="row.name !== '_id_'" class="btn btn-ghost btn-icon btn-sm" style="color:var(--error)" @click="dropIndex(row.name)">
              <Trash2 :size="14" />
            </button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Indexes sub-tab -->
    <el-dialog append-to-body v-model="newIndexDialogVisible" :title="t('db.addIndex')" width="400px">
      <el-form label-width="80px">
        <el-form-item :label="t('db.colName')">
          <el-input v-model="newIndexName" placeholder="index_name" />
        </el-form-item>
        <el-form-item label="Fields">
          <el-input v-model="newIndexFields" placeholder="field1,-field2" />
        </el-form-item>
        <el-form-item label="Unique">
          <el-switch v-model="newIndexUnique" />
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="btn btn-default" @click="newIndexDialogVisible = false">{{ t('common.cancel') }}</button>
        <button class="btn btn-primary" :disabled="!newIndexName.trim() || !newIndexFields.trim()" @click="createIndex">
          {{ t('common.confirm') }}
        </button>
      </template>
    </el-dialog>

    <!-- Document editor dialog -->
    <el-dialog append-to-body
      v-model="docDialogVisible"
      :title="docDialogMode === 'insert' ? t('mongodb.newDocument') : t('mongodb.editDocument')"
      width="600px"
    >
      <SyntaxEditor
        v-model="docEditorText"
        lang="json"
        class="doc-editor"
      />
      <div v-if="docEditorError" class="error-msg" style="margin-top:8px">{{ docEditorError }}</div>
      <template #footer>
        <button class="btn btn-default" @click="docDialogVisible = false">{{ t('settings.cancel') }}</button>
        <button class="btn btn-primary" @click="saveDocument" :disabled="docSaving">
          {{ t('redis.save') }}
        </button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { Plus, Pencil, Trash2, Sparkles } from '@lucide/vue'
import { ElMessageBox } from 'element-plus'
import { useI18n } from '../i18n'
import { msg } from '../services/message'
import { chat } from '../services/llm'
import {
  MongoFind,
  MongoInsertOne,
  MongoUpdateOne,
  MongoDeleteOne,
  MongoListIndexes,
  MongoCreateIndex,
  MongoDropIndex,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import type { MongoIndexInfo, MongoQueryResult } from '../types/mongodb'
import SyntaxEditor from './SyntaxEditor.vue'

defineOptions({ name: 'MongoDBCollectionView' })

const { t } = useI18n()

const props = defineProps<{
  sessionId: string
  dbName: string
  collectionName: string
}>()

// ── Editor resize state ──
const topHeight = ref(220)
let topResizeStartY = 0
let topResizeStartHeight = 0
let topResizing = false

function onTopResizeStart(e: MouseEvent) {
  topResizeStartY = e.clientY
  topResizeStartHeight = topHeight.value
  topResizing = true
  document.addEventListener('mousemove', onTopResizeMove)
  document.addEventListener('mouseup', onTopResizeEnd)
}
function onTopResizeMove(e: MouseEvent) {
  const el = (e.target as HTMLElement).closest('.mongo-collection-view')
  const maxTop = el ? el.clientHeight - 100 : 400
  const dy = e.clientY - topResizeStartY
  topHeight.value = Math.max(80, Math.min(maxTop, topResizeStartHeight + dy))
}
function onTopResizeEnd() {
  topResizing = false
  document.removeEventListener('mousemove', onTopResizeMove)
  document.removeEventListener('mouseup', onTopResizeEnd)
}

// ── Query state ──
const activeSubTab = ref<'query' | 'indexes'>('query')
const filterText = ref('{}')
const nlInput = ref('')
const aiGenerating = ref(false)
const queryLimit = ref(100)
const queryLoading = ref(false)
const queryError = ref('')
const queryResult = ref<MongoQueryResult | null>(null)
const currentSkip = ref(0)

const totalDocs = computed(() => queryResult.value?.total || 0)

const tableData = computed(() => {
  if (!queryResult.value) return []
  return queryResult.value.documents.map(d => {
    try { return JSON.parse(d) } catch { return {} }
  })
})

const columns = computed(() => {
  const keySet = new Set<string>()
  for (const row of tableData.value) {
    for (const key of Object.keys(row)) {
      keySet.add(key)
    }
  }
  const cols = Array.from(keySet)
  const idIdx = cols.indexOf('_id')
  if (idIdx > 0) {
    cols.splice(idIdx, 1)
    cols.unshift('_id')
  }
  return cols
})

// ── Index state ──
const indexes = ref<MongoIndexInfo[]>([])
const indexLoading = ref(false)

// ── Dialog state ──
const newIndexDialogVisible = ref(false)
const newIndexName = ref('')
const newIndexFields = ref('')
const newIndexUnique = ref(false)

const docDialogVisible = ref(false)
const docDialogMode = ref<'insert' | 'edit'>('insert')
const docEditorText = ref('{}')
const docEditorError = ref('')
const docSaving = ref(false)
const editingRow = ref<any>(null)

// ── Query methods ──
function onNLKeydown(e: KeyboardEvent) {
  if (e.ctrlKey && e.key === 'Enter') {
    e.preventDefault()
    generateFilter()
  }
}

async function generateFilter() {
  const input = nlInput.value.trim()
  if (!input || !props.dbName || !props.collectionName) return
  aiGenerating.value = true
  try {
    let sample = ''
    try {
      const result = await MongoFind(props.sessionId, props.dbName, props.collectionName, '{}', 0, 1)
      if (result.documents.length > 0) {
        sample = result.documents[0]
      }
    } catch {}

    const schemaContext = sample
      ? `Collection "${props.collectionName}" in database "${props.dbName}". Sample document:\n${sample}`
      : `Collection "${props.collectionName}" in database "${props.dbName}".`

    let result = ''
    await chat({
      system: `You are a MongoDB query assistant. Convert natural language to MongoDB Extended JSON filter only. Output ONLY the JSON filter (no markdown, no explanation). Use operators like $eq, $gt, $gte, $lt, $lte, $in, $nin, $regex, $exists, $and, $or, $not, $elemMatch. Dates should use ISODate format. ObjectIds should use $oid format.`,
      messages: [
        { role: 'user', content: `Schema context:\n${schemaContext}\n\nQuery: ${input}` }
      ],
      onChunk: (chunk: string) => { result += chunk },
    })
    const cleaned = result.trim()
      .replace(/^```[\w]*\n?/i, '')
      .replace(/\n?```$/i, '')
    JSON.parse(cleaned)
    filterText.value = cleaned
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
  aiGenerating.value = false
}

async function executeQuery() {
  if (!props.dbName || !props.collectionName) return
  queryLoading.value = true
  queryError.value = ''
  try {
    queryResult.value = await MongoFind(
      props.sessionId,
      props.dbName,
      props.collectionName,
      filterText.value,
      currentSkip.value,
      queryLimit.value
    )
  } catch (e: any) {
    queryError.value = e?.message || String(e)
    queryResult.value = null
  }
  queryLoading.value = false
}

function cancelQuery() {
  queryLoading.value = false
}

const currentPage = computed(() => Math.floor(currentSkip.value / queryLimit.value) + 1)

function onPageChange(page: number) {
  currentSkip.value = (page - 1) * queryLimit.value
  executeQuery()
}

function onPageSizeChange(size: number) {
  queryLimit.value = size
  currentSkip.value = 0
  executeQuery()
}

// ── Document CRUD ──
function openNewDocument() {
  docDialogMode.value = 'insert'
  docEditorText.value = '{}'
  docEditorError.value = ''
  editingRow.value = null
  docDialogVisible.value = true
}

function onRowClick(row: any) {
  docDialogMode.value = 'edit'
  docEditorText.value = JSON.stringify(row, null, 2)
  docEditorError.value = ''
  editingRow.value = row
  docDialogVisible.value = true
}

async function saveDocument() {
  try {
    JSON.parse(docEditorText.value)
  } catch {
    docEditorError.value = t('mongodb.invalidJSON')
    return
  }
  docEditorError.value = ''
  docSaving.value = true
  try {
    if (docDialogMode.value === 'insert') {
      await MongoInsertOne(props.sessionId, props.dbName, props.collectionName, docEditorText.value)
      msg.success(t('mongodb.insertSuccess'))
    } else {
      const filter = JSON.stringify({ _id: editingRow.value._id })
      const updateObj = JSON.parse(docEditorText.value)
      delete updateObj._id
      await MongoUpdateOne(props.sessionId, props.dbName, props.collectionName, filter, JSON.stringify(updateObj))
      msg.success(t('mongodb.updateSuccess'))
    }
    docDialogVisible.value = false
    executeQuery()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
  docSaving.value = false
}

async function deleteDocument(row: any) {
  try {
    await ElMessageBox.confirm(t('mongodb.deleteConfirm'))
  } catch {
    return
  }
  try {
    const filter = JSON.stringify({ _id: row._id })
    await MongoDeleteOne(props.sessionId, props.dbName, props.collectionName, filter)
    msg.success(t('mongodb.deleteSuccess'))
    executeQuery()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

// ── Indexes ──
async function loadIndexes() {
  if (!props.dbName || !props.collectionName) return
  indexLoading.value = true
  try {
    indexes.value = await MongoListIndexes(props.sessionId, props.dbName, props.collectionName)
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
  indexLoading.value = false
}

function openNewIndexDialog() {
  newIndexName.value = ''
  newIndexFields.value = ''
  newIndexUnique.value = false
  newIndexDialogVisible.value = true
}

async function createIndex() {
  const name = newIndexName.value.trim()
  const fields = newIndexFields.value.trim()
  if (!name || !fields || !props.dbName || !props.collectionName) return
  try {
    await MongoCreateIndex(
      props.sessionId, props.dbName, props.collectionName,
      name, fields.split(',').map(s => s.trim()).filter(Boolean),
      newIndexUnique.value
    )
    newIndexDialogVisible.value = false
    loadIndexes()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

async function dropIndex(name: string) {
  if (!props.dbName || !props.collectionName) return
  try {
    await MongoDropIndex(props.sessionId, props.dbName, props.collectionName, name)
    loadIndexes()
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

// ── Helpers ──
function formatCellValue(val: any): string {
  if (val === null || val === undefined) return 'null'
  if (typeof val === 'object') return JSON.stringify(val)
  return String(val)
}

// ── Lifecycle ──
onMounted(() => {
  executeQuery()
})

onUnmounted(() => {
  if (topResizing) {
    document.removeEventListener('mousemove', onTopResizeMove)
    document.removeEventListener('mouseup', onTopResizeEnd)
  }
})

// Switching collection re-runs the default query
watch(() => [props.dbName, props.collectionName], () => {
  filterText.value = '{}'
  nlInput.value = ''
  currentSkip.value = 0
  queryResult.value = null
  queryError.value = ''
  activeSubTab.value = 'query'
  nextTick(() => executeQuery())
})
</script>

<style scoped>
.mongo-collection-view {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-height: 0;
  position: relative;
}

.mongo-tabs {
  display: flex;
  border-bottom: 1px solid var(--border-subtle);
  padding: 0 8px;
  flex-shrink: 0;
}
.mongo-tab {
  padding: 6px 16px;
  border: none;
  background: none;
  color: var(--text-secondary);
  cursor: pointer;
  font-family: var(--font-ui);
  font-size: 13px;
  border-bottom: 2px solid transparent;
  transition: all 0.15s ease;
}
.mongo-tab:hover {
  color: var(--text-primary);
  background: var(--bg-hover);
}
.mongo-tab.active {
  color: var(--text-primary);
  border-bottom-color: var(--accent);
}

.ai-pulse { animation: fade-pulse 1.2s ease-in-out infinite; }
@keyframes fade-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.3; }
}

.query-section {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-height: 0;
}
.editor-top {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  padding: 8px 8px 0;
}
.editor-row {
  flex: 1;
  display: flex;
  gap: 8px;
  min-height: 0;
}
.nl-panel {
  position: relative;
  flex: 0 0 30%;
  display: flex;
  min-width: 0;
}
.nl-textarea {
  width: 100%;
  height: 100%;
  font-family: var(--font-ui);
  font-size: 13px;
  line-height: 1.5;
  background: var(--bg-base);
  color: var(--text-primary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: 8px 8px 38px 8px;
  resize: none;
  transition: border-color 0.15s ease;
}
.nl-textarea:focus {
  border-color: var(--accent);
  outline: none;
}
.nl-textarea::placeholder {
  color: var(--text-muted);
}
.nl-btn-wrapper {
  position: absolute;
  left: 6px;
  bottom: 6px;
  display: flex;
  align-items: center;
  gap: 6px;
  z-index: 1;
}
.nl-generate-btn {
  padding: 4px 10px;
  font-size: 12px;
}
.query-panel {
  flex: 1;
  display: flex;
  min-width: 0;
}
.query-editor-wrap {
  position: relative;
  flex: 1;
  display: flex;
}
/* Keep query text clear of the overlaying execute button */
.query-editor-wrap :deep(.cm-content) {
  padding-bottom: 44px;
}
.exec-btn-wrapper {
  position: absolute;
  left: 6px;
  bottom: 6px;
  display: flex;
  align-items: center;
  gap: 6px;
  z-index: 1;
}
.exec-btn-overlay {
  padding: 4px 14px;
  font-size: 12px;
}
.shortcut-hint {
  font-family: var(--font-ui);
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
}

.editor-resizer {
  height: 4px;
  cursor: row-resize;
  background: transparent;
  flex-shrink: 0;
  transition: background 0.15s ease;
}
.editor-resizer:hover {
  background: var(--border-subtle);
}

.editor-bottom {
  flex: 1;
  padding: 0 8px 8px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  min-height: 0;
  position: relative;
}

.error-msg {
  color: var(--error);
  padding: 8px;
  background: var(--error-subtle);
  border-radius: var(--radius-sm);
  margin-bottom: 8px;
  user-select: text;
  -webkit-user-select: text;
  cursor: text;
  font-family: var(--font-mono);
  font-size: 13px;
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
  gap: 12px;
}
.spinner {
  width: 28px;
  height: 28px;
  border: 3px solid var(--border-subtle);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
@keyframes spin {
  to { transform: rotate(360deg); }
}
.loading-text {
  font-family: var(--font-ui);
  font-size: 13px;
  color: var(--text-primary);
}

.result-grid {
  flex: 1;
  overflow: auto;
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.result-table-wrap {
  flex: 1;
  overflow: auto;
  min-height: 0;
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
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  word-break: break-all;
  cursor: default;
}
.cell-null {
  color: var(--text-muted);
  font-style: italic;
}
.pagination {
  display: flex;
  justify-content: center;
  gap: 8px;
  margin-top: 8px;
  flex-shrink: 0;
}
.pagination :deep(.el-pager li.is-active) {
  background-color: var(--accent);
  color: var(--on-accent);
}
.pagination :deep(.el-pager li:hover) {
  color: var(--accent);
}
.pagination :deep(.el-pagination .el-select .el-input.is-focus .el-input__wrapper) {
  box-shadow: 0 0 0 1px var(--accent) inset;
}
.pagination :deep(.el-select__input) {
  color: var(--text-primary);
}

.indexes-section {
  flex: 1;
  overflow: auto;
  padding: 8px;
  position: relative;
}

.db-placeholder {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-secondary);
  font-family: var(--font-ui);
  font-size: 14px;
}

.doc-editor {
  height: 320px;
}
</style>
