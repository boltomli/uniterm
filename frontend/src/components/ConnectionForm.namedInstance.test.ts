// Regression tests for DB-25 (database regression audit 2026-10-25):
// SQL Server named instances. The DSN honours `server\instance` by omitting
// the port so SQL Browser resolves it — but the connection form prefilled
// 1433 on every type switch, making the fixed-port path the only reachable
// one in the UI and the Browser lookup was bypassed. The provider now
// ignores an explicit port for named instances, and the form disables the
// port field with an explanation.
//
// The provider half is pinned by provider_sqlserver_test.go
// (TestSqlserverDSN, "named instance ignores an explicit port"); this file
// pins the form wiring and the i18n key.

import { describe, expect, it } from 'vitest'
import { readSrc } from '../testUtils/sfc'

const formSrc = readSrc('components/ConnectionForm.vue')
const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

describe('ConnectionForm named-instance port handling (DB-25)', () => {
  it('detects server\\instance for SQL Server connections', () => {
    expect(formSrc).toMatch(/const isSqlNamedInstance = computed\(\(\) =>\s*form\.type === 'database' && form\.dbType === 'sqlserver' && form\.host\.includes\('\\\\'\)\)/)
  })

  it('disables and explains the port field for named instances', () => {
    expect(formSrc).toMatch(/:disabled="isSqlNamedInstance"/)
    expect(formSrc).toMatch(/v-if="isSqlNamedInstance" class="port-hint">\{\{ t\('db\.namedInstancePortHint'\) \}\}/)
  })
})

describe('DB-25 i18n key', () => {
  it.each(locales)('%s defines db.namedInstancePortHint', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.namedInstancePortHint'].length).toBeGreaterThan(0)
  })
})
