// Regression tests for DB-20 (database regression audit 2026-10-25):
// "过滤/排序只作用于当前页" — the quick filter applies to the LOADED page only
// (browse mode filters server-side via DBBrowseTable instead), and the
// placeholder says so instead of implying a table-wide search.
//
// The empty-page back-off half of the original DB-20 no longer applies: the
// browse pager is now server-driven (DBBrowseTable + HasMore), so the total
// is exact and a page past the end cannot be offered.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const editor = scriptBody(readSrc('components/DBQueryEditor.vue'))
const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

describe('DBQueryEditor browse paging is server-driven (DB-20)', () => {
  it('derives hasMore from the browse response, not an estimate', () => {
    expect(editor).toMatch(/browseHasMore\.value = result\.hasMore === true/)
  })

  it('applies the quick filter to the loaded page only', () => {
    // displayRows filters the in-memory page; in browse mode the database-side
    // filter/sort (DBBrowseTable) coexists with it.
    expect(editor).toMatch(/rows = rows\.filter\(row =>/)
    expect(editor).toMatch(/Object\.values\(row\)\.some/)
  })
})

describe('DB-20 filter scope is disclosed (DB-20)', () => {
  it.each(locales)('%s labels the result filter as page-scoped', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.filterResults']).toMatch(/[Pp]ágina|页|頁|page|Seite|pagina|trang|ページ|페이지|страниц/i)
  })
})
