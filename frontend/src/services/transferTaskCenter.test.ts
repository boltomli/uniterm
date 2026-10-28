// security.md finding 17 (Medium): transferTaskCenter appended per-file
// entries into arrays inside the deep-reactive panelState and found them
// with O(n) finds over proxy-wrapped objects — a 100k-file upload became
// 100k reactive objects with a linear scan per event. The per-task
// collection is now a Map<path, entry> whose entries are markRaw'd, plus a
// revision ref so TransferPanel can repaint raw status flips (mutating a
// raw object cannot trigger Vue by itself).
//
// Red-first: these tests fail against the old array implementation
// (`files` is an Array, entries are reactive proxies, no revision export).
// No component-mount infra exists (node env), so the TransferPanel half is
// asserted on the SFC source — same pattern as
// components/BaseTerminal.cleanup.test.ts.

import { describe, it, expect, vi, beforeAll, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { effectScope, watch, isReactive, toRaw } from 'vue'

// panelStore (pulled in by the service) loads the Wails bindings at import
// time; stub them plus the runtime event bus so the graph runs under vitest.
vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  DisableSessionOutputLog: vi.fn(async () => {}),
  RegisterSessionForPanel: vi.fn(async () => {}),
  UnregisterSession: vi.fn(async () => {}),
}))
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

import { Events } from '@wailsio/runtime'
import * as taskCenter from './transferTaskCenter'
import { usePanelStore } from '../stores/panelStore'
import type { TransferFileState } from '../stores/panelStore'
import { readSrc } from '../testUtils/sfc'

type EventMsg = Record<string, unknown>

// Drive the service exactly like the backend does: one captured
// `sftp:transfer` handler registered by registerTransferRoute, then raw
// event payloads through it.
let handler: (ev: { data?: EventMsg }) => void

beforeAll(() => {
  taskCenter.registerTransferRoute('boot', 'boot-list')
  const call = vi.mocked(Events.On).mock.calls.find(c => c[0] === 'sftp:transfer')
  expect(call).toBeTruthy()
  handler = call![1] as unknown as (ev: { data?: EventMsg }) => void
})

function emit(sessionId: string, msg: EventMsg) {
  handler({ data: { ...msg, type: 'sftp:transfer', sessionId } })
}

/** Registers a route for this test and starts one upload task on it. */
function setup(sessionId: string, listKey: string, taskId: string, onDone?: taskCenter.TransferDoneCallback) {
  taskCenter.registerTransferRoute(sessionId, listKey, onDone)
  emit(sessionId, {
    event: 'start', taskId, tfType: 'upload', name: `${taskId} dir`,
    localPath: '/l', remotePath: '/r', fileCount: 0,
  })
}

function filesOf(listKey: string, taskId: string): Map<string, TransferFileState> {
  const task = usePanelStore().getTransferTasks(listKey).find(t => t.id === taskId)
  expect(task).toBeTruthy()
  return task!.files
}

