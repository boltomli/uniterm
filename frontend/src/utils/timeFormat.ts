// Locale-aware clock/duration formatters shared by the AI message list and
// the live thinking box.

/** Clock time of an epoch-ms timestamp, e.g. "14:32" (24h, locale-aware). */
export function formatClock(ts?: number): string {
  if (!ts) return ''
  try {
    return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  } catch {
    return ''
  }
}

/** Short duration like "42s" or "3m 12s". Returns '' for empty/zero input. */
export function formatDuration(ms?: number): string {
  if (!ms || ms < 100) return ''
  const totalSeconds = Math.max(1, Math.round(ms / 1000))
  if (totalSeconds < 60) return `${totalSeconds}s`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`
}
