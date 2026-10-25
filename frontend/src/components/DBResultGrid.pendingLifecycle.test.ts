// Regression tests for DB-02 (database regression audit 2026-10-25):
// "过滤/排序/重新执行会清空 pending，但行对象已被改写 → 幽灵保存".
// The grid used to clear staged edits in a watch on props.rows — but the
// parent passes a DERIVED array (filter/sort), so typing one character in
// the result filter produced a new array reference, wiped the pending map,
// and left the edited (already-written-into-the-row) value on screen with no
// way to save it. Pending edits are now keyed to the result-set identity
// (resultKey), and replacing the result set asks before discarding them.
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
  it('clears staged edits only when the result-set key changes', () => {
    const resultKeyWatch = bodyOf(gridScript, 'watch(() => props.resultKey', 'watch(() => props.rows')
    expect(resultKeyWatch).toMatch(/pending\.clear\(\)/)
    expect(resultKeyWatch).toMatch(/bumpPending\(\)/)
  })

  it('a rows-array change (filter/sort) never touches pending', () => {
    const rowsWatch = bodyOf(gridScript, 'watch(() => props.rows', 'defineExpose')
    expect(rowsWatch).not.toMatch(/pending\.clear/)
    expect(rowsWatch).toMatch(/selectedCell\.value = null/)
  })

  it('declares the resultKey prop', () => {
    expect(gridScript).toMatch(/resultKey\?: number/)
  })
})

describe('DBQueryEditor result-set identity (DB-02)', () => {
  it('passes a monotonic result key to the grid', () => {
    expect(editorSrc).toMatch(/:result-key="resultSeq"/)
    expect(editorScript).toMatch(/const resultSeq = ref\(0\)/)
    // The key advances only when a fresh query result replaces the set.
    const queryBranch = bodyOf(
      editorScript,
      'else if (isQuerySql(toRun))',
      'const call = ExecuteStatement',
    )
    expect(queryBranch).toMatch(/resultSeq\.value\+\+/)
  })

  it('asks before running a query that would discard staged edits', () => {
    const exec = bodyOf(editorScript, 'async function onExecute', 'function onCancelQuery')
    expect(exec).toMatch(/if \(pendingCount\.value > 0\)/)
    expect(exec).toMatch(/db\.discardEditsConfirm/)
    // The confirm's cancel path must abort the execution.
    expect(exec).toMatch(/catch \{\s*return\s*\}/)
    // The stale counter must be reset together with the discard.
    expect(exec).toMatch(/pendingCount\.value = 0/)
  })
})

describe('DB-02 i18n key', () => {
  const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

  it.each(locales)('%s defines db.discardEditsConfirm with the {n} param', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.discardEditsConfirm']).toMatch(/\{n\}/)
  })
})
