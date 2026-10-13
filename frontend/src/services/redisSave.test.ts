import { describe, it, expect } from 'vitest'
import {
  diffHashFields,
  diffMembers,
  diffScoredMembers,
  isValueTruncated,
  REDIS_VALUE_READ_CAP,
} from './redisSave'
import type { FieldEntry, ScoredMember } from '../types/redis'

// The old save flow diffed with Array.prototype.find inside filter (O(n²))
// and issued one IPC call per element. These helpers produce the add/remove
// sets for one batched backend call, using Map/Set lookups (O(n)).
describe('diffHashFields', () => {
  it('computes changed/new fields and removed fields', () => {
    const current: FieldEntry[] = [
      { field: 'a', value: '1' },
      { field: 'b', value: '2' },
      { field: 'c', value: '3' },
    ]
    const edited: FieldEntry[] = [
      { field: 'a', value: '1' },    // unchanged → not re-sent
      { field: 'b', value: '22' },   // changed → toSet
      { field: 'd', value: '4' },    // new → toSet
      { field: '', value: 'skip' },  // empty field → ignored
    ]
    const diff = diffHashFields(current, edited)
    expect(diff.toDelete).toEqual(['c'])
    expect(diff.toSet).toEqual([
      { field: 'b', value: '22' },
      { field: 'd', value: '4' },
    ])
  })

  it('deletes every current field when the editor is emptied', () => {
    const current: FieldEntry[] = [
      { field: 'a', value: '1' },
      { field: 'b', value: '2' },
    ]
    const diff = diffHashFields(current, [{ field: '', value: '' }])
    expect(diff.toDelete).toEqual(['a', 'b'])
    expect(diff.toSet).toEqual([])
  })

  it('dedups duplicate edited fields (last wins, matching HSET)', () => {
    const edited: FieldEntry[] = [
      { field: 'x', value: '1' },
      { field: 'x', value: '2' },
    ]
    const diff = diffHashFields([], edited)
    expect(diff.toSet).toEqual([{ field: 'x', value: '2' }])
  })

  it('stays fast on large collections (no O(n²) find)', () => {
    const current: FieldEntry[] = Array.from({ length: 20000 }, (_, i) => ({ field: `f${i}`, value: 'v' }))
    const edited: FieldEntry[] = current.map(e => (e.field === 'f19999' ? { field: e.field, value: 'changed' } : e))
    const diff = diffHashFields(current, edited)
    expect(diff.toSet).toEqual([{ field: 'f19999', value: 'changed' }])
    expect(diff.toDelete).toEqual([])
  })
})

describe('diffMembers', () => {
  it('splits added and removed members', () => {
    const diff = diffMembers(['a', 'b', 'c'], ['b', 'c', 'd'])
    expect(diff.toRemove).toEqual(['a'])
    expect(diff.toAdd).toEqual(['d'])
  })

  it('dedups duplicate edited members', () => {
    const diff = diffMembers([], ['x', 'x', 'y'])
    expect(diff.toAdd).toEqual(['x', 'y'])
  })

  it('empties the set when the editor is cleared', () => {
    const diff = diffMembers(['a', 'b'], [])
    expect(diff.toRemove).toEqual(['a', 'b'])
    expect(diff.toAdd).toEqual([])
  })
})

describe('diffScoredMembers', () => {
  it('detects member removal, addition and score changes', () => {
    const current: ScoredMember[] = [
      { member: 'a', score: 1 },
      { member: 'b', score: 2 },
    ]
    const edited: ScoredMember[] = [
      { member: 'a', score: 10 }, // score changed → re-add with new score
      { member: 'c', score: 3 },  // new
    ]
    const diff = diffScoredMembers(current, edited)
    expect(diff.toRemove).toEqual(['b'])
    expect(diff.toAdd).toEqual([
      { member: 'a', score: 10 },
      { member: 'c', score: 3 },
    ])
  })

  it('does not resend untouched members', () => {
    const current: ScoredMember[] = [{ member: 'a', score: 1.5 }]
    const diff = diffScoredMembers(current, [{ member: 'a', score: 1.5 }])
    expect(diff.toAdd).toEqual([])
    expect(diff.toRemove).toEqual([])
  })
})

describe('isValueTruncated', () => {
  it('detects a capped load', () => {
    expect(REDIS_VALUE_READ_CAP).toBeGreaterThan(0)
    expect(isValueTruncated(REDIS_VALUE_READ_CAP, REDIS_VALUE_READ_CAP + 1)).toBe(true)
  })

  it('treats a complete load as editable', () => {
    expect(isValueTruncated(3, 3)).toBe(false)
    expect(isValueTruncated(0, 0)).toBe(false)
  })

  it('treats an unknown total as complete (legacy backend)', () => {
    expect(isValueTruncated(1000, undefined)).toBe(false)
    expect(isValueTruncated(1000, null)).toBe(false)
  })
})
