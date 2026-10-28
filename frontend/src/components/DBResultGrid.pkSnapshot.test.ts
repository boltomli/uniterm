// Regression tests for DB-01 (database regression audit 2026-10-25):
// "主键快照晚于赋值" — the grid used to write the new cell value into the row
// object BEFORE snapshotting the primary key, so the first staged edit on a
// PK column produced an UPDATE ... WHERE pk = <new value> that matched zero
// rows while the UI happily reported success. The fix moves the snapshot
// ahead of the write; the second half of the fix lives in the backend:
// database.UpdateRowsAtomic rejects any row UPDATE whose affected count is
// not exactly 1 and rolls the whole batch back (pinned by
// backend/database/table_operations_test.go TestUpdateRowsAtomicRollback),
// so a stale WHERE can never masquerade as a successful save.
//
// No component-mount tests exist (node env, no jsdom — see testUtils/sfc.ts),
// so the ordering is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const gridScript = scriptBody(readSrc('components/DBResultGrid.vue'))

describe('DBResultGrid PK snapshot ordering (DB-01)', () => {
  it('snapshots the primary key before writing the new value into the row', () => {
    const snapshot = gridScript.indexOf('snapshotPk(row)')
    const write = gridScript.indexOf('row[field] = newVal')
    expect(snapshot).toBeGreaterThan(-1)
    expect(write).toBeGreaterThan(-1)
    // The WHERE clause of every UPDATE is built from rec.pk; if the snapshot
    // trails the write, a first-time PK edit snapshots the NEW value.
    expect(snapshot).toBeLessThan(write)
  })

  it('creates the pending record from the pre-edit row state', () => {
    expect(gridScript).toMatch(/rec = \{ pk: snapshotPk\(row\), fields: reactive\(new Map<string, StagedCell>\(\)\) \}/)
  })
})

describe('DBQueryEditor batch save keeps the read snapshot (DB-01 / DB-05)', () => {
  it('saves staged edits as one atomic batch keyed by the original PKs', () => {
    const editorScript = scriptBody(readSrc('components/DBQueryEditor.vue'))
    const save = editorScript.slice(
      editorScript.indexOf('async function onSaveEdits'),
      editorScript.indexOf('function onUndoEdits'),
    )
    // The grid hands each row's original PK values as the WHERE clause (see
    // DBResultGrid getPendingEdits), and a failure keeps every edit staged
    // (no markCommittedRow on the error path) — the backend rolled the whole
    // transaction back.
    expect(save).toMatch(/DBUpdateRowsAtomic\(/)
    expect(save).toMatch(/where: ed\.where/)
    expect(save).toMatch(/edits\.map\(ed => \(\{ set: ed\.set, where: ed\.where \}\)\)/)
  })

  it('stages each row as set + original-PK where in the grid', () => {
    const gridScript2 = scriptBody(readSrc('components/DBResultGrid.vue'))
    const pending = gridScript2.slice(
      gridScript2.indexOf('function getPendingEdits'),
      gridScript2.indexOf('function markCommittedRow'),
    )
    expect(pending).toMatch(/where: \{ \.\.\.rec\.pk \}/)
  })
})
