import { describe, it, expect, vi, beforeEach } from 'vitest'

const {
  mockAppendFileBase64,
  mockOpenDirectoryDialog,
  mockOpenMultipleFilesDialog,
  mockSessionEndZmodem,
  mockSessionWrite,
  mockSessionWriteBinary,
  mockWriteFileBase64,
  sentryInstances,
} = vi.hoisted(() => {
  const sentryInstances: any[] = []
  return {
    mockAppendFileBase64: vi.fn().mockResolvedValue(undefined),
    mockOpenDirectoryDialog: vi.fn().mockResolvedValue('C:\\Downloads'),
    mockOpenMultipleFilesDialog: vi.fn().mockResolvedValue([]),
    mockSessionEndZmodem: vi.fn().mockResolvedValue(undefined),
    mockSessionWrite: vi.fn().mockResolvedValue(undefined),
    mockSessionWriteBinary: vi.fn().mockResolvedValue(undefined),
    mockWriteFileBase64: vi.fn().mockResolvedValue(undefined),
    sentryInstances,
  }
})

vi.mock('zmodem.js/src/zmodem_browser', () => ({
  default: {
    Sentry: vi.fn(function (this: any, options) {
      sentryInstances.push(options)
      this.consume = vi.fn()
    }),
  },
}))

vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  AppendFileBase64: mockAppendFileBase64,
  OpenDirectoryDialog: mockOpenDirectoryDialog,
  OpenMultipleFilesDialog: mockOpenMultipleFilesDialog,
  ReadFileBase64: vi.fn(),
  SessionEndZmodem: mockSessionEndZmodem,
  SessionWrite: mockSessionWrite,
  SessionWriteBinary: mockSessionWriteBinary,
  WriteFileBase64: mockWriteFileBase64,
}))

const mockStore = {
  addTransfer: vi.fn(),
  updateTransfer: vi.fn(),
  getPendingUploadFiles: vi.fn(),
}

vi.mock('../stores/zmodemStore', () => ({
  useZmodemStore: vi.fn(() => mockStore),
}))

import { startZmodemService } from './zmodemService'

const BATCH = 64 * 1024

async function sleepTicks() {
  for (let i = 0; i < 100; i++) {
    await Promise.resolve()
  }
}

function base64ToBytes(b64: string): number[] {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return Array.from(out)
}

// Builds a fake zmodem 'receive' session + offer for a download of the given
// byte chunks. Returns handles the test can drive.
function makeDownload(chunks: number[][]) {
  const state: {
    offerHandler?: (offer: any) => void | Promise<void>
    accept?: any
    zsession: any
  } = {} as any
  const offer = {
    get_details: () => ({ name: 'large.bin', size: chunks.flat().length }),
    accept: vi.fn(async (options?: { on_input?: (payload: number[]) => void }) => {
      state.accept = { options }
      for (const c of chunks) options?.on_input?.(c)
    }),
    skip: vi.fn(),
  }
  const zsession = {
    type: 'receive',
    on: vi.fn((event: string, handler: (offer: any) => void | Promise<void>) => {
      if (event === 'offer') state.offerHandler = handler
    }),
    // The real session's start() resolves when the ZMODEM protocol ends (all
    // file data received), which is decoupled from our disk writes. We fire
    // the offer handler in the background and resolve immediately, mirroring
    // that: on_input runs synchronously, but its writes may still be pending
    // on disk.
    start: vi.fn(() => {
      state.offerHandler?.(offer)
      return Promise.resolve()
    }),
    abort: vi.fn(),
    close: vi.fn(),
  }
  state.zsession = zsession
  return state
}

// Builds a fake zmodem 'send' session (the local side of an rz upload).
function makeUpload() {
  const zsession = {
    type: 'send',
    on: vi.fn(),
    abort: vi.fn(),
    close: vi.fn(),
    send_offer: vi.fn(),
  }
  return { zsession }
}

