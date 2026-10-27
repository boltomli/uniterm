<template>
  <div
    ref="gridRootEl"
    class="db-result-grid"
    tabindex="0"
    @keydown="onGridKeydown"
  >
    <el-table
      :key="tableKey"
      ref="tableRef"
      :data="rows"
      border
      size="small"
      height="100%"
      style="width: 100%"
      class="db-result-table"
      :empty-text="emptyText"
      :cell-class-name="cellClassName"
      @sort-change="onSortChange"
      @cell-dblclick="onCellDblClick"
      @cell-click="onCellClick"
      @cell-contextmenu="onCellContextMenu"
    >
      <el-table-column
        v-for="col in columns"
        :key="col.name"
        :prop="col.name"
        :label="colTitle(col)"
        :min-width="uiPx(110)"
        sortable="custom"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          <div
            v-if="canEdit && editingRow === row && editingField === col.name"
            class="cell-edit-wrap"
            @mousedown.stop
          >
            <input
              ref="cellInputEl"
              v-model="draftValue"
              class="cell-edit-input"
              :placeholder="isNullable(col.name) ? t('db.null') : ''"
              @input="onDraftInput"
              @keydown.enter.prevent="onCellEditConfirm"
              @keydown.escape.prevent="onCellEditCancel"
              @blur="onCellEditConfirm"
            />
            <button
              v-if="isNullable(col.name)"
              type="button"
              class="null-btn"
              title="N"
              @mousedown.prevent
              @click.prevent="setDraftNull"
            >
              N
            </button>
          </div>
          <span
            v-else-if="row[col.name] === null"
            class="cell-null"
            :class="{ 'cell-pending': isPending(row, col.name) }"
          >{{ t('db.null') }}</span>
          <span
            v-else
            class="cell-value"
            :class="{ 'cell-pending': isPending(row, col.name) }"
          >{{ formatCell(row[col.name]) }}</span>
        </template>
      </el-table-column>

      <el-table-column
        v-if="canEdit"
        :label="actionsLabel"
        :width="uiPx(100)"
        fixed="right"
        class-name="db-action-cell"
        :resizable="false"
      >
        <template #default="{ row }">
          <div class="row-actions" @mousedown.stop @click.stop>
            <button
              type="button"
              class="btn btn-ghost btn-icon btn-sm"
              :title="editLabel"
              @click.stop.prevent="emit('edit-row', row)"
            >
              <Pencil :size="'0.875rem'" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-icon btn-sm"
              :title="duplicateLabel"
              @click.stop.prevent="emit('duplicate-row', row)"
            >
              <Copy :size="'0.875rem'" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-icon btn-sm danger"
              :title="deleteLabel"
              @click.stop.prevent="emit('delete-row', row)"
            >
              <Trash2 :size="'0.875rem'" />
            </button>
          </div>
        </template>
      </el-table-column>
    </el-table>

    <!-- 单元格右键菜单：复制单元格 / 复制整行 / 复制列名 -->
    <Menu ref="cellMenuRef" v-model:visible="cellMenuVisible">
      <MenuItem :class="{ disabled: !selectedCell }" @click="copySelectedCell">
        {{ t('db.copyCell') }}
      </MenuItem>
      <MenuItem :class="{ disabled: !selectedCell }" @click="copySelectedRow">
        {{ t('db.copyRow') }}
      </MenuItem>
      <MenuItem :class="{ disabled: !selectedCell }" @click="copySelectedColumnName">
        {{ t('db.copyColumnName') }}
      </MenuItem>
    </Menu>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, shallowReactive, shallowRef, ref, watch } from 'vue'
import { Pencil, Copy, Trash2 } from '@lucide/vue'
import type { QueryResultColumn, ColumnInfo } from '../types/database'
import { useI18n } from '../i18n'
import { uiPx } from '../utils/uiScale'
import { writeClipboard } from '../composables/useClipboardWrite'
import Menu from './Menu.vue'
import MenuItem from './MenuItem.vue'


const { t } = useI18n()

const props = defineProps<{
  rows: Record<string, any>[]
  columns: QueryResultColumn[]
  canEdit: boolean
  primaryKeys?: string[]
  tableColumns?: ColumnInfo[]
  emptyText?: string
  actionsLabel?: string
  editLabel?: string
  duplicateLabel?: string
  deleteLabel?: string
}>()

const emit = defineEmits<{
  /** A staged, unsaved cell edit changed; payload = number of pending edits. */
  'pending-change': [count: number]
  'edit-row': [row: Record<string, any>]
  'duplicate-row': [row: Record<string, any>]
  'delete-row': [row: Record<string, any>]
  'sort-change': [payload: { field: string; order: 'asc' | 'desc' | null }]
  'editing-change': [editing: boolean]
}>()

