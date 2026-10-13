// Regression tests for security.md finding 20 (Medium): three components each
// carried a private copy of formatBytes and the copies disagreed:
//   - MonitorTabContent.vue / ZmodemTransfer.vue: sizes = ['B'..'GB'], no
//     clamp → 1 TiB or more computed sizes[-1..] and rendered
//     "undefined undefined"; a rate below 1 B/s produced sizes[-1] too
//     (Math.log(0.4) is negative).
//   - MonitorOverviewSidebar.vue clamped the upper end but stopped at TB and
//     still let sub-1-byte rates hit the -1 index.
// The repo has no component-mount tests (node env, no jsdom), so the SFC
// wiring is asserted on the source (pattern: BaseTerminal.cleanup.test.ts);
// the shared helper itself is covered behaviorally below.
import { describe, it, expect } from 'vitest'
import { scriptBody, readSrc } from '../testUtils/sfc'
import { formatBytes } from './format'

describe('shared formatBytes wiring', () => {
  it('MonitorTabContent imports the shared formatBytes and declares no local copy', () => {
    const script = scriptBody(readSrc('components/MonitorTabContent.vue'))
    expect(script).toMatch(/import\s*\{\s*formatBytes\s*\}\s*from\s*'\.\.\/utils\/format'/)
    expect(script).not.toMatch(/function\s+formatBytes\s*\(/)
  })

  it('ZmodemTransfer imports the shared formatBytes and declares no local copy', () => {
    const script = scriptBody(readSrc('components/ZmodemTransfer.vue'))
    expect(script).toMatch(/import\s*\{\s*formatBytes\s*\}\s*from\s*'\.\.\/utils\/format'/)
    expect(script).not.toMatch(/function\s+formatBytes\s*\(/)
  })

  it('MonitorOverviewSidebar imports the shared formatBytes and declares no local copy', () => {
    const script = scriptBody(readSrc('components/MonitorOverviewSidebar.vue'))
    expect(script).toMatch(/import\s*\{\s*formatBytes\s*\}\s*from\s*'\.\.\/utils\/format'/)
    expect(script).not.toMatch(/function\s+formatBytes\s*\(/)
  })
})

describe('formatBytes', () => {
  it('keeps the established display format for the common ranges', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5 MB')
  })

  it('extends the unit table past GB/TB so large values never render "undefined units"', () => {
    expect(formatBytes(1024 ** 4)).toBe('1 TB')
    expect(formatBytes(2 * 1024 ** 4)).toBe('2 TB')
    expect(formatBytes(1024 ** 5)).toBe('1 PB')
    expect(formatBytes(1024 ** 6)).toBe('1 EB')
    // Beyond the table the index must clamp, not fall off the end.
    expect(formatBytes(1024 ** 7)).not.toContain('undefined')
  })

  it('clamps sub-1-byte rates (the old sizes[-1] path) to 0 B', () => {
    expect(formatBytes(0.4)).toBe('0 B')
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-128)).toBe('0 B')
  })

  it('returns 0 B for non-finite input', () => {
    expect(formatBytes(Number.NaN)).toBe('0 B')
    expect(formatBytes(Number.POSITIVE_INFINITY)).toBe('0 B')
    expect(formatBytes(Number.NEGATIVE_INFINITY)).toBe('0 B')
  })

  it('never emits "undefined" in its output', () => {
    for (const n of [0.4, 0, -1, Number.NaN, 1024 ** 4, 1024 ** 5, 1024 ** 7, 3.7 * 1024 ** 6]) {
      expect(formatBytes(n)).not.toContain('undefined')
    }
  })
})
