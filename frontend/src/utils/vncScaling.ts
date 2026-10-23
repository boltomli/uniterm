interface VncScalingTarget {
  scaleViewport: boolean
  _display: { scale: number }
  _updateScale(): void
  _fixScrollbars(): void
}

export function installVncPixelScaling(
  rfb: VncScalingTarget,
  getPixelRatio: () => number = () => window.devicePixelRatio,
): () => void {
  const original = rfb._updateScale
  const updateScale = () => {
    if (rfb.scaleViewport) {
      original.call(rfb)
      return
    }
    const ratio = getPixelRatio()
    rfb._display.scale = 1 / (Number.isFinite(ratio) && ratio > 0 ? ratio : 1)
    rfb._fixScrollbars()
  }
  rfb._updateScale = updateScale
  updateScale()
  return () => {
    if (rfb._updateScale === updateScale) rfb._updateScale = original
  }
}
