<template>
  <div class="db-result-grid" tabindex="0">
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
      @sort-change="onSortChange"
      @cell-dblclick="onCellDblClick"
    >
      <el-table-column
        v-for="(col, idx) in columns"
        :key="idx"
        :prop="String(idx)"
        :label="colTitle(col)"
        :min-width="uiPx(110)"
        sortable="custom"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          <div
            v-if="canEdit && editingRow === row && editingField === idx"
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
            v-else-if="row[idx] === null"
            class="cell-null"
            :class="{ 'cell-pending': isPending(row, idx) }"
          >{{ t('db.null') }}</span>
          <span
            v-else
            class="cell-value"
            :class="{ 'cell-pending': isPending(row, idx) }"
          >{{ formatCell(row[idx]) }}</span>
        </template>
      </el-table-column>

      <el-table-column
        v-if="canEdit"
        :label="actionsLabel"
        :width="uiPx(88)"
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
              <Pencil :size="lucideSize('0.875rem')" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-icon btn-sm danger"
              :title="deleteLabel"
              @click.stop.prevent="emit('delete-row', row)"
            >
              <Trash2 :size="lucideSize('0.875rem')" />
            </button>
          </div>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup lang="ts">
import { lucideSize } from '../utils/lucideSize'
import { computed, nextTick, onBeforeUnmount, ref, shallowReactive, shallowRef, watch } from 'vue'
import { Pencil, Trash2 } from '@lucide/vue'
import type { QueryResultColumn, ColumnInfo, DbRow, DbCellValue } from '../types/database'
import { useI18n } from '../i18n'
import { uiPx } from '../utils/uiScale'

const { t } = useI18n()

const props = defineProps<{
  /** Positional rows; cell (row r, column c) is rows[r][c]. Duplicate
   *  column names are legal, so cells are addressed by column index. */
  rows: DbRow[]
  columns: QueryResultColumn[]
  canEdit: boolean
  primaryKeys?: string[]
  tableColumns?: ColumnInfo[]
  emptyText?: string
  actionsLabel?: string
  editLabel?: string
  deleteLabel?: string
}>()

const emit = defineEmits<{
  /** A staged, unsaved cell edit changed; payload = number of pending edits. */
  'pending-change': [count: number]
  'edit-row': [row: DbRow]
  'delete-row': [row: DbRow]
  'sort-change': [payload: { index: number; order: 'asc' | 'desc' | null }]
}>()

// Write-only template ref target; never read in script.
const tableRef = ref<unknown>(null)
const cellInputEl = ref<HTMLInputElement | null>(null)

const tableKey = computed(() => `edit-${props.canEdit ? 1 : 0}-cols-${props.columns.map(c => c.name).join('|')}`)

/** Currently edited cell (row + column index). shallowRef: a deep ref would
 *  try to unwrap the recursive DbCellValue row type (TS2589); the row object
 *  identity is all this needs. */
const editingRow = shallowRef<DbRow | null>(null)
const editingField = ref<number | null>(null)
const draftValue = ref('')
const draftIsNull = ref(false)
let snapOldValue: DbCellValue | undefined = undefined

interface StagedCell { old: DbCellValue; newVal: DbCellValue }
interface StagedRow { pk: Record<string, DbCellValue>; fields: Map<number, StagedCell> }

/** Unsaved edits, keyed by the row object they belong to. Reactive for cell
 *  highlight. shallowReactive: the Map operations (get/set/delete) must be
 *  tracked, but deep-unwrapping the recursive DbCellValue rows through
 *  Vue's reactive types blows the instantiation budget (TS2589). */
const pending = shallowReactive(new Map<DbRow, StagedRow>())

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
function formatCell(v: DbCellValue | undefined) {
  if (v === null || v === undefined) return ''
  if (typeof v === 'object') {
    try { return JSON.stringify(v) } catch { return String(v) }
  }
  return String(v)
}
function valuesEqual(a: DbCellValue | undefined, b: DbCellValue | undefined) {
  if (a === b) return true
  if (a === null || b === null || a === undefined || b === undefined) return false
  return String(a) === String(b)
}
function snapshotPk(row: DbRow) {
  const pk: Record<string, DbCellValue> = {}
  for (const k of props.primaryKeys || []) {
    const idx = props.columns.findIndex(c => c.name === k)
    pk[k] = idx >= 0 ? row[idx] ?? null : null
  }
  return pk
}
function isPending(row: DbRow, fieldIdx: number) {
  return pending.get(row)?.fields.has(fieldIdx) ?? false
}

