type SpiceMessage = { type: number; data: ArrayBuffer }
type SpiceChannel = {
  state?: string
  ws?: WebSocket
  onerror?: (error: unknown) => void
  process_channel_message(message: SpiceMessage): boolean
}
type SpiceConnection = SpiceChannel & {
  agent_connected?: boolean
  display?: SpiceChannel & {
    primary_surface?: number
    surfaces?: Record<number, { canvas?: { width: number; height: number } }>
  }
  inputs?: SpiceChannel
  stop(): void
}
type ConnectionState = {
  status: 'connecting' | 'connected' | 'disconnected' | 'error'
  error: string
}
type Listener = (state: Readonly<ConnectionState>) => void
type Monitor = { subscribe(listener: Listener): () => void; stop(): void }

const monitors = new WeakMap<SpiceConnection, Monitor>()

export function getSpiceConnectionMonitor(connection: SpiceConnection): Monitor {
  const existing = monitors.get(connection)
  if (existing) return existing
  let state: ConnectionState = { status: 'connecting', error: '' }
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const listeners = new Set<Listener>()
  const watched = new Set<SpiceChannel>()
  const restore: Array<() => void> = []

  function publish() {
    for (const listener of listeners) listener(state)
  }

  function finish(error?: unknown) {
    if (stopped) return
    stopped = true
    clearTimeout(timer)
    state = error === undefined ? { status: 'disconnected', error: '' } : {
      status: 'error',
      error: error instanceof Error ? error.message : typeof error === 'string' ? error : 'SPICE connection failed.',
    }
    for (const channel of watched) channel.state = 'closing'
    connection.agent_connected = false
    for (const detach of restore.reverse()) detach()
    try { connection.stop() } catch (_) {}
    publish()
  }

  function watchChannel(channel: SpiceChannel | undefined, name: string) {
    if (!channel || watched.has(channel) || stopped) return
    watched.add(channel)
    const originalError = channel.onerror
    const onError = (error: unknown) => finish(error ?? new Error(`SPICE ${name} channel failed.`))
    channel.onerror = onError
    restore.push(() => { if (channel.onerror === onError) channel.onerror = originalError })
    const originalProcess = channel.process_channel_message
    function process(message: SpiceMessage) {
      if (stopped) return true
      try {
        const result = originalProcess.call(channel, message)
        check()
        return result
      } catch (error) {
        finish(error)
        return true
      }
    }
    channel.process_channel_message = process
    restore.push(() => { if (channel.process_channel_message === process) channel.process_channel_message = originalProcess })
    const socket = channel.ws
    if (socket) {
      const onClose = (event: CloseEvent) => finish(new Error(`SPICE ${name} channel closed (code ${event.code}).`))
      socket.addEventListener('error', onError)
      socket.addEventListener('close', onClose)
      restore.push(() => {
        socket.removeEventListener('error', onError)
        socket.removeEventListener('close', onClose)
      })
      if (socket.readyState === 2 || socket.readyState === 3) finish(new Error(`SPICE ${name} channel is closed.`))
    }
    if (!stopped && (channel.state === 'error' || channel.state === 'closing')) finish(new Error(`SPICE ${name} channel is unavailable.`))
  }

  function check() {
    if (stopped) return
    watchChannel(connection, 'main')
    watchChannel(connection.display, 'display')
    watchChannel(connection.inputs, 'inputs')
    if (stopped || state.status !== 'connecting') return
    const display = connection.display
    const primary = display?.primary_surface
    const canvas = primary === undefined ? undefined : display?.surfaces?.[primary]?.canvas
    if (connection.state === 'ready' && display?.state === 'ready' && connection.inputs?.state === 'ready' &&
        canvas && canvas.width > 0 && canvas.height > 0) {
      clearTimeout(timer)
      state = { status: 'connected', error: '' }
      publish()
    }
  }

  const monitor: Monitor = {
    subscribe(listener) {
      listeners.add(listener)
      listener(state)
      return () => { listeners.delete(listener) }
    },
    stop: () => finish(),
  }
  monitors.set(connection, monitor)
  timer = setTimeout(() => finish(new Error('SPICE desktop connection timed out.')), 30000)
  check()
  return monitor
}
