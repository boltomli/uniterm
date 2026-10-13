// Shared byte-size formatter (security.md finding 20).
//
// MonitorTabContent.vue, ZmodemTransfer.vue and MonitorOverviewSidebar.vue
// each kept a private copy that disagreed at the edges: the first two sized
// their unit table to 'GB' with no clamp, so ≥1 TiB values — and any rate
// below 1 B/s, where Math.log is negative — computed an out-of-range index
// and rendered "undefined units"; the sidebar clamped the top but stopped at
// TB and still let sub-1-byte input hit sizes[-1].
//
// Keep the display format identical to the old common-case output:
// parseFloat((bytes / 1024 ** i).toFixed(1)) + ' ' + unit — e.g. 2048 →
// '2 KB', 512 → '512 B'.
/** Unit table, at least through EB so petabyte/exascale rates stay in range. */
const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB'] as const

/**
 * Format a byte count (or per-second rate) as a human-readable string.
 *
 * Non-finite input and anything below 1 byte — including 0, negatives and
 * sub-1 B/s rates — renders `'0 B'` rather than indexing below the table;
 * values beyond the largest unit clamp to that unit instead of rendering an
 * undefined one.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 1) return '0 B'
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), UNITS.length - 1)
  return parseFloat((bytes / Math.pow(1024, i)).toFixed(1)) + ' ' + UNITS[i]
}
