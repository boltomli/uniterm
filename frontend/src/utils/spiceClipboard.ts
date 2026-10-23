type AgentMessage = { type: number; data: ArrayBuffer }
type AgentConnection = {
  agent_connected?: boolean
  process_channel_message(message: AgentMessage): boolean
  announce_agent_capabilities(request: number): void
  send_agent_message(type: number, payload: { buffer_size(): number; to_buffer(buffer: ArrayBuffer, offset?: number): void }): void
}
type ClipboardCallbacks = {
  canReceive(): boolean
  onText(text: string): void | Promise<void>
  onReady(): void
  onError(error: unknown): void
}

const CAP_CLIPBOARD = 1 << 3
const CAP_DEMAND = 1 << 5
const MAX_MESSAGE = 16 * 1024 * 1024

function words(...values: number[]): Uint8Array {
  const bytes = new Uint8Array(values.length * 4)
  const view = new DataView(bytes.buffer)
  values.forEach((value, index) => view.setUint32(index * 4, value, true))
  return bytes
}

export function createSpiceClipboard(connection: AgentConnection, callbacks: ClipboardCallbacks) {
  const originalProcess = connection.process_channel_message
  const originalAnnounce = connection.announce_agent_capabilities
  let capabilities = 0
  let localText: string | null = null
  let remoteOwned = false
  let pendingText = false
  let disposed = false
  let buffer: Uint8Array = new Uint8Array(2048)
  let used = 0
  const ready = () => !disposed && !!connection.agent_connected && !!(capabilities & (CAP_CLIPBOARD | CAP_DEMAND)) && !pendingText
  function send(type: number, bytes: Uint8Array) {
    connection.send_agent_message(type, {
      buffer_size: () => bytes.length,
      to_buffer: (target, offset = 0) => new Uint8Array(target, offset, bytes.length).set(bytes),
    })
  }
  function sendText(text: string | null) {
    const value = capabilities & (1 << 9) ? text?.replace(/\r\n|\r|\n/g, '\r\n') : text
    const utf8 = new TextEncoder().encode(value ?? '')
    if (utf8.length > MAX_MESSAGE - 4) throw new Error('SPICE clipboard text is too large')
    const bytes = new Uint8Array(4 + utf8.length)
    bytes.set(words(text === null ? 0 : 1))
    bytes.set(utf8, 4)
    send(4, bytes)
  }
  function announce(request: number) {
    send(6, words(request, 7 | CAP_CLIPBOARD | CAP_DEMAND))
  }
  function receive(type: number, payload: Uint8Array): boolean {
    const view = new DataView(payload.buffer, payload.byteOffset, payload.length)
    if (type === 6) {
      if (payload.length < 8) throw new Error('Invalid SPICE agent capabilities')
      capabilities = view.getUint32(4, true)
      return false
    }
    if ([4, 7, 8, 9].includes(type) && !connection.agent_connected) return true
    if (type === 7) {
      if (payload.length % 4) throw new Error('Invalid SPICE clipboard formats')
      localText = null
      remoteOwned = true
      pendingText = false
      if (callbacks.canReceive()) {
        for (let i = 0; i < payload.length; i += 4) {
          if (view.getUint32(i, true) === 1) {
            pendingText = true
            send(8, words(1))
            break
          }
        }
      }
      return true
    }
    if (type === 8) {
      if (payload.length !== 4) throw new Error('Invalid SPICE clipboard request')
      sendText(view.getUint32(0, true) === 1 ? localText : null)
      return true
    }
    if (type === 4) {
      if (payload.length < 4) throw new Error('Invalid SPICE clipboard data')
      const accepted = pendingText || callbacks.canReceive()
      pendingText = false
      if (view.getUint32(0, true) === 1 && accepted) {
        const text = new TextDecoder('utf-8', { fatal: true }).decode(payload.subarray(4))
        localText = null
        remoteOwned = true
        Promise.resolve(callbacks.onText(text)).catch(callbacks.onError)
      }
      return true
    }
    if (type === 9) {
      pendingText = false
      if (remoteOwned) {
        remoteOwned = false
        if (callbacks.canReceive()) Promise.resolve(callbacks.onText('')).catch(callbacks.onError)
      }
      return true
    }
    return false
  }
  function process(message: AgentMessage): boolean {
    if (message.type === 107 || message.type === 108 || message.type === 115) {
      capabilities = 0
      localText = null
      remoteOwned = pendingText = false
      used = 0
    }
    if (message.type !== 109) return originalProcess.call(connection, message)
    try {
      const incoming = new Uint8Array(message.data)
      if (used + incoming.length > MAX_MESSAGE + 20) throw new Error('SPICE agent message is too large')
      if (used + incoming.length > buffer.length) {
        const expanded = new Uint8Array(Math.min(MAX_MESSAGE + 20, Math.max(used + incoming.length, buffer.length * 2)))
        expanded.set(buffer.subarray(0, used))
        buffer = expanded
      }
      buffer.set(incoming, used)
      used += incoming.length
      while (used >= 20) {
        const header = new DataView(buffer.buffer)
        const size = header.getUint32(16, true)
        if (header.getUint32(0, true) !== 1 || size > MAX_MESSAGE) throw new Error('Invalid SPICE agent message')
        if (used < 20 + size) break
        const type = header.getUint32(4, true)
        const complete = buffer.slice(0, 20 + size)
        buffer.copyWithin(0, 20 + size, used)
        used -= 20 + size
        const wasReady = ready()
        if (!receive(type, complete.subarray(20))) originalProcess.call(connection, { type: 109, data: complete.buffer })
        if (type === 6 && !wasReady && ready()) callbacks.onReady()
      }
    } catch (error) {
      used = 0
      pendingText = false
      callbacks.onError(error)
    }
    return true
  }
  connection.process_channel_message = process
  connection.announce_agent_capabilities = announce
  if (connection.agent_connected) announce(1)
  return {
    get ready() { return ready() },
    publish(text: string): boolean {
      if (!ready()) return false
      localText = text
      remoteOwned = false
      if (capabilities & CAP_DEMAND) send(text ? 7 : 9, text ? words(1) : new Uint8Array(0))
      else sendText(text)
      return true
    },
    dispose() {
      if (!disposed && localText !== null && connection.agent_connected && (capabilities & CAP_DEMAND)) send(9, new Uint8Array(0))
      disposed = true
      if (connection.process_channel_message === process) connection.process_channel_message = originalProcess
      if (connection.announce_agent_capabilities === announce) connection.announce_agent_capabilities = originalAnnounce
    },
  }
}
