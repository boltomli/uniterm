// Regression tests for DB-20 (database regression audit 2026-10-25):
// "总数靠估算，可翻到空页；过滤/排序只作用于当前页".
//
// - The page total is an estimate (rows loaded so far + "maybe one more"),
//   so an exactly-full last page offers one page past the end. The editor
//   now steps back automatically instead of stranding the user on an empty
//   page.
// - The filter applies to the loaded page only; the placeholder says so
//   instead of implying a table-wide search.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const editor = scriptBody(readSrc('components/DBQueryEditor.vue'))
const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

describe('DBQueryEditor empty-page back-off (DB-20)', () => {
  it('steps back when an estimated page turns out not to exist', () => {
    const queryBranch = editor.slice(
      editor.indexOf('else if (isQuerySql(toRun))'),
      editor.indexOf('const call = ExecuteStatement'),
    )
    expect(queryBranch).toMatch(/if \(browseMode\.value && result\.rows\.length === 0 && page\.value > 0\)/)
    expect(queryBranch).toMatch(/page\.value -= 1/)
    // The retry happens BEFORE the duration/history bookkeeping so the
    // phantom fetch is not recorded as a real query.
    const backoff = queryBranch.indexOf('page.value -= 1')
    const bookkeeping = queryBranch.indexOf('lastDurationMs.value =')
    expect(backoff).toBeLessThan(bookkeeping)
  })
})

describe('DB-20 filter scope is disclosed (DB-20)', () => {
  it.each(locales)('%s labels the result filter as page-scoped', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.filterResults']).toMatch(/[Pp]ágina|页|頁|page|Seite|pagina|trang|ページ|페이지|страниц/i)
  })
})
