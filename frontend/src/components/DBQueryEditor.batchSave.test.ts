// Regression tests for DB-05 (database regression audit 2026-10-25):
// "逐条 UPDATE、无事务、无 affected 校验，失败即 break 留下半提交".
// The grid save now goes through ONE transactional batch: staged edits are
// grouped per row (one UPDATE per row, WHERE = read snapshot), the backend
// returns per-row affected counts, a statement error rolls the whole save
// back, and a 0-affected row keeps ITS staged edits instead of reverting
// unrelated cells of the same row.
//
// The transactional half is pinned by backend/database/update_rows_test.go;
// this file pins the frontend grouping and bookkeeping.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const editor = scriptBody(readSrc('components/DBQueryEditor.vue'))

function bodyOf(src: string, startMarker: string, endMarker: string): string {
  const start = src.indexOf(startMarker)
  expect(start).toBeGreaterThan(-1)
  const end = src.indexOf(endMarker, start + startMarker.length)
  expect(end).toBeGreaterThan(start)
  return src.slice(start, end)
}

describe('DBQueryEditor transactional save (DB-05)', () => {
  const save = bodyOf(editor, 'async function onSaveEdits', 'function onUndoEdits')

  it('groups staged edits per row into one UPDATE each', () => {
    expect(save).toMatch(/const groups = new Map<DbRow/)
    expect(save).toMatch(/g\.set\[ed\.field\] = ed\.newValue/)
    expect(save).toMatch(/g\.fields\.push\(ed\.fieldIndex\)/)
    expect(save).toMatch(/list\.map\(g => \(\{ set: g\.set, where: g\.where \}\)\)/)
  })

  it('marks committed only the fields of rows that reported an affected count', () => {
    expect(save).toMatch(/const affected = counts\[i\]/)
    expect(save).toMatch(/for \(const f of g\.fields\) resultGridRef\.value\?\.markCommitted\(g\.row, f\)/)
    // The stale row stays staged; the rest still commit.
    expect(save).toMatch(/stale \+= 1/)
  })

  it('keeps every edit staged when the transaction rolls back', () => {
    const fail = bodyOf(save, 'catch (e: any) {', 'let stale = 0')
    expect(fail).toMatch(/The transaction rolled back/)
    // No markCommitted and no revertRow on the failure path — the batch was
    // rolled back server-side, the drafts are still exactly what is in the
    // database.
    expect(fail).not.toMatch(/markCommitted/)
    expect(fail).not.toMatch(/revertRow/)
  })

  it('no longer issues per-edit single UPDATE calls', () => {
    expect(save).not.toMatch(/await DBUpdateRow\(/)
  })
})
