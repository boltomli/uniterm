export function spiceDesktopGeometry(
  width: number,
  height: number,
  viewportWidth: number,
  viewportHeight: number,
  scaleViewport: boolean,
  pixelRatio: number,
) {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return null
  const ratio = Number.isFinite(pixelRatio) && pixelRatio > 0 ? pixelRatio : 1
  const canFit = Number.isFinite(viewportWidth) && Number.isFinite(viewportHeight) && viewportWidth > 0 && viewportHeight > 0
  const scale = scaleViewport && canFit ? Math.min(viewportWidth / width, viewportHeight / height, 1) : 1 / ratio
  return { scale, width: width * scale, height: height * scale }
}
