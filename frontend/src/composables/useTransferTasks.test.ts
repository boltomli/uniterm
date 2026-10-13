import { describe, it, expect, vi } from 'vitest'
import { nextTick, reactive } from 'vue'

import { buildSkipList, watchNewTransferTasks } from './useTransferTasks'
import type { TransferFileState, TransferTaskUI } from '../stores/panelStore'

describe('watchNewTransferTasks', () => {
  // Regression: watching the tasks array by reference (a computed that only
  // returns it) never fires when tasks are pushed, so the sidebar transfer
  // panel stayed collapsed. The watcher must track the task ids themselves.
  it('fires when a new task id is pushed and not for in-place updates', async () => {
    const tasks = reactive<any[]>([])
    const onNew = vi.fn()
    watchNewTransferTasks(() => tasks, onNew)
    await nextTick()
    expect(onNew).not.toHaveBeenCalled()

    tasks.push({ id: 'dl-1' })
    await nextTick()
    expect(onNew).toHaveBeenCalledTimes(1)

    // Progress mutations must not re-trigger.
    tasks[0].percentage = 50
    await nextTick()
    expect(onNew).toHaveBeenCalledTimes(1)

    tasks.push({ id: 'dl-2' })
    await nextTick()
    expect(onNew).toHaveBeenCalledTimes(2)
  })

  it('treats a pre-populated list as new only when it changes', async () => {
    const tasks = reactive<any[]>([{ id: 'dl-1' }])
    const onNew = vi.fn()
    watchNewTransferTasks(() => tasks, onNew)
    await nextTick()
    // Lazy watch: registration alone does not expand the panel.
    expect(onNew).not.toHaveBeenCalled()

    tasks.push({ id: 'dl-1b' })
    await nextTick()
    expect(onNew).toHaveBeenCalledTimes(1)
  })
})

describe('buildSkipList', () => {
  // TransferTaskUI.files is a Map<path, entry> (security.md finding 17), so
  // the skip list is the done entries' paths in insertion order.
  it('returns the relative paths of files already completed', () => {
    const task: Pick<TransferTaskUI, 'files'> = {
      files: new Map<string, TransferFileState>([
        ['a.txt', { path: 'a.txt', status: 'done' }],
        ['b.txt', { path: 'b.txt', status: 'failed' }],
        ['c.txt', { path: 'c.txt', status: 'running' }],
        ['d.txt', { path: 'd.txt', status: 'done' }],
      ]),
    }
    expect(buildSkipList(task)).toEqual(['a.txt', 'd.txt'])
  })

  it('returns an empty skip list for a task with no completed files', () => {
    const task: Pick<TransferTaskUI, 'files'> = {
      files: new Map<string, TransferFileState>([['b.txt', { path: 'b.txt', status: 'failed' }]]),
    }
    expect(buildSkipList(task)).toEqual([])
  })
})
