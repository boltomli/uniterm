interface VncCursorTarget {
  _canvas: HTMLCanvasElement
  _cursor: {
    _canvas: HTMLCanvasElement
    _target: HTMLCanvasElement | null
    _hotSpot: { x: number; y: number }
    change(...args: any[]): void
    clear(): void
    detach(): void
    move(clientX: number, clientY: number): void
  }
}

// WebView2 can lose custom OS cursors during fullscreen transitions. noVNC's
// software cursor also lives under body, outside the fullscreen top layer.
// Paint the current noVNC cursor inside the fullscreen element in both cases.
export function installVncCursor(rfb: VncCursorTarget, fullscreenRoot: HTMLElement): () => void {
  const target = rfb._canvas
  const cursor = rfb._cursor
  const overlay = document.createElement('canvas')
  overlay.setAttribute('aria-hidden', 'true')
  Object.assign(overlay.style, {
    position: 'fixed', pointerEvents: 'none', zIndex: '65535',
    userSelect: 'none', WebkitUserSelect: 'none', visibility: 'hidden',
  })
  fullscreenRoot.appendChild(overlay)

  let active = false
  let nativeCursor = target.style.cursor
  let position: { x: number; y: number } | null = null

  function sync() {
    const fullscreen = document.fullscreenElement === fullscreenRoot && cursor._target === target
    if (!fullscreen) {
      if (active) target.style.cursor = nativeCursor
      active = false
      overlay.style.visibility = 'hidden'
      return
    }
    if (!active) nativeCursor = target.style.cursor
    active = true
    target.style.cursor = 'none'
    const visible = position && overlay.width > 0 && overlay.height > 0 &&
      document.hasFocus() && document.elementFromPoint(position.x, position.y) === target
    overlay.style.visibility = visible ? 'visible' : 'hidden'
    if (position) {
      overlay.style.left = `${position.x - cursor._hotSpot.x}px`
      overlay.style.top = `${position.y - cursor._hotSpot.y}px`
    }
  }

  function updateImage() {
    nativeCursor = target.style.cursor
    overlay.width = cursor._canvas.width
    overlay.height = cursor._canvas.height
    if (overlay.width && overlay.height) overlay.getContext('2d')?.drawImage(cursor._canvas, 0, 0)
    sync()
  }

  const originalChange = cursor.change
  const originalClear = cursor.clear
  const originalDetach = cursor.detach
  const originalMove = cursor.move
  const change = function (this: typeof cursor, ...args: any[]) {
    originalChange.apply(this, args)
    updateImage()
  }
  const clear = function (this: typeof cursor) {
    originalClear.call(this)
    updateImage()
  }
  const detach = function (this: typeof cursor) {
    originalDetach.call(this)
    sync()
  }
  const move = function (this: typeof cursor, clientX: number, clientY: number) {
    originalMove.call(this, clientX, clientY)
    position = { x: clientX, y: clientY }
    sync()
  }
  cursor.change = change
  cursor.clear = clear
  cursor.detach = detach
  cursor.move = move

  function onMove(event: MouseEvent) {
    position = { x: event.clientX, y: event.clientY }
    sync()
  }
  function onLeave(event: MouseEvent) {
    if (!event.relatedTarget) {
      position = null
      sync()
    }
  }
  document.addEventListener('fullscreenchange', sync)
  document.addEventListener('scroll', sync, true)
  window.addEventListener('mousemove', onMove, { passive: true })
  window.addEventListener('mouseout', onLeave, { passive: true })
  window.addEventListener('blur', sync)
  window.addEventListener('focus', sync)
  updateImage()

  return () => {
    document.removeEventListener('fullscreenchange', sync)
    document.removeEventListener('scroll', sync, true)
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseout', onLeave)
    window.removeEventListener('blur', sync)
    window.removeEventListener('focus', sync)
    if (cursor.change === change) cursor.change = originalChange
    if (cursor.clear === clear) cursor.clear = originalClear
    if (cursor.detach === detach) cursor.detach = originalDetach
    if (cursor.move === move) cursor.move = originalMove
    if (active) target.style.cursor = nativeCursor
    overlay.remove()
  }
}
