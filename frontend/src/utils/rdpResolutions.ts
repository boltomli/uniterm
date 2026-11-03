export interface RdpResolutionPreset {
  label: string
  w: number
  h: number
}

// Default preset list (mstsc-style), shared by the connection form and the
// RDP status bar / fullscreen toolbar selectors. The RDP protocol accepts
// any desktop size, so presets cannot cover every monitor (e.g. 1360×768) —
// the connection form adds a custom entry with free width/height inputs.
export const RDP_RESOLUTION_PRESETS: RdpResolutionPreset[] = [
  { label: '800 × 600 (SVGA)', w: 800, h: 600 },
  { label: '1024 × 768 (XGA)', w: 1024, h: 768 },
  { label: '1280 × 720 (HD)', w: 1280, h: 720 },
  { label: '1280 × 800 (WXGA)', w: 1280, h: 800 },
  { label: '1280 × 1024 (SXGA)', w: 1280, h: 1024 },
  { label: '1366 × 768 (HD)', w: 1366, h: 768 },
  { label: '1440 × 900 (WXGA+)', w: 1440, h: 900 },
  { label: '1600 × 900 (HD+)', w: 1600, h: 900 },
  { label: '1600 × 1200 (UXGA)', w: 1600, h: 1200 },
  { label: '1680 × 1050 (WSXGA+)', w: 1680, h: 1050 },
  { label: '1920 × 1080 (Full HD)', w: 1920, h: 1080 },
  { label: '1920 × 1200 (WUXGA)', w: 1920, h: 1200 },
  { label: '2560 × 1440 (QHD)', w: 2560, h: 1440 },
  { label: '2560 × 1600 (WQXGA)', w: 2560, h: 1600 },
  { label: '3840 × 2160 (4K UHD)', w: 3840, h: 2160 },
]

// A resolution choice as carried by rdpFixedWidth/Height: 'adaptive' (-2,
// desktop follows the window), 'fullscreen' (-1, primary monitor size) or a
// fixed "WxH" pixel size.
export type RdpResolutionChoice = 'adaptive' | 'fullscreen' | `${number}x${number}`

export function seedResolutionChoice(config: { rdpFixedWidth?: number; rdpFixedHeight?: number } | null | undefined): RdpResolutionChoice {
  const w = config?.rdpFixedWidth
  const h = config?.rdpFixedHeight
  if (w === -1 || h === -1) return 'fullscreen'
  if (w && w > 0 && h && h > 0) return `${w}x${h}`
  return 'adaptive'
}

// The rdpFixedWidth/Height sentinel values to persist for a choice.
export function resolutionSentinels(choice: RdpResolutionChoice): { rdpFixedWidth: number; rdpFixedHeight: number } {
  if (choice === 'adaptive') return { rdpFixedWidth: -2, rdpFixedHeight: -2 }
  if (choice === 'fullscreen') return { rdpFixedWidth: -1, rdpFixedHeight: -1 }
  const idx = choice.indexOf('x')
  return { rdpFixedWidth: Number(choice.slice(0, idx)), rdpFixedHeight: Number(choice.slice(idx + 1)) }
}
