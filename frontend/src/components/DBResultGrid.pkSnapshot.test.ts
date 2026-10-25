// Regression tests for DB-01 (database regression audit 2026-10-25):
// "主键快照晚于赋值" — the grid used to write the new cell value into the row
// object BEFORE snapshotting the primary key, so the first staged edit on a
// PK column produced an UPDATE ... WHERE pk = <new value> that matched zero
// rows while the UI happily reported success. The fix moves the snapshot
// ahead of the write; the second half of the fix is that the backend now
// returns the affected-row count and the editor refuses to mark an edit
// committed when it is 0.
//
// No component-mount tests exist (node env, no jsdom — see testUtils/sfc.ts),
// so the ordering is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const gridScript = scriptBody(readSrc('components/DBResultGrid.vue'))
const editorScript = scriptBody(readSrc('components/DBQueryEditor.vue'))

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
    expect(gridScript).toMatch(/rec = \{ pk: snapshotPk\(row\), fields: new Map\(\) \}/)
  })
})

describe('DBQueryEditor affected-row checks (DB-01)', () => {
  it('does not mark a staged edit committed when UPDATE affected 0 rows', () => {
    const save = editorScript.slice(
      editorScript.indexOf('async function onSaveEdits'),
      editorScript.indexOf('function onUndoEdits'),
    )
    expect(save).toMatch(/const affected = await DBUpdateRow\(/)
    expect(save).toMatch(/if \(affected === 0\)/)
    expect(save).toMatch(/db\.updateNoMatch/)
    // markCommitted must only run on the success path, after the zero check.
    expect(save.indexOf('if (affected === 0)')).toBeLessThan(save.indexOf('markCommitted'))
  })

  it('keeps the row-edit dialog open when the snapshot WHERE matched nothing', () => {
    const confirm = editorScript.slice(
      editorScript.indexOf('async function onEditRowConfirm'),
      editorScript.indexOf('function onEditRowCancel'),
    )
    expect(confirm).toMatch(/const affected = await DBUpdateRow\(/)
    expect(confirm).toMatch(/if \(affected === 0\)/)
    // The early return happens before the local rows array is rewritten.
    expect(confirm.indexOf('if (affected === 0)')).toBeLessThan(
      confirm.indexOf('const updatedRow = [...queryResult.value!.rows[idx]]'),
    )
  })

  it('reports a delete that matched no row', () => {
    const del = editorScript.slice(
      editorScript.indexOf('async function onDeleteRow(rowIndex: number)'),
      editorScript.indexOf('const insertingRow = ref'),
    )
    expect(del).toMatch(/const affected = await DBDeleteRow\(/)
    expect(del).toMatch(/if \(affected === 0\)/)
    expect(del).toMatch(/db\.rowGone/)
  })
})

describe('DB-01 i18n keys', () => {
  const locales = [
    'de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW',
  ]

  it.each(locales)('%s defines db.updateNoMatch and db.rowGone', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(typeof dict['db.updateNoMatch']).toBe('string')
    expect(dict['db.updateNoMatch'].length).toBeGreaterThan(0)
    expect(typeof dict['db.rowGone']).toBe('string')
    expect(dict['db.rowGone'].length).toBeGreaterThan(0)
  })
})
