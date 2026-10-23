// Global single-open menu coordinator.
//
// This must live in a REAL module (module scope, shared across every <Menu>).
// The previous copy of this state sat in Menu.vue's <script setup>, where
// `active` / `docBound` are per INSTANCE and the window listener was added once
// per mounted menu and never removed — every closed tab with menus left
// listeners holding the dead instance's state (security.md component chapter
// #12).
//
// The coordinator binds its shared dismiss listeners once, on the first
// retained (mounted) menu, and removes them when the last one releases.

import { releaseRemoteKeyboardFocus } from '../utils/remoteKeyboard'

/** A mounted menu the coordinator can locate and close. */
export interface ActiveMenu {
  el: () => HTMLElement | null
  close: () => void
}

/** Window / document subset the coordinator needs (injectable for tests). */
export interface ListenerTarget {
  addEventListener(type: string, listener: (e: Event) => void): void
  removeEventListener(type: string, listener: (e: Event) => void): void
  dispatchEvent(event: Event): unknown
}

export interface MenuCoordinator {
  /** A Menu mounted: bind the shared listeners on the first one. */
  retain(menu: ActiveMenu): void
  /** A Menu unmounted: closes it if it is still the active one, then drops
   *  the listener reference (last release unbinds). */
  release(menu: ActiveMenu): void
  activate(menu: ActiveMenu): void
  deactivate(menu: ActiveMenu): void
  isActive(menu: ActiveMenu): boolean
  listenersBound(): boolean
}

export function createMenuCoordinator(win: ListenerTarget, doc: ListenerTarget): MenuCoordinator {
  let active: ActiveMenu | null = null
  let retained = 0
  let bound = false

  // Closing the active menu when any surface broadcasts the legacy
  // global:close-context-menus event (opening a dialog, keyboard nav,
  // main.ts dispatches it on every contextmenu). Single-open +
  // this event is the whole close story — hosts never re-subscribe.
  const onGlobalClose = () => {
    active?.close()
  }

  // Bubble-phase outside-click close. Trigger buttons use @click.stop so their
  // own click only runs toggle(); anything that bubbles past the open menu
  // closes it. Clicks inside a teleported flyout (MenuSubmenu) live outside
  // the menu root in the DOM but must count as inside.
  const onDocClick = (e: Event) => {
    if (!active) return
    const target = e.target as HTMLElement | null
    const el = active.el()
    if (el && target && !el.contains(target) && !target.closest('.menu-submenu')) active.close()
  }
  const onDocKeydown = (e: Event) => {
    if ((e as KeyboardEvent).key === 'Escape') active?.close()
  }

  function bindListeners() {
    if (bound) return
    bound = true
    win.addEventListener('global:close-context-menus', onGlobalClose)
    doc.addEventListener('click', onDocClick)
    doc.addEventListener('keydown', onDocKeydown)
  }

  function unbindListeners() {
    if (!bound) return
    bound = false
    win.removeEventListener('global:close-context-menus', onGlobalClose)
    doc.removeEventListener('click', onDocClick)
    doc.removeEventListener('keydown', onDocKeydown)
  }

  // The RDP ActiveX overlay push/pop (rdp:overlay-push/pop) is gone with the
  // IronRDP WASM client: the desktop renders inside the webview, so an open
  // menu is never occluded by a native window.

  return {
    retain() {
      retained += 1
      if (retained === 1) bindListeners()
    },

    release(menu) {
      if (active === menu) active = null
      retained = Math.max(0, retained - 1)
      if (retained === 0) unbindListeners()
    },

    activate(menu) {
      // Release the remote-desktop keyboard grab first so menu keys are not
      // swallowed by the native capture path.
      releaseRemoteKeyboardFocus()
      // Right-click menus close each other because main.ts dispatches the
      // global close signal on every `contextmenu` (capture) before the new
      // menu opens. Broadcasting here covers button-triggered opens too.
      // Broadcast BEFORE switching `active` so the signal closes the
      // previous menu, not this one.
      //
      // Re-activating the SAME menu (open()/openAt() emit update:visible true
      // and the visible-prop watcher ALSO calls activate) is a no-op.
      if (active === menu) return
      win.dispatchEvent(new Event('global:close-context-menus'))
      active?.close()
      active = menu
    },

    deactivate(menu) {
      if (active === menu) active = null
    },

    isActive: (menu) => active === menu,
    listenersBound: () => bound,
  }
}

let singleton: MenuCoordinator | null = null

/** App-wide coordinator instance (bound to the real window/document). */
export function getMenuCoordinator(): MenuCoordinator {
  if (!singleton) singleton = createMenuCoordinator(window, document)
  return singleton
}
