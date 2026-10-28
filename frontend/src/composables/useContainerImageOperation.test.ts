import { beforeEach, describe, expect, it, vi } from 'vitest'
vi.mock('../services/containerClient', () => ({ prepareImageOperation: vi.fn(), cancelImageOperation: vi.fn() }))
import * as client from '../services/containerClient'
import { useContainerImageOperation } from './useContainerImageOperation'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((ok, fail) => { resolve = ok; reject = fail })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(client.prepareImageOperation).mockResolvedValue('operation')
  vi.mocked(client.cancelImageOperation).mockResolvedValue(undefined)
})

describe('container image operations', () => {
  it('prepares before executing and rejects duplicate submissions', async () => {
    const task = useContainerImageOperation()
    const result = deferred<string>()
    const execute = vi.fn(() => result.promise)
    const pending = task.run('docker-connection', execute)
    expect(task.busy.value).toBe(true)
    expect(await task.run('docker-connection', execute)).toBe(false)
    await Promise.resolve()
    expect(client.prepareImageOperation).toHaveBeenCalledWith('docker-connection')
    expect(execute).toHaveBeenCalledExactlyOnceWith('operation')
    result.resolve('Loaded image: test:latest')
    expect(await pending).toBe(true)
    expect(task.busy.value).toBe(false)
    expect(task.error.value).toBe('')
  })

  it('cancels an operation allocated after the dialog was already closed', async () => {
    const prepared = deferred<string>()
    vi.mocked(client.prepareImageOperation).mockReturnValueOnce(prepared.promise)
    const task = useContainerImageOperation()
    const execute = vi.fn()
    const pending = task.run('connection', execute)
    task.cancel()
    prepared.resolve('late-operation')
    expect(await pending).toBe(false)
    expect(execute).not.toHaveBeenCalled()
    expect(client.cancelImageOperation).toHaveBeenCalledWith('late-operation')
  })

  it('does not let a cancelled request update a newly opened operation', async () => {
    const oldResult = deferred<string>()
    const task = useContainerImageOperation()
    const old = task.run('connection', () => oldResult.promise)
    await Promise.resolve()
    task.cancel()
    expect(client.cancelImageOperation).toHaveBeenCalledWith('operation')
    const newResult = deferred<string>()
    vi.mocked(client.prepareImageOperation).mockResolvedValueOnce('new-operation')
    const current = task.run('connection', () => newResult.promise)
    oldResult.reject(new Error('old error'))
    expect(await old).toBe(false)
    expect(task.busy.value).toBe(true)
    expect(task.error.value).toBe('')
    newResult.resolve('sha256:image')
    expect(await current).toBe(true)
    expect(task.busy.value).toBe(false)
  })

  it('surfaces daemon errors and allows a fresh retry', async () => {
    const task = useContainerImageOperation()
    expect(await task.run('connection', async () => { throw new Error('invalid archive') })).toBe(false)
    expect(task.error.value).toBe('invalid archive')
    expect(task.busy.value).toBe(false)
    expect(await task.run('connection', async () => 'loaded')).toBe(true)
    expect(task.error.value).toBe('')
  })
})