function startEdit(row: DbRow, fieldIdx: number) {
  if (editingRow.value === row && editingField.value === fieldIdx) return
  if (editingRow.value) onCellEditConfirm() // stage pending edit first
  editingRow.value = row
  editingField.value = fieldIdx
  draftIsNull.value = row[fieldIdx] === null
  draftValue.value = row[fieldIdx] === null || row[fieldIdx] === undefined ? '' : String(row[fieldIdx])
  snapOldValue = row[fieldIdx]
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

// el-table's @cell-dblclick passes positional args (row, column, cell, event).
// The column property is the ordinal index (String(idx)), so duplicate
// column names still map to the right cell.
function onCellDblClick(row: DbRow, column?: { property?: string }) {
  if (!props.canEdit) return
  const prop = column?.property
  if (prop === undefined || prop === null || prop === '') return
  const idx = Number(prop)
  if (!Number.isInteger(idx) || idx < 0 || idx >= props.columns.length) return
  startEdit(row, idx)
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
  if (!row || field === null) return
  editingRow.value = null
  editingField.value = null
  onEditDismissCleanup()
  const newVal: DbCellValue = draftIsNull.value ? null : draftValue.value
  const oldVal = snapOldValue
  snapOldValue = undefined
  draftIsNull.value = false
  if (valuesEqual(oldVal, newVal)) {
    row[field] = oldVal ?? null
    return
  }
  // Stage instead of saving: reflect the change live, mark dirty, wait for explicit save.
  row[field] = newVal
  let rec = pending.get(row)
  if (!rec) {
    rec = { pk: snapshotPk(row), fields: new Map() }
    pending.set(row, rec)
  }
  const existing = rec.fields.get(field)
  const original = existing ? existing.old : (oldVal ?? null)
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
  if (row && field !== null) {
    const rec = pending.get(row)
    const staged = rec?.fields.get(field)
    row[field] = staged ? staged.newVal : (snapOldValue ?? null)
  }
  editingRow.value = null
  editingField.value = null
  onEditDismissCleanup()
  snapOldValue = undefined
  draftIsNull.value = false
}

function onSortChange({ prop, order }: { prop?: string; order?: string | null }) {
  const idx = prop === undefined || prop === null || prop === '' ? -1 : Number(prop)
  const ord: 'asc' | 'desc' | null =
    order === 'ascending' || order === 'asc' ? 'asc'
      : order === 'descending' || order === 'desc' ? 'desc'
        : null
  emit('sort-change', { index: idx, order: ord })
}

/** Discard all staged edits for a row and restore its committed values. */
function revertRow(row: DbRow) {
  const rec = pending.get(row)
  if (!rec) return
  for (const [fieldIdx, cell] of rec.fields) row[fieldIdx] = cell.old
  pending.delete(row)
  bumpPending()
}
/** Discard every staged edit. */
function revertAll() {
  for (const [row, rec] of pending) {
    for (const [fieldIdx, cell] of rec.fields) row[fieldIdx] = cell.old
  }
  pending.clear()
  bumpPending()
}
/** Mark a staged edit as persisted (after a successful save). */
function markCommitted(row: DbRow, fieldIdx: number) {
  const rec = pending.get(row)
  if (!rec) return
  rec.fields.delete(fieldIdx)
  if (rec.fields.size === 0) pending.delete(row)
  bumpPending()
}

/** Snapshot of all currently staged edits, ready for DBUpdateRow calls. `field`
 *  carries the column name (unique in editable browse results); fieldIndex is
 *  the positional handle used by markCommitted. */
function getPendingEdits(): Array<{
  row: DbRow
  field: string
  fieldIndex: number
  newValue: DbCellValue
  oldValue: DbCellValue
  where: Record<string, DbCellValue>
}> {
  const out: ReturnType<typeof getPendingEdits> = []
  for (const [row, rec] of pending) {
    for (const [fieldIdx, cell] of rec.fields) {
      out.push({
        row,
        field: props.columns[fieldIdx]?.name ?? '',
        fieldIndex: fieldIdx,
        newValue: cell.newVal,
        oldValue: cell.old,
        where: { ...rec.pk },
      })
    }
  }
  return out
}

function clearEdit() {
  onCellEditConfirm()
}

onBeforeUnmount(onEditDismissCleanup)

watch(() => props.rows, () => {
  if (pending.size) {
    pending.clear()
    bumpPending()
  }
})

defineExpose({
  getPendingEdits,
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