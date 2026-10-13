// panelStore's TransferTaskUI.files contract (security.md finding 17):
// per-task file detail is a Map<path, entry> whose entries are markRaw'd,
// so a large directory transfer does not become thousands of deep-reactive
// proxies inside the panelState, and removeTransferTasks drops the task
// together with its file map — natural GC, transferTaskCenter keeps no
// residual per-task map of its own.
//
// Tasks are only ever created by transferTaskCenter's event flow, so these
// tests drive the store through it (same harness as
// services/transferTaskCenter.test.ts).

import { describe, it, expect, vi, beforeAll, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { isReactive, toRaw } from 'vue'

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  DisableSessionOutputLog: vi.fn(async () => {}),
  RegisterSessionForPanel: vi.fn(async () => {}),
  UnregisterSession: vi.fn(async () => {}),
}))
vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

import { Events } from '@wailsio/runtime'
import { registerTransferRoute } from '../services/transferTaskCenter'
import { usePanelStore } from './panelStore'

type EventMsg = Record<string, unknown>

let handler: (ev: { data?: EventMsg }) => void
beforeAll(() => {
  registerTransferRoute('boot', 'boot-list')
  const call = vi.mocked(Events.On).mock.calls.find(c => c[0] === 'sftp:transfer')
  expect(call).toBeTruthy()
  handler = call![1] as unknown as (ev: { data?: EventMsg }) => void
})

beforeEach(() => {
  setActivePinia(createPinia())
})

/** Starts one task on its own session/list key and feeds it two files. */
function seedTask(sessionId: string, listKey: string, taskId: string) {
  registerTransferRoute(sessionId, listKey)
  const emit = (msg: EventMsg) => handler({ data: { ...msg, type: 'sftp:transfer', sessionId } })
  emit({ event: 'start', taskId, tfType: 'download', name: 'dir' })
  emit({ event: 'file-start', taskId, file: '/r/a.bin', name: 'a.bin' })
  emit({ event: 'file-start', taskId, file: '/r/b.bin', name: 'b.bin' })
}

describe('panelStore TransferTaskUI.files', () => {
  it('holds per-task file entries as a Map keyed by path', () => {
    seedTask('sess-map', 'list-map', 't1')
    const task = usePanelStore().getTransferTasks('list-map').find(t => t.id === 't1')!
    expect(task.files).toBeInstanceOf(Map)
    expect(task.files.size).toBe(2)
    expect(task.files.get('/r/a.bin')).toMatchObject({ path: '/r/a.bin', status: 'running' })
    expect(task.files.get('/r/b.bin')).toMatchObject({ path: '/r/b.bin', status: 'running' })
  })

  it('keeps entries raw inside the deep-reactive panelState', () => {
    seedTask('sess-raw', 'list-raw', 't1')
    const task = usePanelStore().getTransferTasks('list-raw').find(t => t.id === 't1')!
    for (const entry of task.files.values()) {
      expect(isReactive(entry)).toBe(false)
      expect(toRaw(entry)).toBe(entry)
    }
    expect(task.files.size).toBe(2)
  })

  it('removeTransferTasks drops the task and its file map together', () => {
    seedTask('sess-gc', 'list-gc', 't1')
    const store = usePanelStore()
    expect(store.getTransferTasks('list-gc')).toHaveLength(1)
    store.removeTransferTasks('list-gc')
    // The task (and its Map) becomes unreachable with the list — nothing to
    // sweep on the service side.
    expect(store.getTransferTasks('list-gc')).toHaveLength(0)
  })
})