const tableRef = ref<any>(null)
const cellInputEl = ref<HTMLInputElement | null>(null)


const tableKey = computed(() => `edit-${props.canEdit ? 1 : 0}-cols-${props.columns.map(c => c.name).join('|')}`)

// ── 单元格选中与复制 ──
// 数据库结果网格的单元格是 div 渲染，浏览器原生 Ctrl+C 只对文本拖选生效。
// 这里补齐桌面数据库客户端（DBeaver/Navicat）的标准交互：
//   单击选中 → Ctrl/Cmd+C 复制该格 → 右键菜单提供格/行/列名复制。
const gridRootEl = ref<HTMLElement | null>(null)
const selectedCell = shallowRef<{ row: Record<string, any>; field: string } | null>(null)

function selectCell(row: Record<string, any>, field: string) {
  selectedCell.value = { row, field }
  // 容器获得焦点，keydown（Ctrl+C）才能路由到这里；不抢编辑态 input 的焦点。
  if (!editingRow.value) gridRootEl.value?.focus({ preventScroll: true })
}

function isCellSelected(row: Record<string, any>, field: string): boolean {
  const sel = selectedCell.value
  return !!sel && sel.row === row && sel.field === field
}

/** el-table 单元格点击：选中该格。 */
function onCellClick(row: Record<string, any>, column?: { property?: string }) {
  const field = column?.property
  if (!field || !props.columns.some(c => c.name === field)) return
  selectCell(row, field)
}

/** el-table 单元格右键：选中并打开菜单（element-plus 传参 row/column/cell/event）。 */
function onCellContextMenu(
  row: Record<string, any>,
  column: { property?: string },
  _cell: HTMLElement,
  event: MouseEvent,
) {
  const field = column?.property
  if (!field || !props.columns.some(c => c.name === field)) return
  event.preventDefault()
  event.stopPropagation()
  selectCell(row, field)
  openCellMenu(event.clientX, event.clientY)
}

const cellMenuRef = ref<InstanceType<typeof Menu> | null>(null)
const cellMenuVisible = ref(false)
function openCellMenu(x: number, y: number) {
  cellMenuVisible.value = true
  cellMenuRef.value?.openAt(x, y)
}

/** 单元格选中高亮（cellClassName 挂在 el-table 的 td 上）。 */
function cellClassName({ row, columnIndex }: { row: Record<string, any>; columnIndex: number }) {
  const col = props.columns[columnIndex]
  if (col && isCellSelected(row, col.name)) return 'db-cell-selected'
  return ''
}

function copySelectedCell() {
  const sel = selectedCell.value
  if (sel) writeClipboard(formatCell(sel.row[sel.field]))
  cellMenuVisible.value = false
}

/** 整行复制为 TSV（Tab 分隔），可直接粘贴进 Excel/Numbers 分列。 */
function copySelectedRow() {
  const sel = selectedCell.value
  if (sel) {
    const line = props.columns.map(c => formatCell(sel.row[c.name])).join('\t')
    writeClipboard(line)
  }
  cellMenuVisible.value = false
}

function copySelectedColumnName() {
  const sel = selectedCell.value
  if (sel) writeClipboard(sel.field)
  cellMenuVisible.value = false
}

/**
 * 网格容器 keydown：Ctrl/Cmd+C 复制选中单元格。用户用鼠标拖选了真实文本时
 * 放行默认复制（尊重手动选区），与终端"有选区才复制"的语义一致。
 */
