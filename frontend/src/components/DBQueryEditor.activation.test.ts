// Regression test for DB-14 (database regression audit 2026-10-25):
// "打开『结构』页也会跑一次 browse SELECT". DBTabContent keeps the editor
// mounted (v-show) so staged edits survive sub-tab switches, and its
// onMounted autoRun therefore fired `SELECT * … LIMIT 100` even when the
// user opened the table on the STRUCTURE tab — an unwanted query (and lock
// risk) on big or slow views. The editor now takes an `active` flag and
// runs the browse query on first activation of the data tab instead.
//
// No component-mount tests exist (node env — see testUtils/sfc.ts), so the
// wiring is asserted on the SFC sources.

import { describe, expect, it } from 'vitest'
import { readSrc, scriptBody } from '../testUtils/sfc'

const editor = scriptBody(readSrc('components/DBQueryEditor.vue'))
const tabSrc = readSrc('components/DBTabContent.vue')

describe('DBQueryEditor activation gating (DB-14)', () => {
  it('declares the active prop (default true)', () => {
    expect(editor).toMatch(/active\?: boolean/)
    expect(editor).toMatch(/active: true,/)
  })

  it('mount and table-change auto-runs require the tab to be active', () => {
    expect(editor).toMatch(/if \(props\.autoRun && props\.active\) await onExecute\(\)/)
    const occurrences = editor.match(/if \(props\.autoRun && props\.active\) await onExecute\(\)/g) ?? []
    expect(occurrences.length).toBe(2) // onMounted + tableName watch
  })

  it('runs once on first activation, never re-running an existing result', () => {
    const watcher = editor.slice(
      editor.indexOf('watch(() => props.active'),
      editor.indexOf('onMounted(async () =>'),
    )
    expect(watcher).toMatch(/if \(!active \|\| !props\.tableName \|\| !props\.autoRun\) return/)
    // A tab that already holds a result (possibly with staged edits) must
    // not re-execute when re-activated.
    expect(watcher).toMatch(/if \(queryResult\.value \|\| scriptResult\.value \|\| execResult\.value\) return/)
    expect(watcher).toMatch(/await onExecute\(\)/)
  })
})

describe('DBTabContent binds the sub-tab to active (DB-14)', () => {
  it('passes subTab === data as the editor active flag', () => {
    expect(tabSrc).toMatch(/:active="doc\.subTab === 'data'"/)
  })
})
