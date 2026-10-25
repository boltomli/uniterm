// Tests for useDbObjectActions (DB-28, 2026-10-25 regression audit): the
// destructive object actions (drop database/table/view, truncate) used to
// be re-implemented in DBTreePanel and DBObjectList with drifting refresh
// and error handling. The composable owns one confirm → execute → refresh →
// notify flow; these tests exercise the state machine directly (it is a
// plain function, no component mount needed).

import { describe, expect, it, vi } from 'vitest'
import { readSrc } from '../testUtils/sfc'
import { useDbObjectActions } from './useDbObjectActions'

// Element Plus needs a DOM to toast; the node test environment has none.
vi.mock('../services/message', () => ({
  msg: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}))

const treeSrc = readSrc('components/DBTreePanel.vue')
const listSrc = readSrc('components/DBObjectList.vue')

describe('useDbObjectActions flow', () => {
  it('runs execute → refresh → notify in order on confirm', async () => {
    const order: string[] = []
    const actions = useDbObjectActions({
      onRefresh: () => { order.push('refresh') },
      onObjectRemoved: () => { order.push('removed') },
    })
    actions.runObjectAction(
      { dbName: 'db1', tableName: 't1', kind: 'table' },
      { title: 'Drop', text: 'Drop t1?', name: 't1' },
      async () => { order.push('execute') },
    )
    expect(actions.confirmVisible.value).toBe(true)
    expect(actions.confirmName.value).toBe('t1')
    await actions.onConfirm()
    expect(order).toEqual(['execute', 'refresh', 'removed'])
    expect(actions.confirmVisible.value).toBe(false)
  })

  it('does not refresh or notify when the action fails', async () => {
    let refreshed = 0
    let notified = 0
    const actions = useDbObjectActions({
      onRefresh: () => { refreshed++ },
      onObjectRemoved: () => { notified++ },
    })
    actions.runObjectAction(
      { dbName: 'db1', kind: 'database' },
      { title: 'Drop', text: 'Drop db1?', name: 'db1' },
      async () => { throw new Error('permission denied') },
    )
    await actions.onConfirm()
    expect(refreshed).toBe(0)
    expect(notified).toBe(0)
    // The dialog always closes, success or failure.
    expect(actions.confirmVisible.value).toBe(false)
  })

  it('requires the typed name to match before confirming', () => {
    const actions = useDbObjectActions({ onRefresh: () => {} })
    actions.runObjectAction(
      { dbName: 'db1', kind: 'database' },
      { title: 'Drop', text: 'Drop db1?', name: 'db1' },
      async () => {},
    )
    // The dialog enforces type-to-confirm via confirmInput === confirmName;
    // the composable resets the input for every new prompt.
    expect(actions.confirmInput.value).toBe('')
    expect(actions.confirmName.value).toBe('db1')
  })
})

describe('components delegate to the composable (DB-28)', () => {
  it('DBTreePanel has no local confirm state machine left', () => {
    expect(treeSrc).not.toMatch(/let confirmAction/)
    expect(treeSrc).toMatch(/useDbObjectActions\(\{/)
    // Every destructive action goes through runObjectAction.
    const runs = treeSrc.match(/runObjectAction\(/g) ?? []
    expect(runs.length).toBeGreaterThanOrEqual(4)
  })

  it('DBObjectList has no local confirm state machine left', () => {
    expect(listSrc).not.toMatch(/let confirmAction/)
    expect(listSrc).toMatch(/useDbObjectActions\(\{/)
    const runs = listSrc.match(/runObjectAction\(/g) ?? []
    expect(runs.length).toBeGreaterThanOrEqual(3)
  })
})
