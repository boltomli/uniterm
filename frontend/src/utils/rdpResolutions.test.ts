import { describe, expect, it } from 'vitest'
import { RDP_RESOLUTION_PRESETS, resolutionSentinels, seedResolutionChoice } from './rdpResolutions'

describe('seedResolutionChoice', () => {
  it('maps the -2 sentinel (or absent fields) to adaptive', () => {
    expect(seedResolutionChoice({ rdpFixedWidth: -2, rdpFixedHeight: -2 })).toBe('adaptive')
    expect(seedResolutionChoice({})).toBe('adaptive')
    expect(seedResolutionChoice(null)).toBe('adaptive')
  })

  it('maps the -1 sentinel to fullscreen', () => {
    expect(seedResolutionChoice({ rdpFixedWidth: -1, rdpFixedHeight: -1 })).toBe('fullscreen')
  })

  it('maps positive sizes to a fixed WxH choice', () => {
    expect(seedResolutionChoice({ rdpFixedWidth: 1920, rdpFixedHeight: 1080 })).toBe('1920x1080')
  })
})

describe('resolutionSentinels', () => {
  it('round-trips with seedResolutionChoice', () => {
    for (const choice of ['adaptive', 'fullscreen', '1920x1080', '800x600'] as const) {
      expect(seedResolutionChoice(resolutionSentinels(choice))).toBe(choice)
    }
  })

  it('keeps the -2 / -1 sentinels', () => {
    expect(resolutionSentinels('adaptive')).toEqual({ rdpFixedWidth: -2, rdpFixedHeight: -2 })
    expect(resolutionSentinels('fullscreen')).toEqual({ rdpFixedWidth: -1, rdpFixedHeight: -1 })
    expect(resolutionSentinels('1280x720')).toEqual({ rdpFixedWidth: 1280, rdpFixedHeight: 720 })
  })

  it('ships a non-empty preset list with valid sizes', () => {
    expect(RDP_RESOLUTION_PRESETS.length).toBeGreaterThan(0)
    for (const p of RDP_RESOLUTION_PRESETS) {
      expect(p.w).toBeGreaterThanOrEqual(640)
      expect(p.h).toBeGreaterThanOrEqual(480)
      expect(p.label).toContain(String(p.w))
    }
  })
})
