// Pure save/diff helpers for the Redis value editors. Kept free of Vue and
// of the wails bindings so they can be unit-tested directly. All lookups use
// Map/Set (O(n)); the previous inline diffs used Array.find inside filter
// (O(n²)) and issued one IPC call per element.
import type { FieldEntry, ScoredMember } from '../types/redis'

// REDIS_VALUE_READ_CAP mirrors the backend's defaultRedisReadLimit: value
// editors load at most this many elements and mark the key read-only when
// the server holds more (see isValueTruncated).
export const REDIS_VALUE_READ_CAP = 1000

export interface HashDiff {
  /** Fields to write with one batched HSET (changed or new values only). */
  toSet: FieldEntry[]
  /** Current fields missing from the editor → one batched HDEL. */
  toDelete: string[]
}

export interface MemberDiff {
  toAdd: string[]
  toRemove: string[]
}

export interface ScoredMemberDiff {
  /** Members to write with one batched ZADD (new or changed scores). */
  toAdd: ScoredMember[]
  toRemove: string[]
}

export function diffHashFields(current: FieldEntry[], edited: FieldEntry[]): HashDiff {
  // Last duplicate wins, matching HSET semantics for repeated fields.
  const editedMap = new Map<string, string>()
  for (const e of edited) {
    if (e.field) editedMap.set(e.field, e.value)
  }
  const currentMap = new Map(current.map(c => [c.field, c.value]))
  const toSet: FieldEntry[] = []
  for (const [field, value] of editedMap) {
    if (currentMap.get(field) !== value) toSet.push({ field, value })
  }
  const toDelete = current.filter(c => !editedMap.has(c.field)).map(c => c.field)
  return { toSet, toDelete }
}

export function diffMembers(current: string[], edited: string[]): MemberDiff {
  const currentSet = new Set(current)
  const editedSet = new Set(edited)
  return {
    toRemove: current.filter(m => !editedSet.has(m)),
    toAdd: [...editedSet].filter(m => !currentSet.has(m)),
  }
}

export function diffScoredMembers(current: ScoredMember[], edited: ScoredMember[]): ScoredMemberDiff {
  const currentMap = new Map(current.map(c => [c.member, c.score]))
  const editedMap = new Map(edited.map(e => [e.member, e.score]))
  const toAdd: ScoredMember[] = []
  for (const [member, score] of editedMap) {
    if (currentMap.get(member) !== score) toAdd.push({ member, score })
  }
  const toRemove = [...currentMap.keys()].filter(m => !editedMap.has(m))
  return { toAdd, toRemove }
}

/**
 * True when the loaded entries are only a prefix of the server-side value
 * (loaded == REDIS_VALUE_READ_CAP and the key reports more elements). A
 * truncated value must stay read-only: saving a prefix would silently drop
 * everything beyond the cap.
 */
export function isValueTruncated(loaded: number, total: number | undefined | null): boolean {
  return typeof total === 'number' && total > loaded
}
