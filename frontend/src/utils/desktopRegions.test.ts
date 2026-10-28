import { describe, expect, it } from 'vitest'
import { changeDesktopCoordinate, customDesktopRegion, deleteDesktopRegion, desktopRegionPresets, dragDesktopRegion, normalizeDesktopView, saveDesktopRegion, suggestedDesktopRegion, type RegionDragMode } from './desktopRegions'
import { validDesktopRect, type DesktopView } from './vncDisplays'

const first = { id: 'left', name: 'Left', x: 0, y: 0, width: 1920, height: 1080 }
const second = { id: 'right', name: 'Right', x: 1920, y: 0, width: 2560, height: 1440 }

describe('saved desktop regions', () => {
  it('migrates a legacy crop without changing its size or losing it in the all-screen view', () => {
    const rect = { x: 150, y: 250, width: 1024, height: 768 }
    const migrated = normalizeDesktopView({ selected: 'custom', region: rect })
    expect(customDesktopRegion(migrated)).toEqual({ ...rect, id: 'legacy', name: '' })
    expect(normalizeDesktopView({ selected: 'all', region: rect }).regions).toHaveLength(1)
    expect(normalizeDesktopView(migrated)).toEqual(migrated)
  })

  it('adds, edits, serializes, and deletes independent named regions', () => {
    const original: DesktopView = { selected: 'all' }
    const one = saveDesktopRegion(original, first)
    const two = saveDesktopRegion(one, second)
    const loaded = normalizeDesktopView(JSON.parse(JSON.stringify(two)))
    expect(loaded.regions).toEqual([first, second])
    expect(customDesktopRegion(loaded)).toEqual(second)
    const edited = saveDesktopRegion(loaded, { ...first, name: 'Main', width: 1800 })
    expect(edited.regions).toHaveLength(2)
    expect(loaded.regions![0]).toEqual(first)
    const removed = deleteDesktopRegion(edited, 'left')
    expect(removed.selected).toBe('all')
    expect(removed.regions).toEqual([second])
    expect(deleteDesktopRegion(two, 'left').selected).toBe('region:right')
    expect(original).toEqual({ selected: 'all' })
  })

  it('does not resurrect deleted legacy regions', () => {
    const old = normalizeDesktopView({ selected: 'custom', region: first })
    const deleted = deleteDesktopRegion(old, 'legacy')
    expect(normalizeDesktopView(JSON.parse(JSON.stringify(deleted)))).toEqual({ selected: 'all', regions: [] })
  })
})

describe('initial region sizes', () => {
  it('suggests the next unused half of a wide desktop', () => {
    const view = saveDesktopRegion({ selected: 'all' }, first)
    expect(suggestedDesktopRegion({ selected: 'all' }, 3840, 1080, [])).toEqual({ x: 0, y: 0, width: 1920, height: 1080 })
    expect(suggestedDesktopRegion(view, 3840, 1080, [])).toEqual({ x: 1920, y: 0, width: 1920, height: 1080 })
    expect(suggestedDesktopRegion({ selected: 'all' }, 1080, 2160, [])).toEqual({ x: 0, y: 0, width: 1080, height: 1080 })
  })

  it('uses actual display bounds when provided and an inset box on a single desktop', () => {
    expect(suggestedDesktopRegion({ selected: 'all' }, 4480, 1440, [{ ...first, id: 9 }, { ...second, id: 42 }])).toEqual({ x: 0, y: 0, width: 1920, height: 1080 })
    expect(suggestedDesktopRegion({ selected: 'all' }, 1920, 1080, [])).toEqual({ x: 192, y: 108, width: 1536, height: 864 })
  })

  it.each([[3841, 1081], [1, 1], [1, 1080], [1920, 1]])('keeps all presets within an odd or narrow %s × %s desktop', (width, height) => {
    const presets = desktopRegionPresets(width, height)
    expect(presets.length).toBeGreaterThan(0)
    expect(presets.every(preset => validDesktopRect(preset.rect, width, height))).toBe(true)
    const left = presets.find(preset => preset.id === 'leftHalf'), right = presets.find(preset => preset.id === 'rightHalf')
    if (left && right) {
      expect(left.rect.width + right.rect.width).toBe(width)
      expect(left.rect.x + left.rect.width).toBe(right.rect.x)
    }
  })
})

describe('visual region editing', () => {
  const rect = { x: 200, y: 100, width: 400, height: 300 }
  const start = { x: 250, y: 150 }
  const drag = (mode: RegionDragMode, x: number, y: number) => dragDesktopRegion({ mode, start, rect }, { x, y }, 1920, 1080)

  it('moves the region without resizing and clamps it to the desktop', () => {
    expect(drag('move', 450, 350)).toEqual({ x: 400, y: 300, width: 400, height: 300 })
    expect(drag('move', 9000, -200)).toEqual({ x: 1520, y: 0, width: 400, height: 300 })
  })

  it.each<RegionDragMode>(['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'])('resizes the %s handle within the desktop', mode => {
    const result = drag(mode, 350, 200)
    expect(validDesktopRect(result, 1920, 1080)).toBe(true)
    if (!mode.includes('n') && !mode.includes('s')) expect(result.height).toBe(300)
    if (!mode.includes('e') && !mode.includes('w')) expect(result.width).toBe(400)
    expect(result).not.toEqual(rect)
  })

  it('allows drawing in reverse and crossing the opposite edge', () => {
    expect(dragDesktopRegion({ mode: 'draw', start: { x: 600, y: 400 }, rect }, { x: 200, y: 100 }, 1920, 1080)).toEqual(rect)
    expect(drag('w', 950, 150)).toEqual({ x: 600, y: 100, width: 300, height: 300 })
  })

  it('keeps numeric position changes separate from size changes', () => {
    expect(changeDesktopCoordinate(rect, 'x', 1800, 1920, 1080)).toEqual({ ...rect, x: 1520 })
    expect(changeDesktopCoordinate(rect, 'width', 1900, 1920, 1080)).toEqual({ ...rect, width: 1720 })
    expect(changeDesktopCoordinate(rect, 'height', 0, 1920, 1080)).toEqual({ ...rect, height: 1 })
  })
})
