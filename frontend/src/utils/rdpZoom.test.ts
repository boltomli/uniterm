import { describe, expect, it } from 'vitest'
import { DEFAULT_RDP_ZOOM, adaptiveSizeRatio, parseRdpZoom, serializeRdpZoom, zoomedCssSize } from './rdpZoom'

describe('parseRdpZoom', () => {
  it('accepts the named modes', () => {
    expect(parseRdpZoom('adaptive')).toBe('adaptive')
    expect(parseRdpZoom('stretch')).toBe('stretch')
  })

  it('parses numeric factors from strings and numbers', () => {
    expect(parseRdpZoom('0.75')).toBe(0.75)
    expect(parseRdpZoom(1.5)).toBe(1.5)
    expect(parseRdpZoom('2')).toBe(2)
    expect(parseRdpZoom(1)).toBe(1)
  })

  it('falls back to 100% for absent or invalid values', () => {
    expect(parseRdpZoom(undefined)).toBe(DEFAULT_RDP_ZOOM)
    expect(parseRdpZoom('')).toBe(DEFAULT_RDP_ZOOM)
    expect(parseRdpZoom('bigger')).toBe(DEFAULT_RDP_ZOOM)
    expect(parseRdpZoom(-1)).toBe(DEFAULT_RDP_ZOOM)
    expect(parseRdpZoom(0)).toBe(DEFAULT_RDP_ZOOM)
    expect(DEFAULT_RDP_ZOOM).toBe(1)
  })
})

describe('serializeRdpZoom', () => {
  it('round-trips through parseRdpZoom', () => {
    for (const zoom of ['adaptive', 0.5, 0.75, 1, 1.25, 1.5, 1.75, 2] as const) {
      expect(parseRdpZoom(serializeRdpZoom(zoom))).toBe(zoom)
    }
  })
})

describe('zoomedCssSize', () => {
  it('renders one framebuffer pixel per device pixel at 100%', () => {
    expect(zoomedCssSize(1920, 1, 2)).toBe(960)
    expect(zoomedCssSize(1920, 1, 1)).toBe(1920)
  })

  it('scales physical pixels by the zoom factor', () => {
    expect(zoomedCssSize(1920, 0.5, 2)).toBe(480)
    expect(zoomedCssSize(1920, 2, 2)).toBe(1920)
  })

  it('falls back to dpr 1 for a bogus pixel ratio', () => {
    expect(zoomedCssSize(1920, 1, NaN)).toBe(1920)
    expect(zoomedCssSize(1920, 1, 0)).toBe(1920)
  })
})

describe('adaptiveSizeRatio', () => {
  it('uses the device pixel ratio for the fit mode', () => {
    expect(adaptiveSizeRatio('adaptive', 1.5)).toBe(1.5)
  })

  it('divides the device pixel ratio by the zoom so the zoomed desktop fills the pane', () => {
    expect(adaptiveSizeRatio(1, 2)).toBe(2)
    expect(adaptiveSizeRatio(0.75, 2)).toBeCloseTo(2 / 0.75)
    expect(adaptiveSizeRatio(2, 2)).toBe(1)
  })

  it('falls back to dpr 1 for a bogus pixel ratio', () => {
    expect(adaptiveSizeRatio('adaptive', NaN)).toBe(1)
    expect(adaptiveSizeRatio(2, 0)).toBe(0.5)
  })
})
