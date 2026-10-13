// Pure helpers behind FileList.vue's row pipeline (security.md component
// chapter #6 / #16). Kept free of Vue so the selection membership, the
// re-selection after a refresh and the sort → filter ordering are unit
// testable without a component harness.

export type SortProp = 'name' | 'type' | 'modTime' | 'size'
export type SortOrder = 'ascending' | 'descending'
export interface FileSortState {
  prop: SortProp
  order: SortOrder
}

export interface SortableRow {
  name: string
  size: number
  modTime: string
  isDir: boolean
}

/**
 * Membership set for row-level checks (selection, cut clipboard). A linear
 * scan of the selection per rendered row is O(rows × selection) — Ctrl+A in a
 * 20k-entry directory meant tens of millions of comparisons per re-render.
 */
export function namesOf(items: readonly { name: string }[]): Set<string> {
  const names = new Set<string>()
  for (const item of items) names.add(item.name)
  return names
}

/**
 * Re-point a selection at the live row objects of a refreshed listing in one
 * pass (Map by name). The previous `find` per selected item was O(n·m): a
 * full-selection refresh of a 20k directory did ~200 million comparisons.
 * Entries that disappeared from the listing drop out of the selection; the
 * selection order is preserved (shift-range semantics depend on it).
 */
export function remapSelection<T extends { name: string }>(selection: readonly T[], fresh: readonly T[]): T[] {
  if (!selection.length) return []
  const byName = new Map<string, T>()
  for (const item of fresh) byName.set(item.name, item)

  const next: T[] = []
  for (const sel of selection) {
    const live = byName.get(sel.name)
    if (live) next.push(live)
  }
  return next
}

/**
 * Parse every row's modTime exactly once. Sorting n rows does n log n
 * comparisons, and the old comparator built two `Date` objects per comparison
 * — the parse cost dwarfed the comparison itself.
 */
export function buildModTimeIndex<T extends SortableRow>(rows: readonly T[]): Map<T, number> {
  const index = new Map<T, number>()
  for (const row of rows) {
    const raw = row.modTime
    index.set(row, raw ? Date.parse(raw) : 0)
  }
  return index
}

export interface ComparatorOptions<T extends SortableRow> {
  sort: FileSortState | null
  collator: Intl.Collator
  /** Pre-parsed timestamps from buildModTimeIndex. */
  modTimes: ReadonlyMap<T, number>
  /** Expensive label used by the `type` column sort (i18n-backed in the app). */
  typeLabel: (row: T) => string
}

/**
 * Comparator for the listing. '..' is navigation, not an entry: it stays the
 * first row for every column and in both directions. With no explicit sort
 * state the list falls back to directories-first, then name.
 */
export function buildFileComparator<T extends SortableRow>(opts: ComparatorOptions<T>): (a: T, b: T) => number {
  const { sort, collator, modTimes, typeLabel } = opts
  const byName = (a: T, b: T) => collator.compare(a.name, b.name)
  const byType = (a: T, b: T) => typeLabel(a).toLowerCase().localeCompare(typeLabel(b).toLowerCase())
  const byModTime = (a: T, b: T) => (modTimes.get(a) ?? 0) - (modTimes.get(b) ?? 0)
  const bySize = (a: T, b: T) => {
    if (a.isDir && !b.isDir) return -1
    if (!a.isDir && b.isDir) return 1
    return a.size - b.size
  }
  const byColumn: Record<SortProp, (a: T, b: T) => number> = {
    name: byName,
    type: byType,
    modTime: byModTime,
    size: bySize,
  }

  return (a, b) => {
    if (a.name === '..') return -1
    if (b.name === '..') return 1
    if (sort) {
      const cmp = byColumn[sort.prop](a, b)
      return sort.order === 'descending' ? -cmp : cmp
    }
    if (a.isDir && !b.isDir) return -1
    if (!a.isDir && b.isDir) return 1
    return byName(a, b)
  }
}

/**
 * Name filter applied ON TOP of the sorted listing. Filtering preserves
 * order, so typing in the filter box no longer re-sorts the whole directory —
 * the sorted computed stays cached until the data or the sort state change.
 */
export function filterEntries<T extends { name: string; isHidden: boolean }>(
  rows: readonly T[],
  opts: { showHidden: boolean; query: string },
): T[] {
  let out = rows
  if (!opts.showHidden) {
    out = out.filter((f) => f.name === '..' || (!f.name.startsWith('.') && !f.isHidden))
  }
  const q = opts.query.trim().toLowerCase()
  if (!q) return [...out]
  return out.filter((f) => f.name.toLowerCase().includes(q))
}