function onGridKeydown(e: KeyboardEvent) {
  if (e.type !== 'keydown' || (e.key !== 'c' && e.key !== 'C')) return
  if (!(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) return
  // 正在编辑单元格时，input 的默认复制优先。
  if (editingRow.value) return
  const text = window.getSelection()?.toString() ?? ''
  if (text) return // 用户拖选了文本，走浏览器默认复制
  const sel = selectedCell.value
  if (!sel) return
  e.preventDefault()
  writeClipboard(formatCell(sel.row[sel.field]))
}

/** Currently edited cell (row + column name). */
const editingRow = shallowRef<Record<string, any> | null>(null)
const editingField = ref<string | null>(null)
const draftValue = ref('')
const draftIsNull = ref(false)
let snapOldValue: any = undefined

interface StagedCell { old: any; newVal: any }
interface StagedRow { pk: Record<string, any>; fields: Map<string, StagedCell> }

/** Unsaved edits, keyed by the row object they belong to. Reactive for cell highlight. */
const pending = shallowReactive(new Map<Record<string, any>, StagedRow>())

function changedCellCount() {
  let n = 0
  for (const rec of pending.values()) n += rec.fields.size
  return n
}
function bumpPending() {
  emit('pending-change', changedCellCount())
}

function findMeta(colName: string) {
  const lower = colName.toLowerCase()
  return props.tableColumns?.find(c => c.name === colName || c.name.toLowerCase() === lower)
}
function colTitle(col: QueryResultColumn) {
  const meta = findMeta(col.name)
  if (meta?.isPrimary) return `${col.name} 🔑`
  return col.name
}
function isNullable(colName: string) {
  return findMeta(colName)?.nullable ?? true
}
function formatCell(v: any) {
  if (v === null || v === undefined) return ''
  if (typeof v === 'object') {
    try { return JSON.stringify(v) } catch { return String(v) }
  }
  return String(v)
}
function valuesEqual(a: any, b: any) {
  if (a === b) return true
  if (a === null || b === null || a === undefined || b === undefined) return false
  return String(a) === String(b)
}
function snapshotPk(row: Record<string, any>) {
  const pk: Record<string, any> = {}
  for (const k of props.primaryKeys || []) pk[k] = row[k] ?? null
  return pk
}
function isPending(row: Record<string, any>, field: string) {
  return pending.get(row)?.fields.has(field) ?? false
}

function startEdit(row: Record<string, any>, field: string) {
  if (editingRow.value === row && editingField.value === field) return
  if (editingRow.value) onCellEditConfirm() // stage pending edit first
  editingRow.value = row
  editingField.value = field
  emit('editing-change', true)
  draftIsNull.value = row[field] === null
  draftValue.value = row[field] === null || row[field] === undefined ? '' : String(row[field])
  snapOldValue = row[field]
  document.addEventListener('mousedown', onEditOutsideMousedown)
  document.addEventListener('keydown', onEditKeydown)
  nextTick(() => {
    cellInputEl.value?.focus()
    cellInputEl.value?.select()
  })
}

// Dismissal must not depend on the input's focus/blur: focus via the el-table
// slot ref is fragile, so listen globally while editing instead.
function onEditOutsideMousedown(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  if (target?.closest('.cell-edit-wrap')) return
  onCellEditConfirm()
}
function onEditKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') onCellEditCancel()
}
function onEditDismissCleanup() {
  document.removeEventListener('mousedown', onEditOutsideMousedown)
  document.removeEventListener('keydown', onEditKeydown)
}

// el-table's @cell-dblclick passes positional args (row, column, cell, event)
function onCellDblClick(row: Record<string, any>, column?: { property?: string }) {
  if (!props.canEdit) return
  const field = column?.property
  if (!field) return
  if (!props.columns.some(c => c.name === field)) return
  startEdit(row, field)
}

function onDraftInput() {
  draftIsNull.value = false
}
function setDraftNull() {
  draftIsNull.value = true
  draftValue.value = ''
}

function onCellEditConfirm() {
  const row = editingRow.value
  const field = editingField.value
  if (!row || !field) return
  stopEditing()
  const newVal: any = draftIsNull.value ? null : draftValue.value
  const oldVal = snapOldValue
  snapOldValue = undefined
  draftIsNull.value = false
  if (valuesEqual(oldVal, newVal)) {
    row[field] = oldVal
    return
  }
  // Stage instead of saving: reflect the change live, mark dirty, wait for explicit save.
  let rec = pending.get(row)
  if (!rec) {
    rec = { pk: snapshotPk(row), fields: reactive(new Map<string, StagedCell>()) }
    pending.set(row, rec)
  }
  row[field] = newVal
  const existing = rec.fields.get(field)
  const original = existing ? existing.old : oldVal
  if (valuesEqual(original, newVal)) {
    // Edited back to the committed value — drop the staged edit for this field.
    rec.fields.delete(field)
    if (rec.fields.size === 0) pending.delete(row)
    bumpPending()
    return
  }
  rec.fields.set(field, { old: original, newVal })
  bumpPending()
}

function onCellEditCancel() {
  const row = editingRow.value
  const field = editingField.value
  if (row && field) {
    const rec = pending.get(row)
    const staged = rec?.fields.get(field)
    row[field] = staged ? staged.newVal : snapOldValue
  }
  stopEditing()
  snapOldValue = undefined
  draftIsNull.value = false
}

function stopEditing() {
  editingRow.value = null
  editingField.value = null
  emit('editing-change', false)
  onEditDismissCleanup()
}

