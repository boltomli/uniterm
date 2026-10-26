import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { installVncDisplays, parseExtendedDisplays, validDesktopRect, type DesktopDisplayState } from './vncDisplays'
import { installVncPixelScaling } from './vncScaling'
import { deleteDesktopRegion, saveDesktopRegion } from './desktopRegions'

let Display: any
function canvas() {
  return { width: 0, height: 0, style: {}, getContext: () => ({
    drawImage: vi.fn(), getImageData: () => ({}), putImageData: vi.fn(),
  }) }
}
beforeAll(async () => {
  vi.stubGlobal('window', { console })
  vi.stubGlobal('document', { createElement: canvas })
  Display = (await import('@novnc-internals/display.js')).default
})
afterAll(() => vi.unstubAllGlobals())

function layoutBytes(screens = [
  { id: 9, x: 0, y: 0, width: 1920, height: 1080 },
  { id: 42, x: 1920, y: 0, width: 2560, height: 1440 },
]) {
  const bytes = new Uint8Array(4 + screens.length * 16), data = new DataView(bytes.buffer)
  bytes[0] = screens.length
  screens.forEach((screen, i) => {
    const at = 4 + i * 16
    data.setUint32(at, screen.id); data.setUint16(at + 4, screen.x); data.setUint16(at + 6, screen.y)
    data.setUint16(at + 8, screen.width); data.setUint16(at + 10, screen.height)
  })
  return bytes
}

function fakeRfb() {
  const rfb: any = Object.assign(new EventTarget(), {
    _fbWidth: 4480, _fbHeight: 1440, _display: new Display(canvas()), scaleViewport: false,
    _fixScrollbars: vi.fn(), _screenSize: () => ({ w: 960, h: 600 }),
    _FBU: { x: 0, y: 0, width: 4480, height: 1440 },
    _resize(width: number, height: number) {
      this._fbWidth = width; this._fbHeight = height
      this._display.resize(width, height)
      this._updateClip(); this._updateScale()
    },
    _updateClip() { this._display.clipViewport = false },
    _updateScale() { this._display.autoscale(960, 600) },
    _handleExtendedDesktopSize() {
      if (this._FBU.x !== 1 || this._FBU.y === 0) this._resize(this._FBU.width, this._FBU.height)
      return true
    },
  })
  rfb._display.resize(4480, 1440)
  installVncPixelScaling(rfb, () => 2)
  return rfb
}

function receiveLayout(rfb: any, bytes = layoutBytes(), available = bytes.length) {
  rfb._sock = {
    rQwait: (_label: string, length: number) => available < length,
    rQpeek8: () => bytes[0], rQpeekBytes: () => bytes,
  }
  return rfb._handleExtendedDesktopSize()
}

