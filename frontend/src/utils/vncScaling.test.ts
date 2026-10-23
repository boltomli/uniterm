import { describe, expect, it, vi } from 'vitest'
import { installVncPixelScaling } from './vncScaling'

function createRfb() {
  return {
    scaleViewport: false,
    _display: { scale: 1 },
    _updateScale: vi.fn(function (this: { _display: { scale: number } }) {
      this._display.scale = 0.75
    }),
    _fixScrollbars: vi.fn(),
  }
}

describe('VNC physical pixel scaling', () => {
  it.each([1, 1.25, 1.5, 2])('preserves native pixels and pointer mapping at display scale %s', ratio => {
    const rfb = createRfb()
    installVncPixelScaling(rfb, () => ratio)
    expect(1920 * rfb._display.scale * ratio).toBeCloseTo(1920)
    expect(1080 * rfb._display.scale * ratio).toBeCloseTo(1080)
    expect((960 / ratio) / rfb._display.scale).toBeCloseTo(960)
    expect(rfb._fixScrollbars).toHaveBeenCalledOnce()
  })

  it('keeps automatic fitting and restores native pixels when fitting is disabled', () => {
    const rfb = createRfb()
    const original = rfb._updateScale
    installVncPixelScaling(rfb, () => 1.5)
    rfb.scaleViewport = true
    rfb._updateScale()
    expect(original).toHaveBeenCalledOnce()
    expect(rfb._display.scale).toBe(0.75)
    rfb.scaleViewport = false
    rfb._updateScale()
    expect(rfb._display.scale).toBeCloseTo(1 / 1.5)
  })

  it('recalculates after moving between monitors and restores the cached instance', () => {
    const rfb = createRfb()
    const original = rfb._updateScale
    let ratio = 1.25
    const restore = installVncPixelScaling(rfb, () => ratio)
    ratio = 2
    rfb._updateScale()
    expect(rfb._display.scale).toBe(0.5)
    restore()
    expect(rfb._updateScale).toBe(original)
    const restoreAgain = installVncPixelScaling(rfb, () => 1.5)
    expect(rfb._display.scale).toBeCloseTo(1 / 1.5)
    restoreAgain()
    expect(rfb._updateScale).toBe(original)
  })

  it.each([0, -1, NaN, Infinity])('falls back to native CSS pixels for an invalid display scale %s', ratio => {
    const rfb = createRfb()
    installVncPixelScaling(rfb, () => ratio)
    expect(rfb._display.scale).toBe(1)
  })
})