describe('startZmodemService', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    sentryInstances.length = 0
    mockOpenDirectoryDialog.mockResolvedValue('C:\\Downloads')
    mockOpenMultipleFilesDialog.mockResolvedValue([])
    mockAppendFileBase64.mockResolvedValue(undefined)
  })

  it('buffers small sz chunks into a single batched disk write', async () => {
    vi.useFakeTimers()
    const s = makeDownload([
      [1, 2, 3],
      [4, 5],
    ])
    const onComplete = vi.fn()
    startZmodemService({ sessionId: 's1', onComplete })

    sentryInstances[0].on_detect({ confirm: () => s.zsession })
    await sleepTicks()
    await vi.runOnlyPendingTimersAsync()
    vi.useRealTimers()

    // Only a single batched Write — not one per tiny chunk.
    expect(mockAppendFileBase64).toHaveBeenCalledTimes(1)
    expect(mockAppendFileBase64).toHaveBeenNthCalledWith(1, 'C:\\Downloads\\large.bin', 'AQIDBAU=', 0)
    expect(mockWriteFileBase64).not.toHaveBeenCalled()
    expect(onComplete).toHaveBeenCalledWith(['C:\\Downloads\\large.bin'])
    expect(mockSessionEndZmodem).toHaveBeenCalledWith('s1')
  })

  it('flushes an in-flight batch once the buffer exceeds the batch size', async () => {
    vi.useFakeTimers()
    // 3 × 30KB = 90KB total → first flush at 64KB boundary, remainder at end.
    const make = (v: number, len: number) => Array.from({ length: len }, () => v)
    const s = makeDownload([
      make(1, 30000),
      make(2, 30000),
      make(3, 30000),
    ])
    const onComplete = vi.fn()
    startZmodemService({ sessionId: 's1', onComplete })

    sentryInstances[0].on_detect({ confirm: () => s.zsession })
    await sleepTicks()
    await vi.runOnlyPendingTimersAsync()
    vi.useRealTimers()

    expect(mockAppendFileBase64).toHaveBeenCalledTimes(2)
    const c0 = mockAppendFileBase64.mock.calls[0]
    const c1 = mockAppendFileBase64.mock.calls[1]
    expect(c0[2]).toBe(0)
    expect(c1[2]).toBe(BATCH)
    // Reassembled file content equals the source bytes, in order.
    const reassembled = base64ToBytes(c0[1]).concat(base64ToBytes(c1[1]))
    expect(reassembled.length).toBe(90000)
    expect(reassembled[0]).toBe(1)
    expect(reassembled[65536]).toBe(3)
    expect(reassembled[89999]).toBe(3)
  })

  it('waits for the final disk write before completing the download', async () => {
    vi.useFakeTimers()
    const s = makeDownload([[1, 2, 3, 4, 5]])
    const onComplete = vi.fn()

    // Simulate a slow disk: the append write stays pending until we resolve it.
    const appendResolvers: (() => void)[] = []
    mockAppendFileBase64.mockImplementation(
      () => new Promise<void>((res) => appendResolvers.push(res)),
    )

    startZmodemService({ sessionId: 's1', onComplete })

    sentryInstances[0].on_detect({ confirm: () => s.zsession })
    await sleepTicks()

    // Let the 2s idle watchdog fire while the disk write is still pending.
    await vi.runOnlyPendingTimersAsync()

    // BUG: the old code completed here (files empty → "Zmodem transfer
    // cancelled"). The fix must not call onComplete until the write drains.
    expect(onComplete).not.toHaveBeenCalled()
    expect(mockAppendFileBase64).toHaveBeenCalledTimes(1)

    // Now the disk catches up.
    appendResolvers[0]()
    await sleepTicks()
    await vi.runOnlyPendingTimersAsync()
    vi.useRealTimers()

    expect(onComplete).toHaveBeenCalledWith(['C:\\Downloads\\large.bin'])
    expect(mockSessionEndZmodem).toHaveBeenCalledWith('s1')
  })

  // Wails v3 rejects the dialog promise with "cancelled by user" when the
  // user dismisses the picker. The service must treat that as a cancel:
  // abort the zsession (so the remote rz exits), end zmodem mode, and fire
  // the cancel path — not report an error.
  it('treats upload dialog rejection as a user cancel', async () => {
    vi.useFakeTimers()
    const u = makeUpload()
    const onComplete = vi.fn()
    const onError = vi.fn()
    mockOpenMultipleFilesDialog.mockRejectedValue(
      Object.assign(new Error('cancelled by user'), { name: 'RuntimeError' }),
    )

    startZmodemService({ sessionId: 's1', onComplete, onError })
    sentryInstances[0].on_detect({ confirm: () => u.zsession })
    await sleepTicks()
    await vi.runOnlyPendingTimersAsync()
    vi.useRealTimers()

    expect(u.zsession.abort).toHaveBeenCalled()
    expect(mockSessionEndZmodem).toHaveBeenCalledWith('s1')
    expect(onComplete).toHaveBeenCalledWith([])
    expect(onError).not.toHaveBeenCalled()
  })

  it('treats download dialog rejection as a user cancel', async () => {
    vi.useFakeTimers()
    const s = makeDownload([[1, 2, 3]])
    const onComplete = vi.fn()
    const onError = vi.fn()
    mockOpenDirectoryDialog.mockRejectedValue(
      Object.assign(new Error('cancelled by user'), { name: 'RuntimeError' }),
    )

    startZmodemService({ sessionId: 's1', onComplete, onError })
    sentryInstances[0].on_detect({ confirm: () => s.zsession })
    await sleepTicks()
    await vi.runOnlyPendingTimersAsync()
    vi.useRealTimers()

    expect(s.zsession.abort).toHaveBeenCalled()
    expect(mockSessionEndZmodem).toHaveBeenCalledWith('s1')
    expect(onComplete).toHaveBeenCalledWith([])
    expect(onError).not.toHaveBeenCalled()
  })
})