describe('VNC display viewport using the noVNC renderer', () => {
  it('switches between saved crops and restores them on a fresh connection', () => {
    const left = { id: 'left', name: 'Left', x: 0, y: 0, width: 1920, height: 1080 }
    const right = { id: 'right', name: 'Right', x: 1920, y: 0, width: 2560, height: 1440 }
    const saved = saveDesktopRegion(saveDesktopRegion({ selected: 'all' }, left), right)
    const rfb = fakeRfb()
    const controller = installVncDisplays(rfb, saved, () => {})
    expect(rfb._display.absX(50)).toBe(2020)
    controller.select({ ...saved, selected: 'region:left' })
    expect(rfb._display.absX(50)).toBe(100)
    controller.dispose()
    const fresh = fakeRfb()
    let state: DesktopDisplayState | undefined
    const restored = installVncDisplays(fresh, JSON.parse(JSON.stringify(saved)), value => { state = value })
    expect(fresh._display.absX(50)).toBe(2020)
    expect(state?.view.regions).toHaveLength(2)
    restored.select(deleteDesktopRegion(saved, 'right'))
    expect(fresh._display._viewportLoc).toEqual({ x: 0, y: 0, w: 4480, h: 1440 })
    expect(state?.view.regions).toEqual([left])
  })
  it('keeps the complete framebuffer and maps Retina pointer coordinates to the selected display', () => {
    const rfb = fakeRfb()
    const controller = installVncDisplays(rfb, { selected: 'all' }, () => {})
    receiveLayout(rfb)
    controller.select({ selected: 'screen:42' })
    expect(rfb._display._viewportLoc).toEqual({ x: 1920, y: 0, w: 2560, h: 1440 })
    expect(rfb._display.width).toBe(4480)
    expect(rfb._display.height).toBe(1440)
    expect(rfb._display.absX(50)).toBe(2020)
    expect(rfb._display.absY(50)).toBe(100)
    rfb.scaleViewport = true
    rfb._updateClip(); rfb._updateScale()
    expect(rfb._display.scale).toBeCloseTo(960 / 2560)
    expect(rfb._display.absX(37.5)).toBe(2020)
    expect(rfb._display.absY(37.5)).toBe(100)
    controller.select({ selected: 'all' })
    expect(rfb._display._viewportLoc).toEqual({ x: 0, y: 0, w: 4480, h: 1440 })
    controller.dispose()
  })

  it('does not publish partial layouts or failed resize requests', () => {
    const rfb = fakeRfb(), changed = vi.fn()
    installVncDisplays(rfb, { selected: 'all' }, changed)
    changed.mockClear()
    expect(receiveLayout(rfb, layoutBytes(), 12)).toBe(false)
    expect(changed).not.toHaveBeenCalled()
    rfb._FBU = { x: 1, y: 3, width: 1, height: 1 }
    expect(receiveLayout(rfb)).toBe(true)
    expect(changed).not.toHaveBeenCalled()
  })

  it('restores a saved display after the first layout and falls back safely after unplugging it', () => {
    const rfb = fakeRfb()
    let state: DesktopDisplayState | undefined
    installVncDisplays(rfb, { selected: 'screen:42' }, value => { state = value })
    receiveLayout(rfb)
    expect(state?.view.selected).toBe('screen:42')
    rfb._FBU.width = 1920; rfb._FBU.height = 1080
    receiveLayout(rfb, layoutBytes([{ id: 9, x: 0, y: 0, width: 1920, height: 1080 }]))
    expect(state?.view.selected).toBe('all')
    expect(rfb._display._viewportLoc).toEqual({ x: 0, y: 0, w: 1920, h: 1080 })
  })

  it('supports custom vertical regions and restores hooks across tab caching', () => {
    const rfb = fakeRfb(), originalResize = rfb._resize
    const controller = installVncDisplays(rfb, { selected: 'all' }, () => {})
    controller.select({ selected: 'custom', region: { x: 0, y: 400, width: 1080, height: 1000 } })
    expect(rfb._display.absY(50)).toBe(500)
    controller.dispose()
    expect(rfb._resize).toBe(originalResize)
    installVncDisplays(rfb, { selected: 'all' }, () => {})
    expect(rfb._display._viewportLoc).toEqual({ x: 0, y: 400, w: 1080, h: 1000 })
  })

})

describe('display layout validation', () => {
  it('accepts a mixed-size layout and rejects truncated metadata', () => {
    expect(parseExtendedDisplays(layoutBytes())[1]).toEqual({ id: 42, x: 1920, y: 0, width: 2560, height: 1440 })
    expect(() => parseExtendedDisplays(layoutBytes().subarray(1))).toThrow()
  })
  it.each([
    { x: -1, y: 0, width: 1, height: 1 }, { x: 0, y: 0, width: 0, height: 1 },
    { x: 10, y: 10, width: 100, height: 100 }, { x: 0, y: 0, width: NaN, height: 1 },
    { x: 0.5, y: 0, width: 1, height: 1 },
  ])('rejects an invalid custom region %j', rect => {
    expect(validDesktopRect(rect, 100, 100)).toBe(false)
  })
})
