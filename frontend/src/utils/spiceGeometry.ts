import { zoomedCssSize } from './rdpZoom'
import type { RdpZoom } from './rdpZoom'

export interface SpiceGeometry {
  scaleX: number
  scaleY: number
  width: number
  height: number
}

// Display geometry for the SPICE canvas: 'adaptive' fits the viewport in
// both directions (small desktops also upscale to fill a fullscreen window,
// matching VNC/RDP); 'stretch' fills the pane ignoring the aspect ratio; a
// numeric zoom is the PHYSICAL pixel scale (100% = one framebuffer pixel
// per device pixel), expressed as a CSS scale of zoom / devicePixelRatio.
export function spiceDesktopGeometry(
  width: number,
  height: number,
  viewportWidth: number,
  viewportHeight: number,
  zoom: RdpZoom,
  pixelRatio: number,
): SpiceGeometry | null {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return null
  const canFit = Number.isFinite(viewportWidth) && Number.isFinite(viewportHeight) && viewportWidth > 0 && viewportHeight > 0
  let scaleX: number, scaleY: number
  if (zoom === 'adaptive') {
    scaleX = scaleY = canFit ? Math.min(viewportWidth / width, viewportHeight / height) : 1
  } else if (zoom === 'stretch') {
    if (!canFit) return { scaleX: 1, scaleY: 1, width, height }
    scaleX = viewportWidth / width
    scaleY = viewportHeight / height
  } else {
    scaleX = scaleY = zoomedCssSize(1, zoom, pixelRatio)
  }
  return { scaleX, scaleY, width: width * scaleX, height: height * scaleY }
}
