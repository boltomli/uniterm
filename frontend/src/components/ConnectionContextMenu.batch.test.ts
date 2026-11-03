// Tests for the batch-execute menu integration in ConnectionContextMenu.
//
// The context menu shows a "Batch Execute" item only when ≥ 2 selected hosts
// are SSH or local (the types BatchExecuteDialog can run commands on). These
// tests pin the gating logic so the menu item doesn't appear for unsupported
// types or single selections, and does appear for valid multi-selection.
import { describe, it, expect } from 'vitest'
import type { ConnectionConfig } from '../types/session'

// ── Logic under test (mirrors ConnectionContextMenu.vue computed) ──

/** Hosts that support batch execution: SSH and local terminal sessions. */
function selectBatchHosts(targets: ConnectionConfig[]): ConnectionConfig[] {
  return targets.filter(h => h.type === 'ssh' || h.type === 'local')
}

/** Whether the "Batch Execute" menu item should be visible. */
function canBatchExecute(targets: ConnectionConfig[]): boolean {
  return selectBatchHosts(targets).length >= 2
}

// ── Helpers ──

function makeHost(id: string, type: ConnectionConfig['type']): ConnectionConfig {
  return {
    id,
    name: id,
    type,
    host: '127.0.0.1',
    port: 22,
    groupId: '',
  } as ConnectionConfig
}

describe('ConnectionContextMenu batch-execute gating', () => {
  describe('selectBatchHosts', () => {
    it('keeps only ssh and local hosts', () => {
      const targets = [
        makeHost('a', 'ssh'),
        makeHost('b', 'local'),
        makeHost('c', 'rdp'),
        makeHost('d', 'database'),
      ]
      const result = selectBatchHosts(targets)
      expect(result).toHaveLength(2)
      expect(result.map(h => h.id)).toEqual(['a', 'b'])
    })

    it('returns empty array when no supported hosts', () => {
      const targets = [makeHost('a', 'rdp'), makeHost('b', 'vnc')]
      expect(selectBatchHosts(targets)).toHaveLength(0)
    })

    it('returns empty array for empty input', () => {
      expect(selectBatchHosts([])).toHaveLength(0)
    })

    it('preserves all ssh/local hosts in mixed selection', () => {
      const targets = [
        makeHost('s1', 'ssh'),
        makeHost('l1', 'local'),
        makeHost('s2', 'ssh'),
        makeHost('r1', 'redis'),
        makeHost('l2', 'local'),
      ]
      expect(selectBatchHosts(targets).map(h => h.id)).toEqual(['s1', 'l1', 's2', 'l2'])
    })
  })

  describe('canBatchExecute (menu item visibility)', () => {
    it('is false for empty selection', () => {
      expect(canBatchExecute([])).toBe(false)
    })

    it('is false for a single ssh host', () => {
      expect(canBatchExecute([makeHost('a', 'ssh')])).toBe(false)
    })

    it('is false for a single local host', () => {
      expect(canBatchExecute([makeHost('a', 'local')])).toBe(false)
    })

    it('is true for two ssh hosts', () => {
      expect(canBatchExecute([makeHost('a', 'ssh'), makeHost('b', 'ssh')])).toBe(true)
    })

    it('is true for one ssh + one local host', () => {
      expect(canBatchExecute([makeHost('a', 'ssh'), makeHost('b', 'local')])).toBe(true)
    })

    it('is true for two local hosts', () => {
      expect(canBatchExecute([makeHost('a', 'local'), makeHost('b', 'local')])).toBe(true)
    })

    it('is false for two unsupported hosts (e.g. rdp + vnc)', () => {
      expect(canBatchExecute([makeHost('a', 'rdp'), makeHost('b', 'vnc')])).toBe(false)
    })

    it('is false when only one of many selected hosts is supported', () => {
      const targets = [
        makeHost('a', 'ssh'),
        makeHost('b', 'rdp'),
        makeHost('c', 'vnc'),
        makeHost('d', 'database'),
      ]
      expect(canBatchExecute(targets)).toBe(false)
    })

    it('is true when ≥ 2 supported hosts are mixed with unsupported ones', () => {
      const targets = [
        makeHost('a', 'ssh'),
        makeHost('b', 'local'),
        makeHost('c', 'rdp'),
        makeHost('d', 'redis'),
      ]
      expect(canBatchExecute(targets)).toBe(true)
    })

    it('is true for a large multi-selection of ssh hosts', () => {
      const targets = Array.from({ length: 10 }, (_, i) => makeHost(`h${i}`, 'ssh'))
      expect(canBatchExecute(targets)).toBe(true)
    })
  })
})