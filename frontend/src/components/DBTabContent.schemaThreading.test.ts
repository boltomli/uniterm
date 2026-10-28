// Regression tests for DB-06/DB-07/DB-09 (2026-10-25 regression audit):
// "点了 A 表、改了 B 表". TableInfo now carries a schema, the tree shows and
// forwards it, and every metadata / browse / row-CRUD / DDL call hands it to
// the backend, which qualifies schema.table for PostgreSQL and SQL Server.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// threading is asserted on the SFC sources.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const tree = scriptBody(readSrc('components/DBTreePanel.vue'))
const tab = scriptBody(readSrc('components/DBTabContent.vue'))
const editor = scriptBody(readSrc('components/DBQueryEditor.vue'))
const structure = scriptBody(readSrc('components/DBTableStructure.vue'))
const objects = scriptBody(readSrc('components/DBObjectList.vue'))

describe('DBTreePanel schema disambiguation (DB-06/DB-07)', () => {
  it('shows schema-qualified table labels', () => {
    expect(tree).toMatch(/function tableLabel\(tb: \{ name: string; schema\?: string \}\)/)
    expect(tree).toMatch(/return tb\.schema \? `\$\{tb\.schema\}\.\$\{tb\.name\}` : tb\.name/)
  })

  it('forwards the schema on selection and structure views', () => {
    expect(tree).toMatch(/emit\('selectTable', dbName, table\.name, table\.type === 'view', table\.schema \|\| ''\)/)
    expect(tree).toMatch(/emit\('viewStructure', ctxDbName\.value, ctxTableName\.value, ctxTableSchema\.value\)/)
  })

  it('carries the schema into drop/view/truncate/copy', () => {
    expect(tree).toMatch(/await DropTable\(props\.sessionId, dbName, schema, tableName\)/)
    expect(tree).toMatch(/await DropView\(props\.sessionId, dbName, schema, tableName\)/)
    expect(tree).toMatch(/await TruncateTable\(props\.sessionId, dbName, schema, tableName\)/)
    expect(tree).toMatch(/await CopyTable\(props\.sessionId, dbName, schema, copySourceTable\.value, newName\)/)
  })
})

describe('DBTabContent schema-scoped docs (DB-06)', () => {
  it('stores the schema per doc and keys lookups on it', () => {
    expect(tab).toMatch(/schema: string/)
    expect(tab).toMatch(/function findTableDoc\(dbName: string, schema: string, tableName: string\)/)
    expect(tab).toMatch(/\(d\.schema \|\| ''\) === schema/)
  })

  it('passes the schema to the editor, structure page and schema loader', () => {
    expect(readSrc('components/DBTabContent.vue')).toMatch(/:schema="doc\.schema"/)
    expect(tab).toMatch(/GetTableSchema\(props\.sessionId, doc\.dbName, doc\.schema \|\| '', doc\.tableName\)/)
  })
})

describe('DBQueryEditor schema threading (DB-06)', () => {
  it('declares the schema prop', () => {
    expect(editor).toMatch(/schema\?: string/)
  })

  it('qualifies browse, metadata and row-CRUD calls', () => {
    // Browse SQL for the table pager (schema-qualified for PG/MSSQL).
    const browseCalls = editor.match(/DBDefaultTableQuery\(\s*props\.sessionId,\s*props\.dbName \|\| '',\s*props\.schema \|\| '',/g) ?? []
    expect(browseCalls.length).toBeGreaterThanOrEqual(1)
    // The filtered browse path (DBBrowseTable) carries it too.
    expect(editor).toMatch(/DBBrowseTable\(props\.sessionId, props\.dbName \|\| '', props\.schema \|\| '', props\.tableName, \{/)
    // Every metadata / row-CRUD call carries the schema (multi-line calls
    // included, hence the shared count).
    const schemaPasses = editor.match(/props\.schema \|\| ''/g) ?? []
    // DBBrowseTable + DBDefaultTableQuery + 2× GetTableSchema + DBImportCSV
    // + DBUpdateRowsAtomic + DBDeleteRow + DBInsertRow + DBUpdateRow
    // + the AI probe.
    expect(schemaPasses.length).toBeGreaterThanOrEqual(9)
    expect(editor).toMatch(/DBDeleteRow\(props\.sessionId, props\.dbName \|\| '', props\.schema \|\| '',/)
    expect(editor).toMatch(/DBInsertRow\(props\.sessionId, props\.dbName \|\| '', props\.schema \|\| '',/)
    expect(editor).toMatch(/GetTableSchema\(props\.sessionId, props\.dbName \|\| '', props\.schema \|\| '', props\.tableName \|\| ''\)/)
  })
})

describe('DBTableStructure + DBObjectList schema threading (DB-06/DB-07)', () => {
  it('the structure page declares the schema and passes it to every DDL call', () => {
    expect(structure).toMatch(/schema\?: string/)
    for (const call of ['ModifyColumn', 'DropColumn', 'DropIndexOp', 'AddColumn']) {
      const hits = structure.match(new RegExp(`await ${call}\\(props\\.sessionId, props\\.dbName, props\\.schema \\|\\| '',`, 'g')) ?? []
      expect(hits.length).toBeGreaterThanOrEqual(1)
    }
    const addIndex = structure.match(/await AddIndex\(props\.sessionId, props\.dbName, props\.schema \|\| '',/g) ?? []
    expect(addIndex.length).toBeGreaterThanOrEqual(2)
  })

  it('the object list opens and mutates rows schema-aware', () => {
    expect(objects).toMatch(/emit\('open', props\.dbName, row\.name, row\.type === 'view', row\.schema \|\| ''\)/)
    expect(objects).toMatch(/await TruncateTable\(props\.sessionId, props\.dbName, row\.schema \|\| '', row\.name\)/)
    expect(objects).toMatch(/await DropTable\(props\.sessionId, props\.dbName, row\.schema \|\| '', row\.name\)/)
    expect(objects).toMatch(/await DropView\(props\.sessionId, props\.dbName, row\.schema \|\| '', row\.name\)/)
  })
})

describe('frontend types (DB-09)', () => {
  it('TableInfo carries the optional schema', () => {
    expect(readSrc('types/database.ts')).toMatch(/schema\?: string/)
  })
})
