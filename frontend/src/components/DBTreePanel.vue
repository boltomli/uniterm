<template>
  <div class="db-tree-panel">


    <div class="search-wrap">
      <input
        v-model="searchQuery"
        class="search-input"
        :placeholder="t('db.searchTables')"
      />
      <button class="btn btn-ghost btn-icon btn-sm" :title="t('db.refreshDatabases')" @click="refreshAll">
        <RefreshCw :size="lucideSize('0.875rem')" />
      </button>
      <button class="btn btn-ghost btn-icon btn-sm" :title="t('common.more')" @click.stop="onMoreToggle">
        <MoreHorizontal :size="lucideSize('0.875rem')" />
      </button>
      <Menu ref="moreMenuRef" v-model:visible="moreMenuVisible" align="end">
        <MenuItem :class="{ disabled: !canCreateDatabase }" @click="onMoreNewDatabase">{{ t('db.newDatabase') }}</MenuItem>
        <MenuItem @click="onMoreNewTable">{{ t('db.newTable') }}</MenuItem>
        <MenuDivider />
        <MenuItem @click="onMoreRefresh">{{ t('db.refreshDatabases') }}</MenuItem>
      </Menu>
    </div>
    <div ref="treeContentRef" class="tree-content" @contextmenu.prevent="onTreeContextMenu">
      <div v-if="loading || runningSqlFile" class="tree-loading">{{ t('db.loading') }}</div>
      <template v-for="db in filteredDbs" :key="db.name">
        <div
          class="db-header"
          :data-db-row="db.name"
          :class="{ selected: selectedDb === db.name && !selectedTable }"
          @click="onDbClick(db.name)"
          @dblclick="onDbActivate(db.name)"
          @contextmenu.prevent="onDbContextMenu($event, db.name)"
        >
          <span class="db-arrow" @click.stop="onToggleDb(db.name)">
            <component :is="expandedDbs.has(db.name) ? ChevronDown : ChevronRight" :size="lucideSize('0.75rem')" />
          </span>
          <Database class="db-icon" :size="lucideSize('0.875rem')" />
          <span class="db-name">{{ db.name }}</span>
        </div>
        <template v-if="expandedDbs.has(db.name)">
          <div
            v-for="t in db.tables"
            :key="(t.schema || '') + '/' + t.name"
            class="table-item"
            :data-table-row="db.name + '/' + (t.schema || '') + '/' + t.name"
            :class="{ selected: selectedTable === t.name && selectedDb === db.name }"
            @click="onTableClick(db.name, t.name, t.schema || '')"
            @dblclick="onTableDblClick(db.name, t)"
            @contextmenu.prevent="onTableContextMenu($event, db.name, t)"
          >
            <span class="table-icon-spacer" />
            <component :is="t.type === 'view' ? Eye : Table2" class="table-icon" :size="lucideSize('0.875rem')" />
            <span class="table-name">{{ tableLabel(t) }}</span>
          </div>
          <div v-if="db.tables.length === 0" class="empty-hint">
            {{ t('db.noTables') }}
          </div>
        </template>
      </template>
    </div>

    <Menu
      ref="ctxMenuRef"
      v-model:visible="ctxMenuVisible"
      v-slot="{ current }"
    >
      <template v-if="isDbMenuCtx(current) && current.type === 'db'">
        <MenuItem @click="onCtxListDatabase">{{ t('db.tableList') }}</MenuItem>
        <MenuItem @click="onCtxNewQuery">{{ t('db.newQuery') }}</MenuItem>
        <MenuItem @click="onCtxRunSqlFile">{{ t('db.runSqlFile') }}</MenuItem>
        <MenuDivider />
        <MenuItem v-if="canCreateDatabase" @click="onCtxNewDatabase">{{ t('db.newDatabase') }}</MenuItem>
        <MenuItem @click="onCtxNewTable">{{ t('db.newTable') }}</MenuItem>
        <MenuItem class="danger" @click="onCtxDropDatabase">{{ t('db.dropDatabase') }}</MenuItem>
        <MenuDivider />
        <MenuItem @click="onCtxRefresh">{{ t('db.refreshTables') }}</MenuItem>
      </template>
      <template v-else-if="isDbMenuCtx(current) && current.type === 'table'">
        <MenuItem @click="onCtxViewData">{{ t('db.openData') }}</MenuItem>
        <MenuItem v-if="current.tableType !== 'view'" @click="onCtxViewStructure">{{ t('db.tableStructure') }}</MenuItem>
        <MenuDivider />
        <MenuItem @click="onCtxCopyName">{{ t('db.copyName') }}</MenuItem>
        <MenuItem v-if="(current as DbMenuCtx).tableType !== 'view'" @click="onCtxCopyTable">{{ t('db.copyTable') }}</MenuItem>
        <MenuDivider />
        <MenuItem @click="onCtxExportStructure">{{ t('db.exportStructure') }}</MenuItem>
        <MenuItem v-if="current.tableType !== 'view'" @click="onCtxExportData">{{ t('db.exportData') }}</MenuItem>
        <MenuItem v-if="current.tableType !== 'view'" @click="onCtxExportStructureData">{{ t('db.exportStructureData') }}</MenuItem>
        <MenuDivider />
        <template v-if="current.tableType === 'view'">
          <MenuItem class="danger" @click="onCtxDropView">{{ t('db.dropView') }}</MenuItem>
        </template>
        <template v-else>
          <MenuItem class="danger" @click="onCtxTruncateTable">{{ t('db.truncateTable') }}</MenuItem>
          <MenuItem class="danger" @click="onCtxDropTable">{{ t('db.dropTable') }}</MenuItem>
        </template>
      </template>
      <template v-else-if="isDbMenuCtx(current) && current.type === 'blank'">
        <MenuItem @click="onCtxNewQueryBlank">{{ t('db.newQuery') }}</MenuItem>
        <MenuItem v-if="canCreateDatabase" @click="onCtxNewDatabase">{{ t('db.newDatabase') }}</MenuItem>
        <MenuItem @click="onCtxRefreshDatabases">{{ t('db.refreshDatabases') }}</MenuItem>
      </template>
    </Menu>

    <!-- Confirm dialog -->
    <el-dialog append-to-body
      v-model="confirmVisible"
      :title="confirmTitle"
      width="26.25rem"
    >
      <div class="confirm-body">
        <p class="confirm-text">{{ confirmText }}</p>
        <p class="confirm-hint">{{ t('db.typeToConfirm', { name: confirmName }) }}</p>
        <el-input v-model="confirmInput" :placeholder="confirmName" />
      </div>
      <template #footer>
        <el-button @click="confirmVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="danger" :disabled="confirmInput !== confirmName" @click="onConfirm">
          {{ t('common.confirm') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- New Database dialog -->
    <el-dialog append-to-body v-model="newDbVisible" :title="t('db.newDatabase')" width="23.75rem">
      <el-form label-width="5rem">
        <el-form-item :label="t('db.dbName')">
          <el-input v-model="newDbName" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="newDbVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :disabled="!newDbName.trim()" @click="onCreateDatabase">
          {{ t('common.save') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- New Table dialog -->
    <el-dialog append-to-body v-model="newTableVisible" :title="t('db.newTable')" width="23.75rem">
      <el-form label-width="5rem">
        <el-form-item :label="t('db.tableName')">
          <el-input v-model="newTableName" />
        </el-form-item>
        <el-form-item :label="t('db.databases')">
          <el-select v-model="newTableDb" style="width:100%">
            <el-option v-for="d in databases" :key="d.name" :label="d.name" :value="d.name" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="newTableVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :disabled="!newTableName.trim() || !newTableDb" @click="onCreateTable">
          {{ t('common.save') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Copy Table dialog -->
    <el-dialog append-to-body v-model="copyTableVisible" :title="t('db.copyTable')" width="23.75rem">
      <el-form label-width="5rem">
        <el-form-item :label="t('db.sourceTable')">
          <el-input :model-value="copySourceTable" disabled />
        </el-form-item>
        <el-form-item :label="t('db.tableName')">
          <el-input v-model="copyTableName" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="copyTableVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :disabled="!copyTableName.trim()" @click="onCopyTable">
          {{ t('common.confirm') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { lucideSize } from '../utils/lucideSize'
import { detectPlatformSync } from '../utils/platform'
import { ref, watch, computed, nextTick, h } from 'vue'
import { Database, Table2, Eye, ChevronRight, ChevronDown, RefreshCw, MoreHorizontal } from '@lucide/vue'
import { useI18n } from '../i18n'
import { ElMessageBox } from 'element-plus'
import { GetDatabases, GetTables, CreateDatabase, DropDatabase, CreateTable, DropTable, DropView, TruncateTable, CopyTable, GetDBCapabilities, DumpTable, DumpTableToFile, SaveFileDialogFiltered, OpenFileDialogFiltered, ReadFileBase64, ExecuteSQLScript } from '../../bindings/github.com/ys-ll/uniterm/app'
import { msg } from '../services/message'
import { useDbObjectActions } from '../composables/useDbObjectActions'
import Menu from './Menu.vue'
import MenuItem from './MenuItem.vue'
import MenuDivider from './MenuDivider.vue'
import type { TableInfo } from '../types/database'
import { tryAndroidSave } from '../utils/androidSave'

const { t } = useI18n()

interface DbEntry {
  name: string
  tables: TableInfo[]
  loaded: boolean
}

const props = defineProps<{
  sessionId: string
  defaultDbName?: string
  dbType?: string
  activeDb?: string
  activeTable?: string
}>()

const caps = ref<Record<string, any> | null>(null)
const canCreateDatabase = computed(() => caps.value?.['supportsCreateDatabase'] ?? true)

async function loadCapabilities() {
  try {
    caps.value = await GetDBCapabilities(props.sessionId)
  } catch (e) {
    console.error('Failed to load capabilities:', e)
  }
}

watch(() => props.sessionId, () => {
  if (props.sessionId) loadCapabilities()
}, { immediate: true })

const emit = defineEmits<{
  selectTable: [dbName: string, tableName: string, isView?: boolean, schema?: string]
  openDatabase: [dbName: string, tab?: 'query' | 'objects']
  viewStructure: [dbName: string, tableName: string, schema?: string]
  newQuery: [dbName?: string]
  objectRemoved: [payload: { dbName: string; tableName?: string; kind: 'table' | 'view' | 'database' }]
}>()

const databases = ref<DbEntry[]>([])
const expandedDbs = ref(new Set<string>())
const selectedDb = ref('')
const selectedTable = ref('')
const searchQuery = ref('')
const loading = ref(false)
// DB-17: loadTree's awaits have no ordering guarantee — a rapid session
// switch or repeated refresh can interleave GetDatabases/GetTables calls and
// an old response would repaint the tree for the new session. Each run owns a
// token; starting a newer run (or switching session) invalidates the older.
let treeSeq = 0
// Auto-opening a database also focuses the objects tab in the parent — that
// must happen only on the FIRST successful load of a session; a refresh must
// not steal the user back from the table/query they are looking at.
let autoOpened = false
const treeContentRef = ref<HTMLElement | null>(null)

async function loadTree() {
  if (!props.sessionId) return
  const myTree = ++treeSeq
  const sid = props.sessionId
  loading.value = true
  try {
    const dbs: string[] = await GetDatabases(sid)
    // Stale response (newer refresh / session switch already started).
    if (myTree !== treeSeq || sid !== props.sessionId) return
    // Auto-expand the connection's default db only when it's actually a real
    // entry in the list. Oracle's dbName is the service name and never
    // matches — that case falls through to the oracle branch below.
    let autoOpen = ''
    if (props.defaultDbName && dbs.includes(props.defaultDbName)) {
      const tables = await GetTables(sid, props.defaultDbName)
      if (myTree !== treeSeq || sid !== props.sessionId) return
      // 默认库置顶并展开（已加载表），其余可访问的数据库折叠显示，
      // 点击展开时再懒加载表（见 ensureDbExpanded）。
      const otherDbs = dbs.filter(d => d !== props.defaultDbName)
      databases.value = [
        { name: props.defaultDbName, tables, loaded: true },
        ...otherDbs.map(d => ({ name: d, tables: [] as TableInfo[], loaded: false }))
      ]
      expandedDbs.value = new Set([props.defaultDbName])
      autoOpen = props.defaultDbName
    } else if (dbs.length === 1) {
      // Single schema/db (e.g. Oracle showing only the current schema) — expand it.
      const only = dbs[0]
      const tables = await GetTables(sid, only)
      if (myTree !== treeSeq || sid !== props.sessionId) return
      databases.value = [{ name: only, tables, loaded: true }]
      expandedDbs.value = new Set([only])
      autoOpen = only
    } else if (dbs.length > 0 && props.dbType === 'oracle') {
      // Oracle's dbName is the service name (e.g. FREEPDB1), which never
      // matches a schema name — without this the tree would stay fully
      // collapsed. Expand and open the first schema (the login user's
      // default) and lazy-load the rest.
      const first = dbs[0]
      const tables = await GetTables(sid, first)
      if (myTree !== treeSeq || sid !== props.sessionId) return
      databases.value = [
        { name: first, tables, loaded: true },
        ...dbs.slice(1).map((d: string) => ({ name: d, tables: [] as TableInfo[], loaded: false }))
      ]
      expandedDbs.value = new Set([first])
      autoOpen = first
    } else {
      databases.value = dbs.map((db: string) => ({ name: db, tables: [], loaded: false }))
      expandedDbs.value = new Set()
    }
    if (autoOpen && !autoOpened) {
      // First load of this session only: open the db AND move the parent's
      // focus to the objects tab. Refreshes just repaint the tree.
      autoOpened = true
      selectedDb.value = autoOpen
      selectedTable.value = ''
      emit('openDatabase', autoOpen, 'objects')
    }
  } catch (e) {
    if (myTree !== treeSeq) return // a newer run owns the UI now
    console.error('Failed to load tree:', e)
    // A silent console-only failure leaves an empty tree with no clue why (DB-17).
    msg.error(t('db.treeLoadFailed', { msg: (e as any)?.message || String(e) }))
  } finally {
    if (myTree === treeSeq) loading.value = false
  }
}

watch(() => props.sessionId, (newId) => {
  // Switching sessions invalidates everything the old one loaded: drop
  // in-flight responses (DB-17) and clear the visible tree so no stale
  // databases/selection from the previous connection linger (DB-18 neighbour).
  treeSeq++
  autoOpened = false
  databases.value = []
  expandedDbs.value = new Set()
  selectedDb.value = ''
  selectedTable.value = ''
  if (newId) loadTree()
}, { immediate: true })

// Sync tree highlight/position when the active db/table changes from outside
// (breadcrumb navigation, object list, etc.)
watch(() => [props.activeDb, props.activeTable], async ([db, table]) => {
  if (!db) return
  selectedDb.value = db
  selectedTable.value = table || ''
  if (table && !expandedDbs.value.has(db)) {
    expandedDbs.value.add(db)
    const entry = databases.value.find(d => d.name === db)
    if (entry && !entry.loaded) {
      try {
        entry.tables = await GetTables(props.sessionId, db)
        entry.loaded = true
      } catch { /* ignore */ }
    }
    expandedDbs.value = new Set(expandedDbs.value)
  }
  await nextTick()
  scrollActiveIntoView()
})

function scrollActiveIntoView() {
  const root = treeContentRef.value
  if (!root || !selectedDb.value) return
  const key = selectedTable.value ? `${selectedDb.value}/${selectedTable.value}` : selectedDb.value
  const attr = selectedTable.value ? 'data-table-row' : 'data-db-row'
  const esc = key.replace(/\\/g, '\\\\').replace(/"/g, '\\"')
  const el = root.querySelector(`[${attr}="${esc}"]`) as HTMLElement | null
  el?.scrollIntoView({ block: 'nearest' })
}

async function ensureDbExpanded(dbName: string) {
  if (expandedDbs.value.has(dbName)) return
  expandedDbs.value.add(dbName)
  const db = databases.value.find(d => d.name === dbName)
  if (db && !db.loaded) {
    try {
      db.tables = await GetTables(props.sessionId, dbName)
      db.loaded = true
    } catch (e) {
      console.error('Failed to load tables:', e)
    }
  }
  expandedDbs.value = new Set(expandedDbs.value)
}

async function onDbActivate(dbName: string) {
  selectedDb.value = dbName
  selectedTable.value = ''
  await ensureDbExpanded(dbName)
  emit('openDatabase', dbName, 'objects')
}

async function onDbClick(dbName: string) {
  await onDbActivate(dbName)
}

async function onToggleDb(dbName: string) {
  if (expandedDbs.value.has(dbName)) {
    expandedDbs.value.delete(dbName)
    expandedDbs.value = new Set(expandedDbs.value)
    return
  }
  await ensureDbExpanded(dbName)
}

function onTableClick(dbName: string, tableName: string, schema = '') {
  const table = databases.value.find(d => d.name === dbName)?.tables.find(t => t.name === tableName && (t.schema || '') === schema)
  selectedDb.value = dbName
  selectedTable.value = tableName
  emit('selectTable', dbName, tableName, table?.type === 'view', schema)
}

function onTableDblClick(dbName: string, table: TableInfo) {
  selectedDb.value = dbName
  selectedTable.value = table.name
  emit('selectTable', dbName, table.name, table.type === 'view', table.schema || '')
}

/** Tree label: a schema-qualified name where the dialect has schemas
 *  (DB-06/DB-07) so two schemas' `users` are no longer indistinguishable. */
function tableLabel(tb: { name: string; schema?: string }) {
  return tb.schema ? `${tb.schema}.${tb.name}` : tb.name
}

const filteredDbs = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return databases.value
  return databases.value.map(db => ({
    ...db,
    tables: db.tables.filter(t => t.name.toLowerCase().includes(q) || (t.schema || '').toLowerCase().includes(q))
  }))
})

watch(searchQuery, (q) => {
  if (q.trim()) {
    const all = new Set(databases.value.map(d => d.name))
    for (const db of databases.value) {
      if (!db.loaded) {
        GetTables(props.sessionId, db.name).then((tables: TableInfo[]) => {
          db.tables = tables
          db.loaded = true
        }).catch(() => {})
      }
    }
    expandedDbs.value = all
  }
})

// ── Context menu ──

interface DbMenuCtx {
  type: 'db' | 'table' | 'blank'
  dbName: string
  tableName: string
  tableType: string
}

function isDbMenuCtx(v: unknown): v is DbMenuCtx {
  return typeof v === 'object' && v !== null && 'type' in v
}

const ctxMenuVisible = ref(false)
const ctxMenuRef = ref<InstanceType<typeof Menu> | null>(null)
const ctxDbName = ref('')
const ctxTableName = ref('')
// Schema of the right-clicked table (DB-06/DB-07): drop/view/truncate/copy// must act on the table the user pointed at, not a namesake elsewhere.
const ctxTableSchema = ref('')
const ctxTableType = ref('')

function openDbMenu(e: MouseEvent, item: DbMenuCtx) {
  ctxMenuRef.value?.openAt(e.clientX, e.clientY, item)
}

function onDbContextMenu(e: MouseEvent, dbName: string) {
  ctxDbName.value = dbName
  ctxTableName.value = ''
  openDbMenu(e, { type: 'db', dbName, tableName: '', tableType: '' })
}

function onTableContextMenu(e: MouseEvent, dbName: string, table: TableInfo) {
  selectedDb.value = dbName
  selectedTable.value = table.name
  // 右键表节点时直接切换到该表数据页，同时弹出菜单
  emit('selectTable', dbName, table.name, table.type === 'view', table.schema || '')
  ctxDbName.value = dbName
  ctxTableName.value = table.name
  ctxTableSchema.value = table.schema || ''
  ctxTableType.value = table.type || ''
  openDbMenu(e, { type: 'table', dbName, tableName: table.name, tableType: table.type || '' })
}

function onTreeContextMenu(e: MouseEvent) {
  const target = e.target as HTMLElement
  if (target.closest('.db-header') || target.closest('.table-item')) return
  ctxDbName.value = ''
  ctxTableName.value = ''
  openDbMenu(e, { type: 'blank', dbName: '', tableName: '', tableType: '' })
}

function onCtxListDatabase() {
  emit('openDatabase', ctxDbName.value, 'objects')
  ctxMenuVisible.value = false
}

function onCtxNewQuery() {
  emit('newQuery', ctxDbName.value)
  ctxMenuVisible.value = false
}

function onCtxNewQueryBlank() {
  emit('newQuery')
  ctxMenuVisible.value = false
}

function onCtxViewData() {
  emit('selectTable', ctxDbName.value, ctxTableName.value, ctxTableType.value === 'view', ctxTableSchema.value)
  ctxMenuVisible.value = false
}

function onCtxViewStructure() {
  emit('viewStructure', ctxDbName.value, ctxTableName.value, ctxTableSchema.value)
  ctxMenuVisible.value = false
}

function onCtxCopyName() {
  navigator.clipboard.writeText(ctxTableName.value).catch(() => {})
  ctxMenuVisible.value = false
}

// ── Export table as .sql ──

// Rows beyond this cap are not exported (DB-12): the backend streams
// straight to disk, so the cap bounds time/disk rather than memory — but an
// unbounded export of a partition-sized table is still a foot-gun.
const EXPORT_MAX_ROWS = 1_000_000

async function exportTable(withStructure: boolean, withData: boolean) {
  ctxMenuVisible.value = false
  const dbName = ctxDbName.value
  const tableName = ctxTableName.value
  try {
    // Android: save through the system "save to" picker, which needs the
    // bytes up front — the streamed backend path writes to a filesystem
    // path the picker flow cannot supply, so this platform keeps the
    // in-memory round trip.
    if (detectPlatformSync() === 'android') {
      const sql = await DumpTable(props.sessionId, dbName, tableName, withStructure, withData)
      const outcome = await tryAndroidSave(tableName + '.sql', 'text/plain', encodeBase64(sql))
      if (outcome === 'saved') msg.success(t('db.exportDone', { name: tableName }))
      return
    }
    const path = await SaveFileDialogFiltered(t('db.exportSQL'), tableName + '.sql', 'SQL File', '*.sql')
    if (!path) return
    // The backend streams the dump straight to the file (DB-12) — nothing
    // round-trips through the webview as base64 anymore.
    const rows = await DumpTableToFile(props.sessionId, dbName, tableName, withStructure, withData, path, EXPORT_MAX_ROWS)
    msg.success(t('db.exportDone', { name: `${path.split(/[\\/]/).pop()} (${rows})` }))
  } catch (e: any) {
    msg.error(e?.message || String(e))
  }
}

function onCtxExportStructure() {
  exportTable(true, false)
}

function onCtxExportData() {
  exportTable(false, true)
}

function onCtxExportStructureData() {
  exportTable(true, true)
}

// ── Run SQL file (database context menu) ──

// Wails v3 rejects the picker promise with "cancelled by user" when the
// dialog is dismissed, instead of resolving with an empty path.
function isDialogCancel(e: unknown): boolean {
  return String(e).toLowerCase().includes('cancel')
}

function decodeBase64(b64: string): string {
  try {
    const bin = atob(b64)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    return new TextDecoder('utf-8').decode(bytes)
  } catch {
    return ''
  }
}

const runningSqlFile = ref(false)

async function onCtxRunSqlFile() {
  ctxMenuVisible.value = false
  const dbName = ctxDbName.value
  let path = ''
  try {
    path = await OpenFileDialogFiltered(t('db.runSqlFile'), 'SQL File', '*.sql')
  } catch (e) {
    if (!isDialogCancel(e)) msg.error((e as any)?.message || String(e))
    return
  }
  if (!path) return
  try {
    runningSqlFile.value = true
    const b64 = await ReadFileBase64(path)
    const text = decodeBase64(b64)
    const result = await ExecuteSQLScript(props.sessionId, dbName, text)
    if (result?.failedLine) {
      showScriptFailure(result)
    } else {
      const name = path.split(/[\\/]/).pop() || path
      msg.success(t('db.scriptExecuted', { n: result?.executed ?? 0 }) + ` · ${t('db.affectedRows')}: ${result?.affectedTotal ?? 0} · ${name}`)
    }
  } catch (e: any) {
    msg.error(e?.message || String(e))
  } finally {
    runningSqlFile.value = false
  }
}

// Failure detail dialog: line number alone doesn't tell the user what went
// wrong (e.g. a duplicate-key INSERT on a rerun). Show the driver error and
// the failing statement, matching the query editor's script error panel.
function showScriptFailure(result: { failedLine: number; failedSql?: string; error?: string }) {
  const children = [h('p', { style: 'margin:0;font-weight:600' }, t('db.scriptFailedLine', { line: result.failedLine }))]
  if (result.error) {
    children.push(h('p', { style: 'margin:6px 0 0;color:var(--el-color-error);word-break:break-word;font-family:var(--font-mono,monospace);font-size:12px' }, result.error))
  }
  if (result.failedSql) {
    children.push(h('pre', { style: 'margin:8px 0 0;padding:8px;background:var(--bg-base,#f5f5f5);border-radius: var(--radius-sm);font-family:var(--font-mono,monospace);font-size:12px;white-space:pre-wrap;word-break:break-word;max-height:180px;overflow:auto' }, result.failedSql))
  }
  ElMessageBox.alert(h('div', { style: 'display:flex;flex-direction:column' }, children), t('db.runSqlFile'), {
    confirmButtonText: t('common.confirm'),
    type: 'error',
    customStyle: { maxWidth: '35rem', width: '35rem' },
  }).catch(() => {})
}

// ── Copy table ──

const copyTableVisible = ref(false)
const copySourceTable = ref('')
const copyTableName = ref('')

function onCtxCopyTable() {
  ctxMenuVisible.value = false
  copySourceTable.value = ctxTableName.value
  copyTableName.value = ctxTableName.value + '_copy'
  copyTableVisible.value = true
}

async function onCopyTable() {
  const dbName = ctxDbName.value
  const schema = ctxTableSchema.value
  const newName = copyTableName.value.trim()
  if (!copySourceTable.value || !newName) return
  try {
    await CopyTable(props.sessionId, dbName, schema, copySourceTable.value, newName)
    copyTableVisible.value = false
    msg.success(t('db.copyTableDone', { name: newName }))
    await refreshDb(dbName)
  } catch (e: any) {
    console.error('Failed to copy table:', e)
    msg.error(e?.message || String(e))
  }
}

function encodeBase64(text: string): string {
  const bytes = new TextEncoder().encode(text)
  let bin = ''
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
  return btoa(bin)
}

// ── Confirm dialog (shared object-action flow, DB-28) ──

const {
  confirmVisible,
  confirmTitle,
  confirmText,
  confirmName,
  confirmInput,
  onConfirm,
  runObjectAction,
} = useDbObjectActions({
  // Refreshing the tree is the single post-action step; objectRemoved is
  // bubbled per action through the same composable flow.
  onRefresh: () => loadTree(),
  onObjectRemoved: (payload) => emit('objectRemoved', payload),
})

function onCtxDropDatabase() {
  ctxMenuVisible.value = false
  const dbName = ctxDbName.value
  runObjectAction(
    { dbName, kind: 'database' },
    { title: t('db.dropDatabase'), text: t('db.dropDatabaseConfirm', { name: dbName }), name: dbName },
    async () => { await DropDatabase(props.sessionId, dbName) },
  )
}

function onCtxDropTable() {
  ctxMenuVisible.value = false
  const dbName = ctxDbName.value
  const tableName = ctxTableName.value
  const schema = ctxTableSchema.value
  runObjectAction(
    { dbName, tableName, kind: 'table' },
    { title: t('db.dropTable'), text: t('db.dropTableConfirm', { name: tableName }), name: tableName },
    async () => { await DropTable(props.sessionId, dbName, schema, tableName) },
  )
}

function onCtxDropView() {
  ctxMenuVisible.value = false
  const dbName = ctxDbName.value
  const tableName = ctxTableName.value
  const schema = ctxTableSchema.value
  runObjectAction(
    { dbName, tableName, kind: 'view' },
    { title: t('db.dropView'), text: t('db.dropViewConfirm', { name: tableName }), name: tableName },
    async () => { await DropView(props.sessionId, dbName, schema, tableName) },
  )
}

function onCtxTruncateTable() {
  ctxMenuVisible.value = false
  const dbName = ctxDbName.value
  const tableName = ctxTableName.value
  const schema = ctxTableSchema.value
  runObjectAction(
    { dbName, tableName, kind: 'table' },
    { title: t('db.truncateTable'), text: t('db.truncateTableConfirm', { name: tableName }), name: tableName },
    async () => { await TruncateTable(props.sessionId, dbName, schema, tableName) },
  )
}

async function onCtxRefresh() {
  ctxMenuVisible.value = false
  const db = databases.value.find(d => d.name === ctxDbName.value)
  if (db) {
    try {
      db.tables = await GetTables(props.sessionId, ctxDbName.value)
      db.loaded = true
    } catch (e) {
      console.error('Failed to refresh:', e)
    }
  }
}

async function onCtxRefreshDatabases() {
  ctxMenuVisible.value = false
  await loadTree()
}

// Refresh a single database's tables (called from parent after external mutations)
async function refreshDb(dbName: string) {
  const db = databases.value.find(d => d.name === dbName)
  if (db) {
    try {
      db.tables = await GetTables(props.sessionId, dbName)
      db.loaded = true
    } catch (e) {
      console.error('Failed to refresh db:', e)
    }
  }
}

defineExpose({ refreshDb })

// ── Toolbar (search box) refresh + more menu ──

const moreMenuRef = ref<InstanceType<typeof Menu> | null>(null)
const moreMenuVisible = ref(false)

function onMoreToggle(e: MouseEvent) {
  if (e.currentTarget instanceof HTMLElement) moreMenuRef.value?.toggle(e.currentTarget)
}

function refreshAll() {
  loadTree()
}

function onMoreNewDatabase() {
  moreMenuVisible.value = false
  newDbName.value = ''
  newDbVisible.value = true
}

function onMoreNewTable() {
  moreMenuVisible.value = false
  newTableDb.value = selectedDb.value || databases.value[0]?.name || ''
  newTableName.value = ''
  newTableVisible.value = true
}

function onMoreRefresh() {
  moreMenuVisible.value = false
  loadTree()
}

// ── New Database / Table dialogs ──

const newDbVisible = ref(false)
const newDbName = ref('')

function onCtxNewDatabase() {
  ctxMenuVisible.value = false
  newDbName.value = ''
  newDbVisible.value = true
}

async function onCreateDatabase() {
  if (!newDbName.value.trim()) return
  try {
    await CreateDatabase(props.sessionId, newDbName.value.trim())
    newDbVisible.value = false
    await loadTree()
  } catch (e: any) {
    console.error('Failed to create database:', e)
    msg.error(e?.message || String(e))
  }
}

const newTableVisible = ref(false)
const newTableName = ref('')
const newTableDb = ref('')

function onCtxNewTable() {
  ctxMenuVisible.value = false
  newTableDb.value = ctxDbName.value
  newTableName.value = ''
  newTableVisible.value = true
}

async function onCreateTable() {
  if (!newTableName.value.trim() || !newTableDb.value) return
  try {
    await CreateTable(props.sessionId, newTableDb.value, newTableName.value.trim())
    newTableVisible.value = false
    const db = databases.value.find(d => d.name === newTableDb.value)
    if (db) {
      db.tables = await GetTables(props.sessionId, newTableDb.value)
      db.loaded = true
      expandedDbs.value = new Set([...expandedDbs.value, newTableDb.value])
    }
  } catch (e: any) {
    console.error('Failed to create table:', e)
    msg.error(e?.message || String(e))
  }
}
</script>

<style scoped>
.db-tree-panel {
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.panel-header {
  padding: 0.5rem 0.75rem 0.25rem;
  font-family: var(--font-ui);
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--text-secondary);
  text-transform: uppercase;
  flex-shrink: 0;
}
.search-wrap {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.25rem 0.5rem;
  flex-shrink: 0;
}
.search-input {
  flex: 1;
  min-width: 0;
  padding: 0.25rem 0.5rem;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--bg-base);
  color: var(--text-primary);
  font-family: var(--font-ui);
  font-size: 0.75rem;
  outline: none;
  transition: border-color 0.15s ease;
}
.search-input:focus {
  border-color: var(--accent);
}
.search-input::placeholder {
  color: var(--text-muted);
}
.tree-content {
  flex: 1;
  overflow: auto;
}
.tree-loading {
  padding: 0.75rem;
  color: var(--text-secondary);
  font-family: var(--font-ui);
  font-size: 0.75rem;
  text-align: center;
}
.db-header {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.375rem 0.5rem;
  user-select: none;
  transition: background 0.12s ease;
}
.db-header:hover {
  background: var(--bg-hover);
}
.db-header.selected {
  background: var(--bg-hover);
}
.db-arrow {
  width: 0.75rem;
  flex-shrink: 0;
  color: var(--text-muted);
  display: flex;
  align-items: center;
}
.db-arrow:hover {
  color: var(--text-primary);
}
.db-icon {
  flex-shrink: 0;
  color: var(--text-muted);
}
.db-name {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.table-item {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.375rem 0.5rem;
  user-select: none;
  transition: background 0.12s ease;
}
.table-item:hover {
  background: var(--bg-hover);
}
.table-item.selected {
  background: var(--bg-hover);
}
.table-icon-spacer {
  width: 1.875rem;
  flex-shrink: 0;
}
.table-icon {
  flex-shrink: 0;
  color: var(--text-muted);
}
.table-name {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.empty-hint {
  padding: 0.25rem 0.5rem 0.25rem 1.75rem;
  font-family: var(--font-ui);
  font-size: 0.75rem;
  color: var(--text-muted);
}

/* Confirm dialog */
.confirm-body {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.confirm-text {
  font-family: var(--font-ui);
  font-size: 0.875rem;
  color: var(--text-primary);
  margin: 0;
}
.confirm-hint {
  font-family: var(--font-ui);
  font-size: 0.75rem;
  color: var(--text-muted);
  margin: 0;
}
</style>
