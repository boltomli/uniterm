// Regression tests for DB-27 (database regression audit 2026-10-25):
// "多语句脚本里的 SELECT 结果被丢弃". ExecuteScript runs every statement
// through ExecContext, so `SELECT 1; SELECT 2;` in script mode reports
// "executed 2" and shows nothing — the audit's ruling is: pick an explicit
// product behaviour, don't leave "looks like it worked". The chosen
// behaviour: scripts keep executing everything (that is their point for
// imports), and the result panel explicitly says query statements return no
// result sets in this mode.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const src = readSrc('components/DBQueryEditor.vue')
const script = scriptBody(src)
const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

describe('DBQueryEditor script result disclosure (DB-27)', () => {
  it('detects query statements in the executed script', () => {
    expect(script).toMatch(/const scriptHadQueries = ref\(false\)/)
    expect(script).toMatch(/scriptHadQueries\.value = splitScriptLike\(toRun\)\.some\(isQuerySql\)/)
    // The .sql file runner discloses too.
    expect(script).toMatch(/scriptHadQueries\.value = splitScriptLike\(text\)\.some\(isQuerySql\)/)
  })

  it('shows the note in the script result panel', () => {
    expect(src).toMatch(/v-if="scriptHadQueries" class="script-hint">\{\{ t\('db\.scriptNoResults'\) \}\}/)
  })
})

describe('DB-27 i18n key', () => {
  it.each(locales)('%s defines db.scriptNoResults', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.scriptNoResults'].length).toBeGreaterThan(0)
  })
})
