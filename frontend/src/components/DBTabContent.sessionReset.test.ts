// Regression tests for DB-18 / DB-19 (database regression audit 2026-10-25):
//
// DB-18: `docs` was a local ref that survived reconnects and dbType
// switches — stale docs kept issuing queries against the old database name
// and the edit gate trusted the old table's primary keys.
//
// DB-19: onSelectTable awaited the schema round-trip BEFORE opening the tab,
// so the click looked dead and a failure (silently swallowed) left a
// permanently read-only grid with no hint why.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const script = scriptBody(readSrc('components/DBTabContent.vue'))
const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

describe('DBTabContent session-scoped docs (DB-18)', () => {
  it('resets docs when the session or dialect changes', () => {
    const watcher = script.slice(
      script.indexOf('watch([() => props.sessionId, () => props.dbType]'),
      script.indexOf('const activeDoc = computed'),
    )
    expect(watcher).toMatch(/docs\.value = \[\]/)
    expect(watcher).toMatch(/activeDocId\.value = ''/)
  })
})

describe('DBTabContent open-table flow (DB-19)', () => {
  it('opens the tab before awaiting the schema', () => {
    const select = script.slice(
      script.indexOf('async function onSelectTable'),
      script.indexOf('async function onViewStructure'),
    )
    expect(select).toMatch(/docs\.value\.push\(doc\)/)
    expect(select).toMatch(/activateDoc\(doc\.id\)/)
    expect(select).toMatch(/await loadSchema\(doc\)/)
    // The push must come first: waiting on the round-trip made the click
    // feel dead and a failure left no tab at all.
    expect(select.indexOf('docs.value.push(doc)')).toBeLessThan(select.indexOf('await loadSchema(doc)'))
    // Retrying an existing tab must not block either.
    expect(select).toMatch(/if \(!existing\.primaryKeys\.length\) loadSchema\(existing\)/)
  })

  it('reports schema failures instead of swallowing them', () => {
    const load = script.slice(
      script.indexOf('async function loadSchema'),
      script.indexOf('async function onSelectTable'),
    )
    expect(load).not.toMatch(/catch \{ \/\* ignore \*\/ \}/)
    expect(load).toMatch(/msg\.error\(t\('db\.schemaLoadFailed'/)
  })
})

describe('DB-19 i18n key', () => {
  it.each(locales)('%s defines db.schemaLoadFailed with the {msg} param', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.schemaLoadFailed']).toMatch(/\{msg\}/)
  })
})