let seq = 0
function keys() {
  seq += 1
  return { sessionId: `sess-${seq}`, listKey: `panel-${seq}` }
}

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('transferTaskCenter per-task file entries', () => {
  it('stores 10000 file-start entries in a Map keyed by path with correct size', () => {
    const { sessionId, listKey } = keys()
    setup(sessionId, listKey, 'big-task')
    for (let i = 0; i < 10000; i++) {
      emit(sessionId, {
        event: 'file-start', taskId: 'big-task',
        file: `f${i}.txt`, name: `f${i}.txt`, fileCount: 10000,
      })
    }
    const files = filesOf(listKey, 'big-task')
    expect(files).toBeInstanceOf(Map)
    expect(files.size).toBe(10000)
    expect(files.get('f9999.txt')).toMatchObject({ path: 'f9999.txt', status: 'running' })
    expect(files.get('f0.txt')).toMatchObject({ path: 'f0.txt', status: 'running' })
  })

  it('interleaved updates hit the same entry without creating duplicates', () => {
    const { sessionId, listKey } = keys()
    setup(sessionId, listKey, 't1')
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'a.txt', name: 'a.txt' })
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'b.txt', name: 'b.txt' })
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'c.txt', name: 'c.txt' })
    const files = filesOf(listKey, 't1')
    const firstA = files.get('a.txt')
    expect(firstA).toBeTruthy()

    // done → repeated done → failed → re-start of the same path: all must
    // resolve onto the existing per-path entry, never append a second one.
    emit(sessionId, { event: 'file-done', taskId: 't1', file: 'a.txt', completedFiles: 1 })
    emit(sessionId, { event: 'file-done', taskId: 't1', file: 'a.txt', completedFiles: 1 })
    emit(sessionId, { event: 'file-failed', taskId: 't1', file: 'b.txt', error: 'boom' })
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'b.txt', name: 'b.txt' })

    expect(files.size).toBe(3)
    expect(files.get('a.txt')).toBe(firstA)
    expect(files.get('a.txt')!.status).toBe('done')
    expect(files.get('b.txt')!.status).toBe('running')
    expect(files.get('c.txt')!.status).toBe('running')
    const task = usePanelStore().getTransferTasks(listKey).find(t => t.id === 't1')!
    expect(task.failedFiles).toHaveLength(1)
    expect(task.failedFiles[0]).toEqual({ path: 'b.txt', error: 'boom' })
  })

  it('entries are raw — never deep-reactive proxies', () => {
    const { sessionId, listKey } = keys()
    setup(sessionId, listKey, 't1')
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'a.txt', name: 'a.txt' })
    emit(sessionId, { event: 'file-done', taskId: 't1', file: 'a.txt', completedFiles: 1 })
    emit(sessionId, { event: 'file-failed', taskId: 't1', file: 'b.txt', error: 'nope' })
    const files = filesOf(listKey, 't1')
    for (const entry of files.values()) {
      expect(isReactive(entry)).toBe(false)
      expect(toRaw(entry)).toBe(entry)
    }
    expect(files.size).toBe(2)
  })

  it('keeps completed entries reachable after complete (existing semantics)', () => {
    const { sessionId, listKey } = keys()
    const onDone = vi.fn()
    setup(sessionId, listKey, 't1', onDone)
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'a.txt', name: 'a.txt' })
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'b.txt', name: 'b.txt' })
    emit(sessionId, { event: 'file-done', taskId: 't1', file: 'a.txt', completedFiles: 1 })
    emit(sessionId, { event: 'file-done', taskId: 't1', file: 'b.txt', completedFiles: 2 })
    emit(sessionId, { event: 'complete', taskId: 't1', status: 'done', fileCount: 2, completedFiles: 2 })

    const task = usePanelStore().getTransferTasks(listKey).find(t => t.id === 't1')!
    expect(task.status).toBe('done')
    expect(task.percentage).toBe(100)
    // Finished tasks (and their per-file entries) stay listed until cleared.
    const files = filesOf(listKey, 't1')
    expect(files.size).toBe(2)
    expect(files.get('a.txt')!.status).toBe('done')
    expect(files.get('b.txt')!.status).toBe('done')
    expect(onDone).toHaveBeenCalledWith('done', 'upload')
  })

  it('task-list trim and clear drop their per-file maps (memory bound)', () => {
    // No recent-ring: current semantics keep every entry for the task's
    // lifetime (the retry skip list needs them), so the bound is the task
    // list itself — tasks trimmed past 80 and cleared tasks go away WITH
    // their maps.
    const { sessionId, listKey } = keys()
    taskCenter.registerTransferRoute(sessionId, listKey)
    for (let i = 0; i < 81; i++) {
      emit(sessionId, { event: 'start', taskId: `task-${i}`, tfType: 'upload', name: `t${i}` })
      emit(sessionId, { event: 'file-start', taskId: `task-${i}`, file: `f${i}.txt`, name: `f${i}.txt` })
    }
    const tasks = usePanelStore().getTransferTasks(listKey)
    expect(tasks).toHaveLength(80)
    expect(tasks.some(t => t.id === 'task-0')).toBe(false)
    expect(tasks[0].id).toBe('task-1')
    expect(tasks[79].files.size).toBe(1)

    usePanelStore().removeTransferTasks(listKey)
    expect(usePanelStore().getTransferTasks(listKey)).toHaveLength(0)
  })

  it('exports a file-entry revision that bumps on every file event', () => {
    const { sessionId, listKey } = keys()
    setup(sessionId, listKey, 't1')
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'a.txt', name: 'a.txt' })
    const before = taskCenter.fileEntriesRevision.value
    emit(sessionId, { event: 'file-done', taskId: 't1', file: 'a.txt', completedFiles: 1 })
    expect(taskCenter.fileEntriesRevision.value).toBeGreaterThan(before)
  })

  it('revision reads re-run watchers of a raw entry status flip (repaint wiring)', () => {
    const { sessionId, listKey } = keys()
    setup(sessionId, listKey, 't1')
    emit(sessionId, { event: 'file-start', taskId: 't1', file: 'a.txt', name: 'a.txt' })

    const seen: string[] = []
    const scope = effectScope()
    scope.run(() => {
      // Mirrors what TransferPanel's row rendering reads: the revision plus
      // the raw entry's status. Raw objects cannot trigger Vue, so only the
      // revision bump re-runs this watcher.
      watch(
        () => {
          void taskCenter.fileEntriesRevision.value
          return filesOf(listKey, 't1').get('a.txt')?.status
        },
        status => { if (status) seen.push(status) },
        { immediate: true, flush: 'sync' },
      )
    })
    emit(sessionId, { event: 'file-done', taskId: 't1', file: 'a.txt', completedFiles: 1 })
    emit(sessionId, { event: 'file-failed', taskId: 't1', file: 'a.txt', error: 'x' })
    scope.stop()

    expect(seen).toEqual(['running', 'done', 'failed'])
  })
})

