// Regression tests for DB-02 (database regression audit 2026-10-25):
// "过滤/排序/重新执行会清空 pending，但行里已是新值 → 幽灵保存".
// The grid must NOT clear staged edits when the parent passes a DERIVED
// rows array (filter/sort produce a new array holding the same row objects);
// only a genuinely different result set may invalidate them. Replacing the
// result set asks before discarding.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC sources.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const gridScript = scriptBody(readSrc('components/DBResultGrid.vue'))
const editorSrc = readSrc('components/DBQueryEditor.vue')
const editorScript = scriptBody(editorSrc)

function bodyOf(src: string, startMarker: string, endMarker: string): string {
  const start = src.indexOf(startMarker)
  expect(start).toBeGreaterThan(-1)
  const end = src.indexOf(endMarker, start + startMarker.length)
  expect(end).toBeGreaterThan(start)
  return src.slice(start, end)
}

describe('DBResultGrid pending lifecycle (DB-02)', () => {
  it('keeps staged edits when the new rows array is a subset of the old', () => {
    // Filtering/sorting re-orders or narrows the SAME row objects; clearing
    // pending here used to turn a later save into a phantom no-op while the
    // edited value stayed rendered.
    const rowsWatch = bodyOf(gridScript, 'watch(() => props.rows', 'defineExpose')
    expect(rowsWatch).toMatch(/if \(rows\.every\(row => previous\.includes\(row\)\)\) return/)
    expect(rowsWatch).toMatch(/pending\.clear\(\)/)
    expect(rowsWatch).toMatch(/bumpPending\(\)/)
  })

  it('clears the selection when its row left the array', () => {
    const rowsWatch = bodyOf(gridScript, 'watch(() => props.rows', 'defineExpose')
    expect(rowsWatch).toMatch(/if \(selectedCell\.value && !rows\.includes\(selectedCell\.value\.row\)\)/)
  })
})

describe('DBQueryEditor result-set identity (DB-02)', () => {
  it('asks before running a query that would discard staged edits', () => {
    const confirm = bodyOf(editorScript, 'async function confirmDiscardEdits', 'function onPendingChange')
    expect(confirm).toMatch(/if \(!pendingCount\.value\) return true/)
    expect(confirm).toMatch(/db\.discardEditsConfirm/)
    // The confirm's cancel path aborts the execution (returns false) and the
    // accepted path restores the committed values before the new result lands.
    expect(confirm).toMatch(/revertAll\(\)/)
    expect(confirm).toMatch(/return false/)
  })

  it('every execution entry point gates on the confirm', () => {
    const exec = bodyOf(editorScript, 'async function onExecute', 'function isCancelError')
    expect(exec).toMatch(/if \(!\(await confirmDiscardEdits\(\)\)\) return/)
  })
})

describe('DB-02 i18n key', () => {
  const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

  it.each(locales)('%s defines db.discardEditsConfirm with the {n} param', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.discardEditsConfirm']).toMatch(/\{n\}/)
  })
})
