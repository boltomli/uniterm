// Ctrl+Backspace is ^H in xterm's legacy keyboard encoding. Unix shells
// commonly treat that as one-character erase, while ConPTY already gives it
// native word-delete behavior. Override only an active Unix-style shell path.
export interface WordDeleteContext {
  connectionType?: string
  connected: boolean
  alternateScreen: boolean
  passwordPrompt: boolean
  remoteOS?: string
  hostPlatform?: string
}

type WordDeleteEvent = Pick<KeyboardEvent,
  'type' | 'key' | 'ctrlKey' | 'altKey' | 'shiftKey' | 'metaKey' | 'isComposing' | 'preventDefault'>

export function handleTerminalWordDelete(
  event: WordDeleteEvent,
  context: WordDeleteContext,
  input: (data: string) => void,
): boolean {
  if (event.type !== 'keydown' || event.key !== 'Backspace' || !event.ctrlKey ||
      event.altKey || event.shiftKey || event.metaKey || event.isComposing) return false
  if (!context.connected || context.alternateScreen || context.passwordPrompt) return false
  const type = context.connectionType
  if (type === 'local') {
    // This is the app host OS, not the browser OS (server mode can differ).
    if (!['linux', 'darwin', 'android'].includes(context.hostPlatform ?? '')) return false
  } else if (type === 'ssh') {
    if (context.remoteOS?.toLowerCase().includes('windows')) return false
  } else if (type !== 'mosh' && type !== 'wsl') return false

  event.preventDefault()
  // Go through onData, local input tracking and broadcast. Do not write to
  // the transport directly or emulate remote editing with repeated Backspace.
  input('\x17')
  return true
}
