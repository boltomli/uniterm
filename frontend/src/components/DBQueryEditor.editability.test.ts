// Regression tests for DB-03 (database regression audit 2026-10-25):
// "自定义/JOIN SQL 完成后仍可内联编辑". canEditRows only looked at the
// primary keys present in the result, so rewriting the browse SQL into a
// JOIN kept the grid editable: saving a cell then issued
// `UPDATE <base table> SET <same-named column> …` — either the wrong table
// or an error. Editability is now bound to browse mode, which onExecute
// re-derives by comparing the buffer against DefaultTableQuery.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const src = readSrc('components/DBQueryEditor.vue')
const script = scriptBody(src)

describe('DBQueryEditor editability (DB-03)', () => {
  it('gates inline editing on browse mode, not just primary keys', () => {
    expect(script).toMatch(/const canEditRows = computed\(\(\) => browseMode\.value && resolvedPrimaryKeys\.value\.length > 0\)/)
  })

  it('leaves browse mode when the executed SQL is not the browse query', () => {
    const exec = script.slice(
      script.indexOf('async function onExecute'),
      script.indexOf('function onCancelQuery'),
    )
    expect(exec).toMatch(/browseMode\.value = false/)
    expect(exec).toMatch(/DBDefaultTableQuery\(/)
  })

  it('enters browse mode only through the generated browse SQL', () => {
    expect(script).toMatch(/function loadBrowseSql\(offset = 0\)[\s\S]*?browseMode\.value = true/)
    // Manual entry points (history, AI) leave browse mode immediately.
    expect(script).toMatch(/function applyHistory[\s\S]*?browseMode\.value = false/)
    expect(script).toMatch(/function generateSQL[\s\S]*?browseMode\.value = false/)
  })

  it('binds the grid to the gated flag', () => {
    expect(src).toMatch(/:can-edit="canEditRows"/)
  })
})
