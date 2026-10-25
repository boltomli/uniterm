// Regression tests for DB-17 (database regression audit 2026-10-25):
// "树刷新无时序保护，且每次刷新都切换前台页签". loadTree awaited GetDatabases /
// GetTables with no ordering token, so a rapid session switch let the OLD
// session's response repaint the new session's tree; and every refresh
// emitted openDatabase, yanking the user back to the objects tab.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC source.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const script = scriptBody(readSrc('components/DBTreePanel.vue'))
const locales = ['de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'vi', 'zh-CN', 'zh-TW']

function bodyOf(src: string, startMarker: string, endMarker: string): string {
  const start = src.indexOf(startMarker)
  expect(start).toBeGreaterThan(-1)
  const end = src.indexOf(endMarker, start + startMarker.length)
  expect(end).toBeGreaterThan(start)
  return src.slice(start, end)
}

describe('DBTreePanel load ordering (DB-17)', () => {
  it('tokens each loadTree run and drops stale responses', () => {
    const load = bodyOf(script, 'async function loadTree', 'watch(() => props.sessionId')
    expect(load).toMatch(/const myTree = \+\+treeSeq/)
    // Guards after every await: GetDatabases and each GetTables.
    const guards = load.match(/myTree !== treeSeq/g) ?? []
    expect(guards.length).toBeGreaterThanOrEqual(4)
    // The finally block must only settle loading for the owning run.
    expect(load).toMatch(/if \(myTree === treeSeq\) loading\.value = false/)
  })

  it('clears state and invalidates in-flight loads on session switch', () => {
    const watcher = bodyOf(script, 'watch(() => props.sessionId, (newId)', 'watch(')
    expect(watcher).toMatch(/treeSeq\+\+/)
    expect(watcher).toMatch(/autoOpened = false/)
    expect(watcher).toMatch(/databases\.value = \[\]/)
    expect(watcher).toMatch(/selectedDb\.value = ''/)
  })

  it('auto-opens only on the first successful load', () => {
    const load = bodyOf(script, 'async function loadTree', 'watch(() => props.sessionId')
    expect(load).toMatch(/if \(autoOpen && !autoOpened\)/)
    expect(load).toMatch(/autoOpened = true/)
  })

  it('surfaces load failures instead of console-only logging', () => {
    const load = bodyOf(script, 'async function loadTree', 'watch(() => props.sessionId')
    expect(load).toMatch(/msg\.error\(t\('db\.treeLoadFailed'/)
  })
})

describe('DB-17 i18n key', () => {
  it.each(locales)('%s defines db.treeLoadFailed with the {msg} param', (loc) => {
    const dict = JSON.parse(readSrc(`i18n/locales/${loc}.json`))
    expect(dict['db.treeLoadFailed']).toMatch(/\{msg\}/)
  })
})
