import { describe, expect, it } from 'vitest'
import { spiceDesktopGeometry } from './spiceGeometry'

describe('SPICE display geometry', () => {
  it.each([
    [1, 2, 0.5], // 100% on a 2× display = one framebuffer pixel per device pixel
    [0.5, 2, 0.25],
    [2, 2, 1],
    [1, 1, 1],
  ])('renders zoom %s at display scale %s as CSS scale %s', (zoom, dpr, css) => {
    const geometry = spiceDesktopGeometry(1920, 1080, 1000, 700, zoom, dpr)!
    expect(geometry.scaleX).toBeCloseTo(css)
    expect(geometry.scaleY).toBeCloseTo(css)
    expect(geometry.width).toBeCloseTo(1920 * css)
    expect(geometry.height).toBeCloseTo(1080 * css)
  })

  it('fits fractional viewports without overflow or aspect ratio distortion', () => {
    const geometry = spiceDesktopGeometry(1920, 1080, 1000.25, 500.5, 'adaptive', 1.5)!
    expect(geometry.width).toBeLessThanOrEqual(1000.25)
    expect(geometry.height).toBeLessThanOrEqual(500.5)
    expect(geometry.width / geometry.height).toBeCloseTo(1920 / 1080)
    expect((100 * geometry.scaleX) * (1920 / geometry.width)).toBeCloseTo(100)
  })

  it('stretches to fill the pane ignoring the aspect ratio', () => {
    const geometry = spiceDesktopGeometry(1920, 1080, 960, 900, 'stretch', 2)!
    expect(geometry.scaleX).toBeCloseTo(0.5)
    expect(geometry.scaleY).toBeCloseTo(900 / 1080)
    expect(geometry.width).toBeCloseTo(960)
    expect(geometry.height).toBeCloseTo(900)
  })

  it('upscales small desktops to fill the viewport like VNC/RDP', () => {
    const fitted = spiceDesktopGeometry(800, 600, 1600, 1200, 'adaptive', 1.5)!
    expect(fitted.width).toBe(1600)
    expect(fitted.height).toBe(1200)
  })

  it('keeps the fit scale while the tab is hidden', () => {
    const geometry = spiceDesktopGeometry(1920, 1080, 0, 0, 'adaptive', 1.5)!
    expect(geometry).toEqual({ scaleX: 1, scaleY: 1, width: 1920, height: 1080 })
  })

  it('stretches at native scale while the tab is hidden', () => {
    const geometry = spiceDesktopGeometry(1920, 1080, 0, 0, 'stretch', 1.5)!
    expect(geometry).toEqual({ scaleX: 1, scaleY: 1, width: 1920, height: 1080 })
  })

  it('falls back to CSS scale 1 for a bogus pixel ratio', () => {
    expect(spiceDesktopGeometry(1920, 1080, 1000, 700, 1, NaN)!.scaleX).toBe(1)
    expect(spiceDesktopGeometry(1920, 1080, 1000, 700, 2, 0)!.scaleX).toBe(2)
  })

  it.each([[0, 1080], [1920, 0], [NaN, 1080], [1920, Infinity]])('ignores unknown surface dimensions %s × %s', (width, height) => {
    expect(spiceDesktopGeometry(width, height, 1000, 700, 1, 1)).toBeNull()
  })
})
