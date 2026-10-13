// Regression tests for the FileList perf findings (security.md component
// chapter #6 and #16):
//   - selection membership was `selectedItems.some(...)` per row, called
//     twice per rendered row (O(rows × selection));
//   - the cut-clipboard check was `cutItemNames.includes(...)` per row;
//   - the refresh watcher re-pointed the selection with `find` per selected
//     item (O(n·m) on a 20k-entry directory);
//   - the whole listing was re-sorted on every filter keystroke (sort ran
//     before the filter) and the modTime comparator built two `Date` objects
//     per comparison.
// The helpers under test are what FileList.vue now delegates to.

import { describe, it, expect } from 'vitest'
import {
  namesOf,
  remapSelection,
  buildModTimeIndex,
  buildFileComparator,
  filterEntries,
  type SortableRow,
} from './fileList'
import { scriptBody, readSrc } from '../testUtils/sfc'

interface Row extends SortableRow {
  isHidden: boolean
}

function row(name: string, over: Partial<Row> = {}): Row {
  return { name, size: 0, modTime: '', isDir: false, isHidden: false, ...over }
}

const collator = new Intl.Collator()

describe('namesOf (selection membership)', () => {
  it('answers membership questions for a 20k-selection in constant time', () => {
    const selection: Row[] = []
    for (let i = 0; i < 20000; i += 1) selection.push(row(`file-${i}`))
    const names = namesOf(selection)
    expect(names.size).toBe(20000)
    expect(names.has('file-19999')).toBe(true)
    expect(names.has('file-20000')).toBe(false)
  })

  it('reads each name exactly once while building the set', () => {
    let reads = 0
    const selection: Row[] = []
    for (let i = 0; i < 500; i += 1) {
      selection.push({
        size: 0, modTime: '', isDir: false, isHidden: false,
        get name() { reads += 1; return `f${i}` },
      })
    }
    namesOf(selection)
    expect(reads).toBe(500)
  })
})

describe('remapSelection (refresh watcher)', () => {
  it('re-points the selection at the live row objects', () => {
    const selected = [row('a'), row('b')]
    const fresh = [row('a', { size: 42 }), row('c')]
    const next = remapSelection(selected, fresh)
    expect(next).toHaveLength(1)
    expect(next[0]).toBe(fresh[0])
    expect(next[0].size).toBe(42)
  })

  it('drops entries that no longer exist and keeps selection order', () => {
    const fresh = [row('a'), row('b'), row('c')]
    const next = remapSelection([row('c'), row('gone'), row('a')], fresh)
    expect(next.map((r) => r.name)).toEqual(['c', 'a'])
  })

  it('stays O(n + m): one name read per item, not a scan per selected item', () => {
    let reads = 0
    const counting = (name: string): Row => ({
      size: 0, modTime: '', isDir: false, isHidden: false,
      get name() { reads += 1; return name },
    })
    const fresh: Row[] = []
    for (let i = 0; i < 2000; i += 1) fresh.push(counting(`f${i}`))
    const selected: Row[] = []
    for (let i = 0; i < 2000; i += 2) selected.push(counting(`f${i}`))

    const next = remapSelection(selected, fresh)
    expect(next).toHaveLength(1000)
    // A `find` per selected item reads ~fresh/2 names per item on average;
    // the Map build reads every name once.
    expect(reads).toBeLessThanOrEqual((fresh.length + selected.length) * 2)
  })
})

describe('modTime comparator (precomputed timestamps)', () => {
  function countingRows(n: number): { rows: Row[]; reads: () => number } {
    let count = 0
    const rows: Row[] = []
    for (let i = 0; i < n; i += 1) {
      const ts = new Date(1700000000000 - i * 86_400_000).toISOString()
      rows.push({
        name: `f${String(i).padStart(4, '0')}`, size: 0, isDir: false, isHidden: false,
        get modTime() { count += 1; return ts },
      })
    }
    return { rows, reads: () => count }
  }

  it('parses each modTime once, not once per comparison', () => {
    const { rows, reads } = countingRows(64)
    const modTimes = buildModTimeIndex(rows)
    expect(reads()).toBe(rows.length)

    const cmp = buildFileComparator<Row>({
      sort: { prop: 'modTime', order: 'ascending' },
      collator,
      modTimes,
      typeLabel: () => '',
    })
    const sorted = [...rows].sort(cmp)
    expect(reads()).toBe(rows.length)
    // Ascending order puts the smallest timestamp first: f0063 was stamped
    // furthest in the past.
    expect(sorted[0].name).toBe('f0063')
    expect(sorted[sorted.length - 1].name).toBe('f0000')
  })

  it('honours descending order', () => {
    const rows = [
      row('a', { modTime: '2024-01-01T00:00:00Z' }),
      row('b', { modTime: '2024-06-01T00:00:00Z' }),
    ]
    const modTimes = buildModTimeIndex(rows)
    const cmp = buildFileComparator<Row>({
      sort: { prop: 'modTime', order: 'descending' },
      collator,
      modTimes,
      typeLabel: () => '',
    })
    expect([...rows].sort(cmp).map((r) => r.name)).toEqual(['b', 'a'])
  })

  it('treats a missing modTime as epoch 0', () => {
    const rows = [row('stamped', { modTime: '2024-01-01T00:00:00Z' }), row('unstamped')]
    const modTimes = buildModTimeIndex(rows)
    expect(modTimes.get(rows[1])).toBe(0)
  })
})