describe('TransferPanel.vue consumption of the per-file Map (source wiring)', () => {
  const src = readSrc('components/TransferPanel.vue')

  it('reads entries through Map APIs, not array length/slice', () => {
    expect(src).not.toContain('task.files.length')
    expect(src).not.toContain('task.files.slice')
    expect(src).toMatch(/task\.files\.size/)
    expect(src).toMatch(/task\.files\.values\(\)/)
  })

  it('preserves the rendered-row cap of 200 rows', () => {
    expect(src).toMatch(/slice\(-200\)/)
    expect(src).toMatch(/task\.files\.size > 200/)
  })

  it('subscribes to the file-entry revision so raw status flips repaint', () => {
    expect(src).toContain('fileEntriesRevision')
  })

  it('task lists are reactive for consumers that capture the array once (issue #1227 regression)', async () => {
    // FileTabContent grabs the task array once at setup and renders from it;
    // the router pushes into that same array. If the store Map is not deep
    // reactive, the push is invisible to Vue and the transfer panel stays on
    // its empty state even though events flow.
    const { watch, nextTick } = await import('vue')
    taskCenter.registerTransferRoute('sid-r1', 'key-r1')
    const tasks = usePanelStore().getTransferTasks('key-r1')
    const onListChange = vi.fn()
    watch(() => tasks.map(t => t.id).join('|'), onListChange)
    await nextTick()
    expect(onListChange).not.toHaveBeenCalled()

    emit('sid-r1', { event: 'start', taskId: 'up-r1', tfType: 'upload', name: 'big.bin', total: 100 })
    await nextTick()
    expect(onListChange).toHaveBeenCalledTimes(1)

    // A later in-place status write on an existing task must stay visible too
    // (the watch is registered after the push so the push itself doesn't count).
    const onStatusChange = vi.fn()
    watch(() => tasks.map(t => t.status).join('|'), onStatusChange)
    await nextTick()

    emit('sid-r1', { event: 'complete', taskId: 'up-r1', status: 'done' })
    await nextTick()
    expect(onStatusChange).toHaveBeenCalledTimes(1)
  })
})
