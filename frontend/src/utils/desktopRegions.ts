import type { DesktopDisplay, DesktopRect, DesktopRegion, DesktopView } from './vncDisplays'

export function normalizeDesktopView(view: DesktopView): DesktopView {
  const regions = view.regions?.map(region => ({ ...region })) ??
    (view.region ? [{ ...view.region, id: 'legacy', name: '' }] : [])
  return {
    selected: view.selected === 'custom' ? (regions.length ? `region:${regions[0]!.id}` : 'all') : view.selected,
    regions,
  }
}

export function customDesktopRegion(view: DesktopView): DesktopRegion | undefined {
  const normalized = normalizeDesktopView(view)
  return normalized.regions!.find(region => `region:${region.id}` === normalized.selected)
}

export function saveDesktopRegion(view: DesktopView, region: DesktopRegion): DesktopView {
  const regions = normalizeDesktopView(view).regions!
  const index = regions.findIndex(saved => saved.id === region.id)
  if (index < 0) regions.push({ ...region })
  else regions[index] = { ...region }
  return { selected: `region:${region.id}`, regions }
}

export function deleteDesktopRegion(view: DesktopView, id: string): DesktopView {
  const normalized = normalizeDesktopView(view)
  return {
    selected: normalized.selected === `region:${id}` ? 'all' : normalized.selected,
    regions: normalized.regions!.filter(region => region.id !== id),
  }
}

export interface RegionPreset {
  id: string
  labelKey: string
  number?: number
  rect: DesktopRect
}

export function desktopRegionPresets(width: number, height: number, displays: DesktopDisplay[] = []): RegionPreset[] {
  if (!Number.isInteger(width) || !Number.isInteger(height) || width < 1 || height < 1) return []
  const halfWidth = Math.floor(width / 2), halfHeight = Math.floor(height / 2)
  const centerWidth = Math.max(1, Math.floor(width * 0.8)), centerHeight = Math.max(1, Math.floor(height * 0.8))
  const preset = (id: string, rect: DesktopRect): RegionPreset => ({ id, labelKey: `remoteDesktop.${id}`, rect })
  return [
    ...displays.map((display, index) => ({ id: `screen:${display.id}`, labelKey: 'remoteDesktop.displayNumber', number: index + 1,
      rect: { x: display.x, y: display.y, width: display.width, height: display.height } })),
    preset('fullDesktop', { x: 0, y: 0, width, height }),
    preset('centerRegion', { x: Math.floor((width - centerWidth) / 2), y: Math.floor((height - centerHeight) / 2), width: centerWidth, height: centerHeight }),
    preset('leftHalf', { x: 0, y: 0, width: halfWidth, height }),
    preset('rightHalf', { x: halfWidth, y: 0, width: width - halfWidth, height }),
    preset('topHalf', { x: 0, y: 0, width, height: halfHeight }),
    preset('bottomHalf', { x: 0, y: halfHeight, width, height: height - halfHeight }),
    preset('topLeftQuarter', { x: 0, y: 0, width: halfWidth, height: halfHeight }),
    preset('topRightQuarter', { x: halfWidth, y: 0, width: width - halfWidth, height: halfHeight }),
    preset('bottomLeftQuarter', { x: 0, y: halfHeight, width: halfWidth, height: height - halfHeight }),
    preset('bottomRightQuarter', { x: halfWidth, y: halfHeight, width: width - halfWidth, height: height - halfHeight }),
  ].filter(preset => preset.rect.width > 0 && preset.rect.height > 0 && preset.rect.x >= 0 && preset.rect.y >= 0 &&
    preset.rect.x + preset.rect.width <= width && preset.rect.y + preset.rect.height <= height)
}

export function sameDesktopRect(a: DesktopRect, b: DesktopRect): boolean {
  return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height
}

export function suggestedDesktopRegion(view: DesktopView, width: number, height: number, displays: DesktopDisplay[]): DesktopRect {
  const presets = desktopRegionPresets(width, height, displays)
  const preferred = displays.length ? presets.filter(preset => preset.id.startsWith('screen:')) :
    presets.filter(preset => (width >= height * 2 ? ['leftHalf', 'rightHalf'] :
      height >= width * 1.5 ? ['topHalf', 'bottomHalf'] : ['centerRegion']).includes(preset.id))
  const regions = normalizeDesktopView(view).regions!
  return { ...(preferred.find(preset => !regions.some(region => sameDesktopRect(region, preset.rect))) ?? preferred[0] ?? presets[0])!.rect }
}

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value))

export function fitDesktopRect(rect: DesktopRect, width: number, height: number): DesktopRect {
  const w = clamp(Math.round(rect.width), 1, width), h = clamp(Math.round(rect.height), 1, height)
  return { x: clamp(Math.round(rect.x), 0, width - w), y: clamp(Math.round(rect.y), 0, height - h), width: w, height: h }
}

export function changeDesktopCoordinate(rect: DesktopRect, key: keyof DesktopRect, value: number, width: number, height: number): DesktopRect {
  const next = fitDesktopRect(rect, width, height)
  const max = { x: width - next.width, y: height - next.height, width: width - next.x, height: height - next.y }
  next[key] = clamp(Math.round(value), key === 'width' || key === 'height' ? 1 : 0, max[key])
  return next
}

export type RegionDragMode = 'draw' | 'move' | 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw'
export interface RegionPoint { x: number; y: number }
export interface RegionDrag {
  mode: RegionDragMode
  start: RegionPoint
  rect: DesktopRect
}

export function dragDesktopRegion(drag: RegionDrag, point: RegionPoint, width: number, height: number): DesktopRect {
  const x = clamp(Math.round(point.x), 0, width), y = clamp(Math.round(point.y), 0, height)
  const dx = x - drag.start.x, dy = y - drag.start.y
  if (drag.mode === 'move') {
    return { ...drag.rect, x: clamp(Math.round(drag.rect.x + dx), 0, width - drag.rect.width),
      y: clamp(Math.round(drag.rect.y + dy), 0, height - drag.rect.height) }
  }
  let left = drag.rect.x, right = left + drag.rect.width, top = drag.rect.y, bottom = top + drag.rect.height
  if (drag.mode === 'draw') { left = drag.start.x; top = drag.start.y; right = x; bottom = y }
  else {
    if (drag.mode.includes('w')) left = clamp(left + dx, 0, width)
    if (drag.mode.includes('e')) right = clamp(right + dx, 0, width)
    if (drag.mode.includes('n')) top = clamp(top + dy, 0, height)
    if (drag.mode.includes('s')) bottom = clamp(bottom + dy, 0, height)
  }
  return fitDesktopRect({ x: Math.min(left, right), y: Math.min(top, bottom), width: Math.max(1, Math.abs(right - left)), height: Math.max(1, Math.abs(bottom - top)) }, width, height)
}