describe('sort → filter pipeline', () => {
  function sortRows(rows: Row[], sort: { prop: 'name' | 'type' | 'modTime' | 'size'; order: 'ascending' | 'descending' } | null): Row[] {
    const cmp = buildFileComparator<Row>({
      sort,
      collator,
      modTimes: buildModTimeIndex(rows),
      typeLabel: (r) => (r.isDir ? 'dir' : r.name.includes('.') ? r.name.split('.').pop()! : '-'),
    })
    return [...rows].sort(cmp)
  }

  it('keeps ".." pinned first in both directions', () => {
    const rows = [row('b'), row('..', { isDir: true }), row('a')]
    expect(sortRows(rows, { prop: 'name', order: 'ascending' })[0].name).toBe('..')
    expect(sortRows(rows, { prop: 'name', order: 'descending' })[0].name).toBe('..')
  })

  it('defaults to directories first, then name', () => {
    const rows = [row('z.txt'), row('adir', { isDir: true }), row('a.txt'), row('bdir', { isDir: true })]
    expect(sortRows(rows, null).map((r) => r.name)).toEqual(['adir', 'bdir', 'a.txt', 'z.txt'])
  })

  it('sizes put directories first too', () => {
    const rows = [row('file', { size: 10 }), row('dir', { isDir: true, size: 9999 })]
    const cmp = buildFileComparator<Row>({
      sort: { prop: 'size', order: 'ascending' },
      collator,
      modTimes: buildModTimeIndex(rows),
      typeLabel: () => '',
    })
    expect([...rows].sort(cmp).map((r) => r.name)).toEqual(['dir', 'file'])
  })

  it('filtering a sorted list never reorders it (filter runs on top of sort)', () => {
    const sorted = sortRows([row('cc'), row('aa'), row('ab'), row('dd')], { prop: 'name', order: 'ascending' })
    const filtered = filterEntries(sorted, { showHidden: true, query: 'a' })
    expect(filtered.map((r) => r.name)).toEqual(['aa', 'ab'])
  })

  it('hides dotfiles but never the ".." row', () => {
    const rows = [row('..', { isDir: true }), row('.secret'), row('visible', { isHidden: true }), row('plain')]
    const shown = filterEntries(rows, { showHidden: false, query: '' })
    expect(shown.map((r) => r.name)).toEqual(['..', 'plain'])
    expect(filterEntries(rows, { showHidden: true, query: '' })).toHaveLength(4)
  })

  it('matches the query case-insensitively against the name', () => {
    const rows = [row('Report.PDF'), row('notes.md')]
    expect(filterEntries(rows, { showHidden: true, query: '  rep ' }).map((r) => r.name)).toEqual(['Report.PDF'])
  })
})

describe('FileList.vue wiring', () => {
  const script = scriptBody(readSrc('components/FileList.vue'))

  it('answers row selection from the computed name Set', () => {
    expect(script).toMatch(/const selectedNames = computed\(\(\) => namesOf\(selectedItems\.value\)\)/)
    expect(script).toMatch(/function isSelected\(row: FileItem\): boolean \{\s*return selectedNames\.value\.has\(row\.name\)/)
    // The cut-clipboard row class uses a Set too, not `includes` per row.
    expect(script).toMatch(/cutNames\.value\.has\(row\.name\)/)
    expect(script).not.toMatch(/cutItemNames\.includes/)
  })

  it('re-selects through the Map-based helper on refresh', () => {
    expect(script).toMatch(/remapSelection\(selectedItems\.value, fresh\)/)
    expect(script).not.toMatch(/fresh\.find\(f => f\.name === sel\.name\)/)
  })

  it('filters on top of a sorted computed and sorts with precomputed timestamps', () => {
    expect(script).toMatch(/const filteredFiles = computed\(\(\) =>\s*filterEntries\(sortedFiles\.value/)
    expect(script).toMatch(/buildModTimeIndex\(list\)/)
    // The old per-comparison Date construction is gone.
    expect(script).not.toMatch(/new Date\(a\.modTime\)/)
  })
})
