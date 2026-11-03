// RDP/VNC/SPICE display zoom: 'adaptive' CSS-fits the framebuffer to the
// pane, 'stretch' fills the pane ignoring the aspect ratio, a number is the
// PHYSICAL pixel scale — 100% renders one framebuffer pixel per device
// pixel (native, sharp), 50% shows each pixel at half device size (more
// content fits), 200% doubles it (accessibility zoom).
// With adaptive resolution a numeric factor Z negotiates a framebuffer of
// pane × devicePixelRatio / Z, so the zoomed desktop still fills the pane
// with Z device pixels per framebuffer pixel.
export type RdpZoom = 'adaptive' | 'stretch' | number

export const ZOOM_FACTORS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2] as const
export const DEFAULT_RDP_ZOOM: RdpZoom = 1

// Persisted in desktopPreferences.rdpZoom as a string: 'adaptive' /
// 'stretch' / the factor ("0.75"); absent or invalid falls back to the
// default (100%). Numbers stay parseable across locales.
export function serializeRdpZoom(zoom: RdpZoom): string {
  return typeof zoom === 'number' ? String(zoom) : zoom
}

export function parseRdpZoom(value: unknown): RdpZoom {
  if (value === 'adaptive' || value === 'stretch') return value
  const n = typeof value === 'number' ? value : Number(value)
  if (Number.isFinite(n) && n > 0.1 && n <= 4) return n
  return DEFAULT_RDP_ZOOM
}

// CSS size of a framebuffer at the zoom: physical scale Z means the canvas
// spans fb × Z device pixels = fb × Z / dpr CSS pixels.
export function zoomedCssSize(size: number, zoom: number, pixelRatio: number): number {
  const dpr = Number.isFinite(pixelRatio) && pixelRatio > 0 ? pixelRatio : 1
  return size * zoom / dpr
}

// Ratio applied to the pane's CSS size when negotiating the adaptive
// desktop size: 'adaptive' (fit) and 'stretch' use the device pixel ratio so
// the filled view stays sharp (stretch fills the pane exactly anyway); a
// numeric zoom Z requests pane × dpr / Z so each framebuffer pixel lands on
// exactly Z device pixels.
export function adaptiveSizeRatio(zoom: RdpZoom, pixelRatio: number): number {
  const dpr = Number.isFinite(pixelRatio) && pixelRatio > 0 ? pixelRatio : 1
  return typeof zoom === 'number' ? dpr / zoom : dpr
}
