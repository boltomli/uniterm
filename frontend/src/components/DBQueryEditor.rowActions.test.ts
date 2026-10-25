// Regression tests for DB-04 (database regression audit 2026-10-25):
// "行弹窗/删除行使用『当前行值』构造 WHERE". The whole-row edit dialog and the
// delete action built their WHERE clause from the row as it currently sits
// in the result — but the grid writes staged cell edits INTO that row
// object, so the WHERE could carry unsaved (dirty) values; and the
// rowIndexOf fallback matched by value-equality, which hits the wrong row
// when duplicates exist (views / no-PK results).
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC sources.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const gridScript = scriptBody(readSrc('components/DBResultGrid.vue'))
const editorScript = scriptBody(readSrc('components/DBQueryEditor.vue'))
const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

function bodyOf(src: string, startMarker: string, endMarker: string): string {
  const start = src.indexOf(startMarker)
  expect(start).toBeGreaterThan(-1)
  const end = src.indexOf(endMarker, start + startMarker.length)
  expect(end).toBeGreaterThan(start)
  return src.slice(start, end)
}

describe('DBQueryEditor row-identity WHERE construction (DB-04)', () => {
  it('locates rows by object identity only', () => {
    const locate = bodyOf(editorScript, 'function rowIndexOf', '/** Row-level actions')
    expect(locate).toMatch(/rows\.indexOf\(row\)/)
    // The value-equality fallback could pick a different duplicate row.
    expect(locate).not.toMatch(/r\.every\(/)
    expect(locate).not.toMatch(/findIndex\(r =>/)
  })

  it('refuses row actions on rows carrying staged edits', () => {
    const refuse = bodyOf(editorScript, 'function refusePendingRow', 'function startEditRowByRow')
    expect(refuse).toMatch(/isRowPending\(row\)/)
    expect(refuse).toMatch(/db\.saveOrDiscardFirst/)

    const editByRow = bodyOf(editorScript, 'function startEditRowByRow', 'async function onDeleteRowByRow')
    expect(editByRow.indexOf('refusePendingRow(row)')).toBeLessThan(editByRow.indexOf('rowIndexOf(row)'))

    const deleteByRow = bodyOf(editorScript, 'async function onDeleteRowByRow', '// ?? Resize splitter ??')
    expect(deleteByRow.indexOf('refusePendingRow(row)')).toBeLessThan(deleteByRow.indexOf('rowIndexOf(row)'))
  })
})

describe('DBResultGrid pending-row query (DB-04)', () => {
  it('exposes isRowPending backed by the pending map', () => {
    expect(gridScript).toMatch(/function isRowPending\(row: DbRow\) \{\s*return pending\.has\(row\)/)
    expect(gridScript).toMatch(/isRowPending,/)
  })
})

describe('DB-04 i18n key', () => {
  it.each(locales)('%s defines db.saveOrDiscardFirst', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.saveOrDiscardFirst'].length).toBeGreaterThan(0)
  })
})
