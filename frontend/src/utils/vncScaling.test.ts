import { describe, expect, it, vi } from 'vitest'
import { installVncPixelScaling } from './vncScaling'

function createRfb() {
  const canvas = {
    width: 1920,
    height: 1080,
    style: {} as Record<string, string>,
    // Pretend the canvas is CSS-stretched to half its framebuffer size.
    getBoundingClientRect: () => ({ width: 960, height: 540 }),
  }
  const display = {
    scale: 1,
    _clipViewport: false,
    _viewportLoc: { x: 0, y: 0 },
    _target: canvas,
    absX: (x: number) => x * 2,
    absY: (y: number) => y * 2,
  }
  return {
    scaleViewport: false,
    _display: display,
    _canvas: canvas,
    _updateScale: vi.fn(function (this: { _display: { scale: number } }) {
      this._display.scale = 0.75
    }),
    _fixScrollbars: vi.fn(),
  }
}

describe('VNC zoom scaling', () => {
  it.each([
    [1, 2, 0.5], // 100% on a 2× display = one framebuffer pixel per device pixel
    [0.5, 2, 0.25],
    [2, 2, 1],
    [1, 1, 1],
    [1.25, 1.5, 1.25 / 1.5],
  ])('renders zoom %s at display scale %s as CSS scale %s', (zoom, dpr, css) => {
    const rfb = createRfb()
    installVncPixelScaling(rfb, () => zoom, () => dpr)
    expect(rfb._display.scale).toBeCloseTo(css)
    expect(rfb._fixScrollbars).toHaveBeenCalledOnce()
  })

  it('keeps automatic fitting in fit mode and restores it afterwards', () => {
    const rfb = createRfb()
    const original = rfb._updateScale
    installVncPixelScaling(rfb, () => 'adaptive')
    original.mockClear()
    rfb._updateScale()
    expect(original).toHaveBeenCalledOnce()
    expect(rfb._display.scale).toBe(0.75)
  })

  it('follows zoom changes through the getter', () => {
    const rfb = createRfb()
    const original = rfb._updateScale
    let zoom: number | 'adaptive' = 1
    const restore = installVncPixelScaling(rfb, () => zoom, () => 2)
    zoom = 1.5
    rfb._updateScale()
    expect(rfb._display.scale).toBeCloseTo(0.75)
    zoom = 'adaptive'
    rfb._updateScale()
    expect(rfb._display.scale).toBe(0.75)
    restore()
    expect(rfb._updateScale).toBe(original)
  })

  it('stretches the canvas to the pane and maps pointers per-axis', () => {
    const rfb = createRfb()
    const originalAbsX = rfb._display.absX
    installVncPixelScaling(rfb, () => 'stretch', () => 2)
    expect(rfb._display.scale).toBe(1)
    expect(rfb._canvas.style.width).toBe('100%')
    expect(rfb._canvas.style.height).toBe('100%')
    // CSS px are half the framebuffer px per axis (1920/960, 1080/540).
    expect(rfb._display.absX!(100)).toBe(200)
    expect(rfb._display.absY!(100)).toBe(200)
    // Reinstalling over the patch restores the original mapping.
    installVncPixelScaling(rfb, () => 1, () => 1)
    expect(rfb._display.absX).toBe(originalAbsX)
    expect(rfb._display.scale).toBe(1)
  })

  it('falls back to fit when a region crop clips the viewport', () => {
    const rfb = createRfb()
    const original = rfb._updateScale
    installVncPixelScaling(rfb, () => 'stretch', () => 1)
    rfb._display._clipViewport = true
    original.mockClear()
    rfb._updateScale()
    expect(original).toHaveBeenCalledOnce()
    expect(rfb._display.scale).toBe(0.75)
  })

  it('reinstalls over a fresh patch and restores the native updater', () => {
    const rfb = createRfb()
    const original = rfb._updateScale
    const restore = installVncPixelScaling(rfb, () => 2, () => 1)
    expect(rfb._display.scale).toBe(2)
    restore()
    expect(rfb._updateScale).toBe(original)
    const restoreAgain = installVncPixelScaling(rfb, () => 1.5, () => 1)
    expect(rfb._display.scale).toBe(1.5)
    restoreAgain()
    expect(rfb._updateScale).toBe(original)
  })
})
