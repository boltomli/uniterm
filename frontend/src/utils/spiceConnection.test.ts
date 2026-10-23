import { afterEach, describe, expect, it, vi } from 'vitest'
import { getSpiceConnectionMonitor } from './spiceConnection'

class Socket extends EventTarget {
  readyState = 1
  close() {
    this.readyState = 3
    this.dispatchEvent(new Event('close'))
  }
}

function fixture() {
  vi.useFakeTimers()
  function channel() {
    const socket = new Socket()
    return {
      state: 'connecting',
      ws: socket as unknown as WebSocket,
      socket,
      onerror: undefined as ((error: unknown) => void) | undefined,
      process_channel_message: vi.fn(() => true),
    }
  }
  const display = { ...channel(), primary_surface: undefined as number | undefined, surfaces: {} as Record<number, { canvas: { width: number; height: number } }> }
  const inputs = channel()
  const connection = {
    ...channel(),
    display: undefined as typeof display | undefined,
    inputs: undefined as typeof inputs | undefined,
    agent_connected: true,
    process_channel_message: vi.fn((message: { type: number }) => {
      if (message.type === 103) connection.state = 'ready'
      if (message.type === 104) { connection.display = display; connection.inputs = inputs }
      return true
    }),
    stop: vi.fn(() => { for (const item of [connection, display, inputs]) item.socket.close() }),
  }
  const monitor = getSpiceConnectionMonitor(connection)
  const changed = vi.fn()
  const unsubscribe = monitor.subscribe(changed)
  const message = (type: number) => ({ type, data: new ArrayBuffer(0) })
  function handshake() {
    connection.process_channel_message(message(103))
    connection.process_channel_message(message(104))
  }
  function surface() {
    display.state = 'ready'
    display.primary_surface = 0
    display.surfaces[0] = { canvas: { width: 1280, height: 720 } }
    display.process_channel_message()
  }
  function inputReady() {
    inputs.state = 'ready'
    inputs.process_channel_message()
  }
  return { connection, display, inputs, monitor, changed, unsubscribe, handshake, surface, inputReady }
}

afterEach(() => { vi.useRealTimers() })

describe('SPICE connection state', () => {
  it('keeps connecting after the local proxy and main handshake are available', () => {
    const f = fixture()
    f.handshake()
    expect(f.changed).toHaveBeenCalledExactlyOnceWith({ status: 'connecting', error: '' })
    f.monitor.stop()
  })

  it('requires the display surface and inputs channel before reporting connected', () => {
    const f = fixture()
    f.handshake()
    f.surface()
    expect(f.changed).toHaveBeenCalledTimes(1)
    f.inputReady()
    expect(f.changed).toHaveBeenLastCalledWith({ status: 'connected', error: '' })
    vi.advanceTimersByTime(60000)
    expect(f.connection.stop).not.toHaveBeenCalled()
    f.monitor.stop()
  })

  it('also connects when inputs are ready before the display surface', () => {
    const f = fixture()
    f.handshake()
    f.inputReady()
    expect(f.changed).toHaveBeenCalledTimes(1)
    f.surface()
    expect(f.changed).toHaveBeenLastCalledWith({ status: 'connected', error: '' })
    f.monitor.stop()
  })

  it('reports main authentication failures and closes the client once', () => {
    const f = fixture()
    const fail = f.connection.onerror!
    fail(new Error('Permission denied.'))
    fail(new Error('late close'))
    vi.advanceTimersByTime(60000)
    expect(f.changed).toHaveBeenLastCalledWith({ status: 'error', error: 'Permission denied.' })
    expect(f.connection.stop).toHaveBeenCalledOnce()
    expect(f.connection.agent_connected).toBe(false)
  })

  it.each(['display', 'inputs'] as const)('propagates errors from the %s channel', name => {
    const f = fixture()
    f.handshake()
    expect(() => f[name].onerror!(new Error(`${name} rejected`))).not.toThrow()
    expect(f.changed).toHaveBeenLastCalledWith({ status: 'error', error: `${name} rejected` })
    expect(f.connection.stop).toHaveBeenCalledOnce()
  })

  it.each(['connection', 'display', 'inputs'] as const)('detects %s socket closures even without vendor callbacks', name => {
    const f = fixture()
    f.handshake()
    f.surface()
    f.inputReady()
    f[name].socket.close()
    expect(f.changed.mock.lastCall?.[0].status).toBe('error')
    expect(f.connection.stop).toHaveBeenCalledOnce()
  })

  it('times out if the main channel succeeds but no desktop arrives', () => {
    const f = fixture()
    f.handshake()
    f.inputReady()
    vi.advanceTimersByTime(30000)
    expect(f.changed).toHaveBeenLastCalledWith({ status: 'error', error: 'SPICE desktop connection timed out.' })
    expect(f.connection.stop).toHaveBeenCalledOnce()
  })

  it('catches protocol decoding errors instead of leaving a black connected desktop', () => {
    const f = fixture()
    const other = {
      ...f.connection,
      ws: new Socket() as unknown as WebSocket,
      process_channel_message: (_message: { type: number; data: ArrayBuffer }) => { throw new Error('invalid channel message') },
      stop: vi.fn(),
    }
    const changed = vi.fn()
    getSpiceConnectionMonitor(other).subscribe(changed)
    expect(() => other.process_channel_message({ type: 103, data: new ArrayBuffer(0) })).not.toThrow()
    expect(changed).toHaveBeenLastCalledWith({ status: 'error', error: 'invalid channel message' })
    f.monitor.stop()
  })

  it('retains failures while the component is unmounted and its connection is cached', () => {
    const f = fixture()
    f.handshake()
    f.surface()
    f.inputReady()
    f.unsubscribe()
    f.display.socket.close()
    const restored = vi.fn()
    expect(getSpiceConnectionMonitor(f.connection)).toBe(f.monitor)
    f.monitor.subscribe(restored)
    expect(restored.mock.lastCall?.[0].status).toBe('error')
    expect(f.changed).toHaveBeenCalledTimes(2)
  })

  it('stops deliberately without turning socket cleanup into a connection error', () => {
    const f = fixture()
    f.handshake()
    f.monitor.stop()
    f.monitor.stop()
    vi.advanceTimersByTime(60000)
    expect(f.changed).toHaveBeenLastCalledWith({ status: 'disconnected', error: '' })
    expect(f.connection.stop).toHaveBeenCalledOnce()
  })
})
