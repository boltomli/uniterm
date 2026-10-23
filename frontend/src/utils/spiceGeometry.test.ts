import { describe, expect, it } from 'vitest'
import { spiceDesktopGeometry } from './spiceGeometry'

describe('SPICE display geometry', () => {
  it.each([1, 1.25, 1.5, 2])('keeps one remote pixel per physical pixel at display scale %s', ratio => {
    const geometry = spiceDesktopGeometry(1920, 1080, 1000, 700, false, ratio)!
    expect(geometry.width * ratio).toBeCloseTo(1920)
    expect(geometry.height * ratio).toBeCloseTo(1080)
    expect((100 / ratio) / geometry.scale).toBeCloseTo(100)
  })

  it('fits fractional viewports without overflow or aspect ratio distortion', () => {
    const geometry = spiceDesktopGeometry(1920, 1080, 1000.25, 500.5, true, 1.5)!
    expect(geometry.width).toBeLessThanOrEqual(1000.25)
    expect(geometry.height).toBeLessThanOrEqual(500.5)
    expect(geometry.width / geometry.height).toBeCloseTo(1920 / 1080)
    expect((100 * geometry.scale) * (1920 / geometry.width)).toBeCloseTo(100)
  })

  it('keeps the existing automatic fitting limit and recovers native pixels when disabled', () => {
    const fitted = spiceDesktopGeometry(800, 600, 1600, 1200, true, 1.5)!
    expect(fitted.width).toBe(800)
    expect(fitted.height).toBe(600)
    const native = spiceDesktopGeometry(800, 600, 1600, 1200, false, 1.5)!
    expect(native.width * 1.5).toBeCloseTo(800)
  })

  it.each([0, -1, NaN, Infinity])('handles unavailable monitor scale %s', ratio => {
    expect(spiceDesktopGeometry(800, 600, 0, 0, false, ratio)).toEqual({ scale: 1, width: 800, height: 600 })
  })

  it('keeps native dimensions while the tab is hidden', () => {
    const geometry = spiceDesktopGeometry(1920, 1080, 0, 0, true, 1.5)!
    expect(geometry.width * 1.5).toBeCloseTo(1920)
    expect(geometry.height * 1.5).toBeCloseTo(1080)
  })

  it.each([[0, 1080], [1920, 0], [NaN, 1080], [1920, Infinity]])('ignores unknown surface dimensions %s × %s', (width, height) => {
    expect(spiceDesktopGeometry(width, height, 1000, 700, false, 1.5)).toBeNull()
  })
})
