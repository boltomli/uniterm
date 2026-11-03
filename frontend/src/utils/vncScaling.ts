import { zoomedCssSize } from './rdpZoom'
import type { RdpZoom } from './rdpZoom'

// Structural stand-in for the noVNC visible canvas (kept duck-typed so test
// doubles don't need a full HTMLCanvasElement).
interface VncCanvasLike {
  width: number
  height: number
  style: Record<string, string>
  getBoundingClientRect(): { width: number; height: number }
}

interface VncDisplay {
  scale: number
  _clipViewport?: boolean
  _viewportLoc?: { x: number; y: number }
  _target?: VncCanvasLike
  absX?: (x: number) => number
  absY?: (y: number) => number
}

interface VncScalingTarget {
  scaleViewport: boolean
  _display: VncDisplay
  _canvas?: VncCanvasLike
  _updateScale(): void
  _fixScrollbars(): void
}

// Per-axis framebuffer factor for the stretched canvas: framebuffer px per
// CSS px, read live from the canvas rect so window resizes stay correct.
function stretchedAbsFactor(canvas: VncCanvasLike, axis: 'width' | 'height'): number {
  const rect = canvas.getBoundingClientRect()
  const css = axis === 'width' ? rect.width : rect.height
  return css > 0 ? canvas[axis] / css : 1
}

// Zoom semantics for noVNC: 'adaptive' keeps noVNC's own fit-to-viewport
// scaling, a numeric factor is the PHYSICAL pixel scale (100% = one
// framebuffer pixel per device pixel) via the uniform display scale, and
// 'stretch' fills the pane ignoring the aspect ratio — the canvas CSS-fills
// the screen element and absX/absY are patched to divide per-axis by the
// live CSS size, keeping pointer mapping exact (issue #1310). With a region
// or single-screen crop active the canvas is a clipped viewport, so stretch
// falls back to fit.
export function installVncPixelScaling(
  rfb: VncScalingTarget,
  getZoom: () => RdpZoom = () => 'adaptive',
  getPixelRatio: () => number = () => window.devicePixelRatio,
): () => void {
  const original = rfb._updateScale
  const display = rfb._display
  // Capture the true originals once: reinstalling over an existing patch
  // must not treat the patched absX/absY as the originals.
  const host = display as unknown as { __unitermOrigAbs?: { absX?: (x: number) => number; absY?: (y: number) => number } }
  if (!host.__unitermOrigAbs) host.__unitermOrigAbs = { absX: display.absX, absY: display.absY }
  const originalAbs = host.__unitermOrigAbs
  const restoreAbs = () => {
    if (display.absX !== originalAbs.absX) display.absX = originalAbs.absX
    if (display.absY !== originalAbs.absY) display.absY = originalAbs.absY
  }
  const updateScale = () => {
    const zoom = getZoom()
    const canvas = rfb._canvas
    if (zoom === 'stretch' && canvas && !display._clipViewport) {
      display.scale = 1
      canvas.style.width = '100%'
      canvas.style.height = '100%'
      const locX = display._viewportLoc?.x ?? 0
      const locY = display._viewportLoc?.y ?? 0
      display.absX = (x: number) => x * stretchedAbsFactor(canvas, 'width') + locX
      display.absY = (y: number) => y * stretchedAbsFactor(canvas, 'height') + locY
      rfb._fixScrollbars()
      return
    }
    restoreAbs()
    if (typeof zoom !== 'number') {
      // 'adaptive' fits via noVNC itself; a 'stretch' request that reaches
      // this branch implies a clipped viewport, which cannot be stretched.
      original.call(rfb)
      return
    }
    display.scale = zoomedCssSize(1, zoom, getPixelRatio())
    rfb._fixScrollbars()
  }
  rfb._updateScale = updateScale
  updateScale()
  return () => {
    restoreAbs()
    if (rfb._updateScale === updateScale) rfb._updateScale = original
  }
}
