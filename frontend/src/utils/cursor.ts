// Strip DEC private mode 12 (cursor blink) from a CSI ? ... h/l sequence
// when the user has disabled blink. The remote SSH shell sends these to
// re-enable blinking at runtime, overriding our cursorBlink option.
// Also accepts and filters intermediate characters (e.g. \x1b[?12;25h → \x1b[?25h).
export function stripCursorBlink(data: string, enabled: boolean): string {
  if (enabled) return data
  // Cheap guard: the regex below can only match a CSI private-mode sequence.
  // Most output chunks carry no ESC at all, and a full-string replace on
  // every chunk is measurable during a flood.
  if (data.indexOf('\x1b[?') < 0) return data
  return data.replace(/\x1b\[\?(\d+(?:;\d+)*)([hl])/g, (_m, params, suffix) => {
    const list = (params as string).split(';').filter(p => p !== '12')
    if (list.length === 0) return ''
    return `\x1b[?${list.join(';')}${suffix}`
  })
}
