import { afterEach, describe, expect, it, vi } from 'vitest'
import { installVncCursor } from './vncCursor'

class Element extends EventTarget {
  style: Record<string, string> = {}
  children: Element[] = []
  parentNode: Element | null = null
  width = 300
  height = 150
  context = { clearRect: vi.fn(), putImageData: vi.fn(), drawImage: vi.fn() }
  getContext() { return this.context }
  setAttribute() {}
  toDataURL() { return `data:image/png;${this.width}x${this.height}` }
  appendChild(child: Element) { child.remove(); this.children.push(child); child.parentNode = this }
  removeChild(child: Element) { child.remove() }
  remove() {
    if (this.parentNode) this.parentNode.children = this.parentNode.children.filter(child => child !== this)
    this.parentNode = null
  }
  contains(target: Element): boolean { return target === this || this.children.some(child => child.contains(target)) }
}

async function desktop(attached = true) {
  const body = new Element(), root = new Element(), target = new Element()
  body.appendChild(root); root.appendChild(target)
  let focused = true, hovered: Element | null = target
  const doc = Object.assign(new EventTarget(), {
    body, documentElement: new Element(), fullscreenElement: null as Element | null,
    createElement: () => new Element(),
    contains: (element: Element) => body.contains(element),
    hasFocus: () => focused, elementFromPoint: () => hovered,
  })
  const win = new EventTarget()
  vi.stubGlobal('document', doc)
  vi.stubGlobal('window', win)
  vi.stubGlobal('navigator', { maxTouchPoints: 0 })
  vi.stubGlobal('ImageData', class {})
  vi.resetModules()
  const Cursor = (await import('@novnc-internals/util/cursor.js')).default
  const cursor = new Cursor()
  if (attached) cursor.attach(target)
  const stop = installVncCursor({ _canvas: target, _cursor: cursor } as any, root as any)
  const overlay = root.children[1]!
  function move(x = 123, y = 97) {
    win.dispatchEvent(Object.assign(new Event('mousemove'), { clientX: x, clientY: y }))
  }
  function fullscreen(enabled: boolean) {
    doc.fullscreenElement = enabled ? root : null
    doc.dispatchEvent(new Event('fullscreenchange'))
  }
  function image(width = 8, height = 9, hotx = 2, hoty = 3) {
    cursor.change(new Uint8Array(width * height * 4).fill(255), hotx, hoty, width, height)
  }
  return { root, target, cursor, overlay, stop, move, fullscreen, image,
    hover: (element: Element | null) => { hovered = element; move() },
    focus: (value: boolean) => { focused = value; win.dispatchEvent(new Event(value ? 'focus' : 'blur')) },
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('VNC fullscreen cursor using the noVNC cursor renderer', () => {
  it('paints the remote cursor inside fullscreen with its hotspot and follows shape changes', async () => {
    const d = await desktop()
    d.image(); d.move(); d.fullscreen(true)
    expect(d.overlay.parentNode).toBe(d.root)
    expect(d.overlay.style.visibility).toBe('visible')
    expect(d.overlay.style.left).toBe('121px')
    expect(d.overlay.style.top).toBe('94px')
    d.cursor.move(200, 150)
    expect(d.overlay.style.left).toBe('198px')
    expect(d.overlay.style.top).toBe('147px')
    d.move()
    expect(d.target.style.cursor).toBe('none')
    expect(d.overlay.context.drawImage).toHaveBeenLastCalledWith(d.cursor._canvas, 0, 0)
    d.image(16, 18, 4, 5)
    expect([d.overlay.width, d.overlay.height]).toEqual([16, 18])
    expect(d.overlay.style.left).toBe('119px')
    expect(d.overlay.style.top).toBe('92px')
    d.fullscreen(false)
    expect(d.overlay.style.visibility).toBe('hidden')
    expect(d.target.style.cursor).toBe('url(data:image/png;16x18)4 5, default')
    d.stop()
  })

  it('hides over toolbars, on blur and for an empty cursor, then restores the current image', async () => {
    const d = await desktop()
    d.image(); d.move(); d.fullscreen(true)
    d.hover(new Element())
    expect(d.overlay.style.visibility).toBe('hidden')
    d.hover(d.target)
    expect(d.overlay.style.visibility).toBe('visible')
    d.focus(false)
    expect(d.overlay.style.visibility).toBe('hidden')
    d.focus(true)
    expect(d.overlay.style.visibility).toBe('visible')
    d.cursor.clear()
    expect(d.overlay.style.visibility).toBe('hidden')
    d.image(3, 3, 1, 1)
    expect(d.overlay.style.visibility).toBe('visible')
    d.stop()
  })

  it('handles delayed cursor attachment, disconnects and disposal while fullscreen', async () => {
    const d = await desktop(false)
    d.move(); d.fullscreen(true)
    expect(d.overlay.style.visibility).toBe('hidden')
    d.cursor.attach(d.target); d.image()
    expect(d.overlay.style.visibility).toBe('visible')
    d.cursor.detach()
    expect(d.overlay.style.visibility).toBe('hidden')
    d.cursor.attach(d.target); d.image()
    expect(d.overlay.style.visibility).toBe('visible')
    d.stop()
    expect(d.overlay.parentNode).toBeNull()
    expect(d.target.style.cursor).toBe('url(data:image/png;8x9)2 3, default')
    d.image(5, 5)
    expect(d.overlay.width).toBe(8)
  })
})
