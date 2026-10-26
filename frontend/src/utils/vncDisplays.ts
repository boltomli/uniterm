import { customDesktopRegion, normalizeDesktopView } from './desktopRegions'

export interface DesktopRect {
  x: number
  y: number
  width: number
  height: number
}

export interface DesktopDisplay extends DesktopRect {
  id: number
  primary?: boolean
}

export interface DesktopView {
  selected: string
  regions?: DesktopRegion[]
  // Legacy single-region configuration; migrated when the view is loaded.
  region?: DesktopRect
}

export interface DesktopRegion extends DesktopRect {
  id: string
  name: string
}

export interface DesktopPreview {
  dataUrl: string
  width: number
  height: number
}

export function captureVncPreview(rfb: any): DesktopPreview {
  const source = rfb?._display?._backbuffer
  if (!source || source.width <= 0 || source.height <= 0) throw new Error('Desktop preview unavailable')
  const scale = Math.min(1, 1280 / source.width, 800 / source.height)
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(source.width * scale))
  canvas.height = Math.max(1, Math.round(source.height * scale))
  const context = canvas.getContext('2d')
  if (!context) throw new Error('Desktop preview unavailable')
  context.drawImage(source, 0, 0, canvas.width, canvas.height)
  return { dataUrl: canvas.toDataURL('image/png'), width: source.width, height: source.height }
}

export interface DesktopDisplayState {
  displays: DesktopDisplay[]
  width: number
  height: number
  view: DesktopView
}

export function validDesktopRect(rect: DesktopRect, width: number, height: number): boolean {
  return [rect.x, rect.y, rect.width, rect.height].every(Number.isInteger) &&
    rect.x >= 0 && rect.y >= 0 && rect.width > 0 && rect.height > 0 &&
    rect.x + rect.width <= width && rect.y + rect.height <= height
}

export function parseExtendedDisplays(bytes: Uint8Array): DesktopDisplay[] {
  if (bytes.length < 4 || bytes.length !== 4 + bytes[0]! * 16) throw new Error('Invalid display layout')
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  return Array.from({ length: bytes[0]! }, (_, i) => {
    const at = 4 + i * 16
    return {
      id: view.getUint32(at), x: view.getUint16(at + 4), y: view.getUint16(at + 6),
      width: view.getUint16(at + 8), height: view.getUint16(at + 10),
    }
  })
}

// noVNC 1.7 has no public API for display layouts or a fixed framebuffer
// viewport. Keep the private integration here, including restoration on cache
// handoff. The backbuffer stays complete, so CopyRect decoding remains valid.
export function installVncDisplays(
  rfb: any,
  initial: DesktopView,
  onChange: (state: DesktopDisplayState) => void,
) {
  const originalResize = rfb._resize
  const originalLayout = rfb._handleExtendedDesktopSize
  const originalClip = rfb._updateClip
  const originalScale = rfb._updateScale
  let displays: DesktopDisplay[] = rfb._unitermDisplays ?? []
  let view: DesktopView = normalizeDesktopView(rfb._unitermView ?? initial)
  let initialScreen = view.selected.startsWith('screen:') ? view.selected : undefined
  let disposed = false

  function selectedRect(): DesktopRect | undefined {
    if (view.selected.startsWith('region:')) return customDesktopRegion(view)
    if (view.selected.startsWith('screen:')) {
      return displays.find(d => `screen:${d.id}` === view.selected)
    }
    return undefined
  }

  function notify() {
    rfb._unitermDisplays = displays
    rfb._unitermView = view
    onChange({ displays, width: rfb._fbWidth, height: rfb._fbHeight, view })
  }

  function updateClip() {
    const rect = selectedRect()
    if (!rect || !validDesktopRect(rect, rfb._fbWidth, rfb._fbHeight)) {
      originalClip.call(rfb)
      return
    }
    const display = rfb._display
    display.clipViewport = true
    display.viewportChangeSize(rect.width, rect.height)
    const current = display._viewportLoc
    display.viewportChangePos(rect.x - current.x, rect.y - current.y)
    rfb._fixScrollbars()
  }

  function updateScale() {
    if (!selectedRect()) return originalScale.call(rfb)
    // Autoscale uses the clipped viewport. The existing native-pixel adapter
    // still handles DPR when fit-to-window is disabled.
    if (rfb.scaleViewport) {
      const size = rfb._screenSize()
      rfb._display.autoscale(size.w, size.h)
      rfb._fixScrollbars()
    } else {
      originalScale.call(rfb)
    }
  }

  function refresh() {
    const rect = selectedRect()
    if (view.selected !== 'all' && (!rect || !validDesktopRect(rect, rfb._fbWidth, rfb._fbHeight))) {
      view = { ...view, selected: 'all' }
    }
    updateClip()
    updateScale()
    notify()
  }

  const resize = function (this: any, width: number, height: number) {
    originalResize.call(this, width, height)
    // A real size change invalidates stale coordinates until the next layout.
    displays = displays.filter(d => validDesktopRect(d, width, height))
    refresh()
  }
  const layout = function (this: any) {
    const sock = this._sock
    if (sock.rQwait('display layout', 4)) return false
    const length = 4 + sock.rQpeek8() * 16
    if (sock.rQwait('display layout', length)) return false
    const bytes = sock.rQpeekBytes(length)
    const accepted = this._FBU.x !== 1 || this._FBU.y === 0
    if (accepted) {
      displays = parseExtendedDisplays(bytes).filter(d => validDesktopRect(d, this._FBU.width, this._FBU.height))
      if (initialScreen && displays.some(d => `screen:${d.id}` === initialScreen)) view = { ...view, selected: initialScreen }
      initialScreen = undefined
    }
    const result = originalLayout.call(this)
    if (result && accepted) {
      refresh()
    }
    return result
  }
  rfb._resize = resize
  rfb._handleExtendedDesktopSize = layout
  rfb._updateClip = updateClip
  rfb._updateScale = updateScale

  if (rfb._fbWidth > 0 && rfb._fbHeight > 0) refresh()
  else notify()

  return {
    select(next: DesktopView) {
      if (disposed) return
      initialScreen = undefined
      next = normalizeDesktopView(next)
      view = next
      refresh()
    },
    dispose() {
      disposed = true
      if (rfb._resize === resize) rfb._resize = originalResize
      if (rfb._handleExtendedDesktopSize === layout) rfb._handleExtendedDesktopSize = originalLayout
      if (rfb._updateClip === updateClip) rfb._updateClip = originalClip
      if (rfb._updateScale === updateScale) rfb._updateScale = originalScale
    },
  }
}