function onSortChange({ prop, order }: { prop?: string; order?: string | null }) {
  const field = prop || ''
  const ord: 'asc' | 'desc' | null =
    order === 'ascending' || order === 'asc' ? 'asc'
      : order === 'descending' || order === 'desc' ? 'desc'
        : null
  emit('sort-change', { field, order: ord })
}

/** Discard all staged edits for a row and restore its committed values. */
function revertRow(row: Record<string, any>) {
  const rec = pending.get(row)
  if (!rec) return
  for (const [field, cell] of rec.fields) row[field] = cell.old
  pending.delete(row)
  bumpPending()
}
/** Discard every staged edit. */
function revertAll() {
  for (const [row, rec] of pending) {
    for (const [field, cell] of rec.fields) row[field] = cell.old
  }
  pending.clear()
  bumpPending()
}
/** Mark a staged edit as persisted (after a successful save). */
function markCommitted(row: Record<string, any>, field: string) {
  const rec = pending.get(row)
  if (!rec) return
  rec.fields.delete(field)
  if (rec.fields.size === 0) pending.delete(row)
  bumpPending()
}

/** One UPDATE per row keeps a changed primary key and other fields together. */
function getPendingEdits(): Array<{
  row: Record<string, any>
  set: Record<string, any>
  where: Record<string, any>
}> {
  const out: ReturnType<typeof getPendingEdits> = []
  for (const [row, rec] of pending) {
    const set: Record<string, any> = {}
    for (const [field, cell] of rec.fields) set[field] = cell.newVal
    out.push({ row, set, where: { ...rec.pk } })
  }
  return out
}

function markCommittedRow(row: Record<string, any>) {
  pending.delete(row)
  bumpPending()
}

function clearEdit() {
  onCellEditConfirm()
}

onBeforeUnmount(onEditDismissCleanup)

watch(() => props.rows, (rows, previous = []) => {
  if (selectedCell.value && !rows.includes(selectedCell.value.row)) {
    selectedCell.value = null
  }
  // Filtering and client-side sorting create new arrays containing the same
  // row objects. Keep their staged changes until a different result arrives.
  if (rows.every(row => previous.includes(row))) return
  if (pending.size) {
    pending.clear()
    bumpPending()
  }
  // 新结果集到达：旧选中单元格已失效。
  selectedCell.value = null
})

defineExpose({
  getPendingEdits,
  markCommittedRow,
  revertRow,
  revertAll,
  markCommitted,
  clearEdit,
  getSelectedRows: () => [],
})
</script>

<style scoped>
.db-result-grid {
  flex: 1;
  min-height: 0;
  height: 100%;
  width: 100%;
  overflow: hidden;
  outline: none;
}
.db-result-table {
  --el-table-header-bg-color: var(--bg-elevated, var(--bg-surface));
  --el-table-tr-bg-color: var(--bg-surface);
  --el-table-row-hover-bg-color: var(--bg-hover);
  --el-table-border-color: var(--border-subtle);
  --el-table-header-text-color: var(--text-secondary);
  --el-table-text-color: var(--text-primary);
  --el-table-bg-color: var(--bg-surface);
  font-size: 0.75rem;
}
.row-actions {
  display: inline-flex;
  align-items: center;
  gap: 0.125rem;
}
.row-actions :deep(svg) { pointer-events: none; }
.cell-null {
  color: var(--text-muted);
  font-style: italic;
}
.cell-value {
  font-family: var(--font-mono, monospace);
  font-size: 0.75rem;
}
/* 单元格选中态（经 cellClassName 挂在 el-table 生成的 td 上，需 :deep）。 */
.db-result-table :deep(.db-cell-selected) {
  background-color: var(--accent-subtle, rgba(64, 128, 255, 0.15));
  box-shadow: inset 0 0 0 1px var(--accent);
  outline: none;
}
.cell-pending {
  background-color: var(--accent-subtle, rgba(64, 128, 255, 0.12));
  box-shadow: inset 0 -0.125rem 0 var(--accent);
}
.cell-edit-wrap {
  display: flex;
  align-items: stretch;
  width: 100%;
  gap: 0.125rem;
}
.cell-edit-input {
  flex: 1;
  min-width: 0;
  height: 1.5rem;
  padding: 0 0.375rem;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--bg-base);
  color: var(--text-primary);
  font-family: var(--font-mono, monospace);
  font-size: 0.75rem;
  outline: none;
}
.null-btn {
  flex-shrink: 0;
  width: 1.375rem;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--bg-elevated, var(--bg-hover));
  color: var(--text-muted);
  font-size: 0.625rem;
  font-weight: 700;
  font-style: italic;
}
.null-btn:hover {
  color: var(--text-primary);
  border-color: var(--accent);
}
</style>
