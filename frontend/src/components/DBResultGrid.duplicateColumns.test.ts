// Regression test for the duplicate-column data-loss bug (security.md §MCP/
// database): ExecuteQuery used to key each row by column NAME, so
// `SELECT a.id, b.id FROM a JOIN b …` kept only one of the two values and the
// grid rendered that same value in both columns. The backend now returns
// positional [][]any rows (backend/database/executor_test.go
// TestExecuteQueryKeepsDuplicateColumnNames pins the data half); this file
// pins the rendering half: cells must be addressed by column index, never by
// name.
//
// The repo has no component-mount tests (node env, no jsdom / @vue/test-utils
// — see testUtils/sfc.ts), so the wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const gridSrc = readSrc('components/DBResultGrid.vue')
const gridScript = scriptBody(gridSrc)

describe('DBResultGrid positional cell access', () => {
  it('iterates columns with an index and keys/props by that index', () => {
    expect(gridSrc).toMatch(/v-for="\(col, idx\) in columns"/)
    expect(gridSrc).toMatch(/:key="idx"/)
    expect(gridSrc).toMatch(/:prop="String\(idx\)"/)
  })

  it('renders each cell from rows[r][c]', () => {
    expect(gridSrc).toMatch(/row\[idx\] === null/)
    expect(gridSrc).toMatch(/formatCell\(row\[idx\]\)/)
  })

  it('never looks a cell up by column name', () => {
    expect(gridSrc).not.toMatch(/row\[col\.name\]/)
  })

  it('tracks the edited cell by column index', () => {
    expect(gridScript).toMatch(/const editingField = ref<number \| null>\(null\)/)
    expect(gridScript).toMatch(/function isPending\(row: DbRow, fieldIdx: number\)/)
    // dblclick resolves the el-table column property (String(idx)) back to a
    // validated index instead of a name.
    expect(gridScript).toMatch(/const idx = Number\(prop\)/)
  })

  it('exposes the column index with each pending edit', () => {
    expect(gridScript).toMatch(/fieldIndex: fieldIdx/)
  })
})

describe('DBQueryEditor positional row handling', () => {
  const script = scriptBody(readSrc('components/DBQueryEditor.vue'))

  it('filters and sorts rows positionally', () => {
    expect(script).toMatch(/row\.some\(v => v != null && String\(v\)\.toLowerCase\(\)\.includes\(q\)\)/)
    expect(script).toMatch(/const av = a\[idx\]/)
    expect(script).toMatch(/const bv = b\[idx\]/)
  })

  it('builds WHERE clauses from the primary key column index', () => {
    expect(script).toMatch(/const pkIdx = cols\.findIndex\(c => c\.name === pk\)/)
    expect(script).toMatch(/const pkIdx = queryResult\.value!\.columns\.findIndex\(c => c\.name === pk\)/)
  })

  it('stages grid edits against the column index', () => {
    // The batch save carries the positional handle through to commit time
    // (now grouped per row: fieldIndex → fields[] → markCommitted(g.row, f)).
    expect(script).toMatch(/g\.fields\.push\(ed\.fieldIndex\)/)
    expect(script).toMatch(/markCommitted\(g\.row, f\)/)
  })
})

describe('types/database.ts positional QueryResult', () => {
  it('declares rows as positional DbRow[]', () => {
    expect(readSrc('types/database.ts')).toMatch(/rows: DbRow\[\]/)
  })
})
