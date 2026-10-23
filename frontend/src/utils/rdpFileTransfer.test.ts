import { afterEach, describe, expect, it, vi } from 'vitest'
import { RdpFileTransfer } from './rdpFileTransfer'

function fixture() {
  const bridge = {
    invoke: vi.fn((_name: string, _value: unknown): unknown => undefined),
    readChunk: vi.fn(async (_id: string, _offset: number, _size: number) => btoa('chunk')),
    getFiles: vi.fn(async () => [{ id: 'download', name: 'test.txt', size: 5, lastModified: 1 }]),
    begin: vi.fn(async (_files: Array<{ name: string; size: number; lastModified: number }>) => 'transfer'),
    writeChunk: vi.fn(async (_id: string, _index: number, _offset: number, _data: string) => {}),
    finish: vi.fn(async (_id: string) => {}),
    abort: vi.fn(async (_id: string) => {}),
    log: vi.fn(),
  }
  return { bridge, transfer: new RdpFileTransfer(bridge) }
}

afterEach(() => vi.useRealTimers())

describe('RDP clipboard files', () => {
  it('answers SIZE and RANGE uploads using a stable local file token', async () => {
    const { bridge, transfer } = fixture()
    transfer.setLocalFiles([{ id: 'local-file', name: '本地.txt', size: 2 ** 33 + 5, lastModified: 1 }])
    await transfer.onRequest({ streamId: 10, index: 0, flags: 1, position: 0, size: 8 })
    const size = (bridge.invoke.mock.calls[0][1] as { data: Uint8Array }).data
    expect(new DataView(size.buffer).getBigUint64(0, true)).toBe(2n ** 33n + 5n)
    await transfer.onRequest({ streamId: 11, index: 0, flags: 2, position: 123, size: 5 })
    expect(bridge.readChunk).toHaveBeenCalledWith('local-file', 123, 5)
    expect(bridge.invoke).toHaveBeenLastCalledWith('submit_file_contents', {
      stream_id: 11, is_error: false, data: new TextEncoder().encode('chunk'),
    })
  })

  it('keeps the old file snapshot while the remote holds a clipboard lock', async () => {
    const { bridge, transfer } = fixture()
    transfer.setLocalFiles([{ id: 'old', name: 'old.txt', size: 5, lastModified: 0 }])
    transfer.lock(7)
    transfer.setLocalFiles([{ id: 'new', name: 'new.txt', size: 5, lastModified: 0 }])
    await transfer.onRequest({ streamId: 1, index: 0, flags: 2, position: 0, size: 5, dataId: 7 })
    expect(bridge.readChunk).toHaveBeenCalledWith('old', 0, 5)
  })

  it('sends an error response for invalid ranges or unreadable local files', async () => {
    const { bridge, transfer } = fixture()
    await transfer.onRequest({ streamId: 2, index: -1, flags: 2, position: 0, size: 10 })
    expect(bridge.invoke).toHaveBeenCalledWith('submit_file_contents', expect.objectContaining({ stream_id: 2, is_error: true }))
    transfer.setLocalFiles([{ id: 'missing', name: 'gone.txt', size: 10, lastModified: 0 }])
    bridge.readChunk.mockRejectedValueOnce(new Error('deleted'))
    await transfer.onRequest({ streamId: 3, index: 0, flags: 2, position: 0, size: 10 })
    expect(bridge.invoke).toHaveBeenLastCalledWith('submit_file_contents', expect.objectContaining({ stream_id: 3, is_error: true }))
  })

  it('downloads binary chunks and only publishes complete files', async () => {
    const { bridge, transfer } = fixture()
    const bytes = new Uint8Array(1024 * 1024 + 17).map((_, i) => i % 256)
    bridge.invoke.mockImplementation((name, value) => {
      if (name !== 'request_file_contents') return
      const request = value as { stream_id: number; flags: number; position: number; size: number }
      const data = request.flags === 1 ? new Uint8Array(8) : bytes.slice(request.position, request.position + request.size)
      if (request.flags === 1) new DataView(data.buffer).setBigUint64(0, BigInt(bytes.length), true)
      queueMicrotask(() => transfer.onResponse({ streamId: request.stream_id, isError: false, data }))
    })
    await transfer.download([{ name: '二进制.dat', size: 0, lastModified: 42 }], 77)
    expect(bridge.begin).toHaveBeenCalledWith([{ name: '二进制.dat', size: bytes.length, lastModified: 42 }])
    expect(bridge.writeChunk).toHaveBeenCalledTimes(2)
    expect(bridge.writeChunk.mock.calls[1]).toEqual(['transfer', 0, 1024 * 1024, btoa(String.fromCharCode(...bytes.slice(1024 * 1024)))])
    expect(bridge.invoke).toHaveBeenCalledWith('request_file_contents', expect.objectContaining({ clip_data_id: 77 }))
    expect(bridge.finish).toHaveBeenCalledWith('transfer')
    expect(bridge.abort).not.toHaveBeenCalled()
  })

  it('aborts partial downloads if the server fails a chunk', async () => {
    const { bridge, transfer } = fixture()
    bridge.invoke.mockImplementation((_name, value) => {
      const request = value as { stream_id: number; flags: number }
      const data = new Uint8Array(8)
      new DataView(data.buffer).setBigUint64(0, 5n, true)
      queueMicrotask(() => transfer.onResponse({ streamId: request.stream_id, isError: request.flags === 2, data }))
    })
    await expect(transfer.download([{ name: 'test.txt', size: 5, lastModified: 0 }])).rejects.toThrow('read failed')
    expect(bridge.abort).toHaveBeenCalledWith('transfer')
    expect(bridge.finish).not.toHaveBeenCalled()
  })

  it('cancels pending file requests on disconnect', async () => {
    const { transfer, bridge } = fixture()
    const download = transfer.download([{ name: 'test.txt', size: 5, lastModified: 0 }])
    transfer.dispose()
    await expect(download).rejects.toThrow('cancelled')
    expect(bridge.finish).not.toHaveBeenCalled()
  })

  it('times out stalled remote file requests', async () => {
    vi.useFakeTimers()
    const { transfer } = fixture()
    const failed = expect(transfer.download([{ name: 'test.txt', size: 5, lastModified: 0 }])).rejects.toThrow('timed out')
    await vi.advanceTimersByTimeAsync(15001)
    await failed
    transfer.dispose()
  })
})
