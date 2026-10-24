<template>
  <div
    ref="container"
    class="vnc-tab-content"
    :class="{ 'is-fullscreen': isFullscreen }"
    :data-remote-keyboard="keyboardFocused && !viewOnly ? 'true' : undefined"
    @focusin="onFocusIn"
    @focusout="onFocusOut"
    @keydown.capture="onLocalKey"
    @keyup.capture="onLocalKey"
  >
    <!-- Connecting state -->
    <div v-if="status === 'connecting'" class="vnc-overlay">
      <el-icon class="is-loading" :size="'2rem'"><Loader /></el-icon>
      <p>{{ t('vnc.connecting', { host: config?.host || '...' }) }}</p>
    </div>

    <!-- Error state -->
    <div v-else-if="status === 'error'" class="vnc-overlay">
      <p class="vnc-error-text">{{ t('vnc.error') }}</p>
      <p v-if="lastError" class="vnc-error-detail">{{ lastError }}</p>
      <el-button type="primary" @click="reconnect">{{ t('vnc.retry') }}</el-button>
    </div>

    <!-- Disconnected state -->
    <div v-else-if="status === 'disconnected'" class="vnc-overlay">
      <p>{{ t('vnc.disconnected') }}</p>
      <el-button type="primary" @click="reconnect">{{ t('vnc.reconnect') }}</el-button>
    </div>

    <!-- Connected: noVNC Canvas mounts here -->
    <div
      v-show="status === 'connected'"
      ref="vncContainer"
      class="vnc-area"
      :class="{ 'is-unscaled': !scaleViewport }"
      tabindex="0"
      @paste="onPaste"
    />

    <!-- Status bar -->
    <div v-show="status === 'connected' && !isFullscreen" class="vnc-statusbar">
      <span class="vnc-status-dot" />
      <span>{{ t('vnc.connected') }}</span>
      <span class="vnc-status-sep">|</span>
      <span>{{ config?.host }}:{{ config?.port || 5900 }}</span>
      <span class="vnc-status-sep">|</span>
      <span class="vnc-status-label">{{ t('vnc.scale') }}</span>
      <el-switch
        v-model="scaleViewport"
        inline-prompt
        :active-text="t('vnc.scaleOn')"
        :inactive-text="t('vnc.scaleOff')"
        style="--el-switch-on-color: var(--success); --el-switch-off-color: var(--text-disabled)"
      />
      <span class="vnc-status-sep">|</span>
      <span class="vnc-status-label">{{ t('vnc.viewOnly') }}</span>
      <el-switch
        v-model="viewOnly"
        inline-prompt
        :active-text="t('vnc.scaleOn')"
        :inactive-text="t('vnc.scaleOff')"
        style="--el-switch-on-color: var(--success); --el-switch-off-color: var(--text-disabled)"
      />
      <span class="vnc-status-sep">|</span>
      <span class="vnc-status-label">{{ t('vnc.showDotCursor') }}</span>
      <el-switch
        v-model="showDotCursor"
        inline-prompt
        :active-text="t('vnc.scaleOn')"
        :inactive-text="t('vnc.scaleOff')"
        style="--el-switch-on-color: var(--success); --el-switch-off-color: var(--text-disabled)"
      />
      <span class="vnc-status-spacer" />
      <button class="vnc-fullscreen-btn" @click="toggleFullscreen">{{ t('remoteDesktop.fullscreen') }}</button>
    </div>
    <RemoteDesktopToolbar
      v-if="isFullscreen"
      v-model:scale-viewport="scaleViewport"
      :host="config?.host || ''"
      :port="config?.port || 5900"
      :disabled="status !== 'connected'"
      @restore="toggleFullscreen"
      @focus-desktop="focusDesktop"
      @minimize="blurDesktop"
      @error="onToolbarError"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, onActivated, onDeactivated, nextTick } from 'vue'
import { Loader } from '@lucide/vue'
import RemoteDesktopToolbar from './RemoteDesktopToolbar.vue'
import { useI18n } from '../i18n'
import { usePanelStore } from '../stores/panelStore'
import { useSessionStore } from '../stores/sessionStore'
import type { ConnectionConfig } from '../types/session'
import { Events } from '@wailsio/runtime'
import { CreateSession, CloseSession, ResolveConnectionSecret, SetVNCKeyboardCapture, EncodeVNCClipboardText } from '../../bindings/github.com/ys-ll/uniterm/app'
import { createVncClipboard } from '../utils/vncClipboard'
import { installVncPixelScaling } from '../utils/vncScaling'
import { VncNativeKeyboard } from '../utils/vncKeyboard'
import { queueRemoteKeyboardChange } from '../utils/remoteKeyboard'
import type { NativeRdpKey } from '../utils/remoteKeyboard'
import { RdpClipboardSync, RdpInputQueue } from '../utils/rdpClipboard'
import { readClipboardText, writeClipboard } from '../composables/useClipboardWrite'
const { t } = useI18n()
const panelStore = usePanelStore()
const sessionStore = useSessionStore()

const props = defineProps<{
  panelId: string
  config: ConnectionConfig | null
  sessionId: string | null
}>()

const status = ref<'connecting' | 'connected' | 'disconnected' | 'error'>('disconnected')
const lastError = ref<string>('')
const currentSessionId = ref<string | null>(props.sessionId)
const container = ref<HTMLDivElement | null>(null)
const isFullscreen = ref(false)
const vncContainer = ref<HTMLDivElement | null>(null)
const savedPassword = ref<string>('')
const scaleViewport = ref(false)
const viewOnly = ref(false)
const showDotCursor = ref(false)
const keyboardFocused = ref(false)
let keyboardCaptureId = ''
let keyboardHeartbeat: ReturnType<typeof setInterval> | null = null
let clipboardPoll: ReturnType<typeof setInterval> | null = null
let clipboardSync: RdpClipboardSync | null = null
let nativeKeyboard: VncNativeKeyboard | null = null
let restoreDesktopInput: (() => void) | null = null
let unsubKeyboard: (() => void) | null = null
const inputKeys = new Set<string>()
const sentKeys = new Map<string, number>()
const inputQueue = new RdpInputQueue(error => console.error('VNC input/clipboard error:', error))

function readThemeBackground(): string {
  // Pull the resolved --bg-base CSS variable, which the global theme
  // system (settingsStore.applyTheme) updates on `data-theme` change.
  // Returning the computed value rather than a hardcoded color keeps
  // the VNC canvas background in sync with the app's light / dark /
  // deep-blue / custom themes.
  return getComputedStyle(document.documentElement)
    .getPropertyValue('--bg-base')
    .trim() || '#000000'
}

let themeObserver: MutationObserver | null = null
function watchThemeBackground() {
  if (themeObserver) return
  themeObserver = new MutationObserver(() => {
    if (rfb) rfb.background = readThemeBackground()
  })
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['data-theme'],
  })
}

let rfb: any = null
let unsubStatus: (() => void) | null = null
let isIniting = false
let restorePixelScaling: (() => void) | null = null
let pixelRatioQuery: MediaQueryList | null = null

function onPixelRatioChange() {
  pixelRatioQuery?.removeEventListener('change', onPixelRatioChange)
  pixelRatioQuery = window.matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`)
  pixelRatioQuery.addEventListener('change', onPixelRatioChange)
  rfb?._updateScale()
}

// LoadConnections 脱敏后渲染层不再持有连接密码；noVNC 的 RFB 握手需要明文，
// 按连接 ID 向后端解析（config.password 仍覆盖凭据弹窗内联输入的场景）。
async function resolvePassword(): Promise<string> {
  if (!props.config) return ''
  if (props.config.password) return props.config.password
  try {
    const secret = await ResolveConnectionSecret(props.config.id) as { password?: string }
    return secret.password || ''
  } catch {
    return ''
  }
}

async function connect() {
  if (!props.config) return
  if (status.value === 'connecting' || status.value === 'connected') return
  status.value = 'connecting'
  lastError.value = ''
  try {
    const info = await CreateSession('vnc', props.config)
    currentSessionId.value = info.id
    // Bind the session to the panel so tab-close cleanup and later
    // session:status events resolve to this id. The proxy address comes
    // straight from the CreateSession result: the synchronous VNC connect
    // makes it available here, independent of the racy session:status
    // 'connected' event that used to be dropped before the session id was
    // set, leaving VNC stuck on "connecting".
    panelStore.bindSession(props.panelId, info.id)
    sessionStore.initSession(info.id)
    if (info.proxyAddr) {
      panelStore.setProxyAddr(props.panelId, info.proxyAddr)
      savedPassword.value = await resolvePassword()
      initRFB(info.proxyAddr, savedPassword.value)
    }
  } catch (e: any) {
    console.error('VNC connect error:', e)
    lastError.value = e?.message || String(e)
    status.value = 'error'
  }
}

async function reconnect() {
  onFocusOut()
  restoreDesktopInput?.()
  restoreDesktopInput = null
  restorePixelScaling?.()
  restorePixelScaling = null
  if (currentSessionId.value) {
    try { await CloseSession(currentSessionId.value) } catch (_) {}
    currentSessionId.value = null
  }
  if (rfb) {
    rfb.disconnect()
    rfb = null
  }
  await connect()
}

function initRFB(proxyAddr: string, password: string) {
  if (isIniting) return
  isIniting = true
  onFocusOut()
  restoreDesktopInput?.()
  restoreDesktopInput = null
  restorePixelScaling?.()
  restorePixelScaling = null

  if (rfb) {
    try { rfb.disconnect() } catch (_) {}
    rfb = null
  }
  if (vncContainer.value) {
    vncContainer.value.innerHTML = ''
  }

  const RFB = (window as any).__novnc_RFB
  if (RFB) {
    createRFB(RFB, proxyAddr, password)
    return
  }

  import('@novnc/novnc').then((module: any) => {
    const LoadedRFB = module.default || module
    ;(window as any).__novnc_RFB = LoadedRFB
    createRFB(LoadedRFB, proxyAddr, password)
  }).catch((e: any) => {
    console.error('Failed to load noVNC module:', e)
    lastError.value = e?.message || String(e)
    status.value = 'error'
    isIniting = false
  })
}

function applyRFBOptions() {
  if (!rfb) return
  restorePixelScaling?.()
  restorePixelScaling = installVncPixelScaling(rfb)
  rfb.viewOnly = viewOnly.value
  rfb.scaleViewport = scaleViewport.value
  rfb.showDotCursor = showDotCursor.value
  rfb.background = readThemeBackground()
  setupDesktopInput()
}

function ownsRemoteKeyboard(): boolean {
  return status.value === 'connected' && !viewOnly.value && document.hasFocus() &&
    document.activeElement === vncContainer.value?.querySelector('canvas')
}

function onLocalKey(event: KeyboardEvent) {
  if (event.code === 'F12') event.stopPropagation()
}

function syncLocalClipboard(text?: string): Promise<void> {
  return ownsRemoteKeyboard() ? clipboardSync?.sync(false, text) ?? Promise.resolve() : Promise.resolve()
}

function queueVncKey(keysym: number, code: string, down: boolean, send: () => void) {
  const repeat = inputKeys.has(code)
  const paste = down && !repeat && ((code === 'KeyV' &&
    ['ControlLeft', 'ControlRight', 'MetaLeft', 'MetaRight'].some(key => inputKeys.has(key))) ||
    (code === 'Insert' && ['ShiftLeft', 'ShiftRight'].some(key => inputKeys.has(key))))
  if (down) inputKeys.add(code)
  else inputKeys.delete(code)
  if (paste) inputQueue.enqueue(() => syncLocalClipboard())
  inputQueue.enqueue(() => {
    send()
    if (down) sentKeys.set(code, keysym)
    else sentKeys.delete(code)
  })
}

function setupDesktopInput() {
  restoreDesktopInput?.()
  const target = rfb
  const clipboard = createVncClipboard(target, props.config?.vncClipboardEncoding, EncodeVNCClipboardText,
    () => ownsRemoteKeyboard() && rfb === target)
  const sync = new RdpClipboardSync({
    read: async () => {
      const text = await readClipboardText()
      return ownsRemoteKeyboard() ? { kind: 'text', text } : null
    },
    publish: async content => {
      if (content.kind === 'text') await clipboard.publish(content.text)
    },
    writeText: writeClipboard,
  }, true)
  clipboardSync = sync
  nativeKeyboard = new VncNativeKeyboard((keysym, code, down) => {
    queueVncKey(keysym, code, down, () => target.sendKey(keysym, code, down))
  })
  const keyboard = target._keyboard
  const original = keyboard.onkeyevent
  const handler = (keysym: number, code: string, down: boolean, numlock: boolean, capslock: boolean) => {
    queueVncKey(keysym, code, down, () => original(keysym, code, down, numlock, capslock))
  }
  keyboard.onkeyevent = handler
  const onClipboard = (event: any) => {
    try {
      sync.receiveText(clipboard.decode(event.detail.text)).catch(error => console.error('VNC clipboard write error:', error))
    } catch (error) {
      console.error('VNC clipboard decode error:', error)
    }
  }
  target.addEventListener('clipboard', onClipboard)
  restoreDesktopInput = () => {
    sync.dispose()
    clipboard.dispose()
    target.removeEventListener('clipboard', onClipboard)
    if (keyboard.onkeyevent === handler) keyboard.onkeyevent = original
    inputQueue.clear()
    nativeKeyboard = null
    clipboardSync = null
    inputKeys.clear()
    sentKeys.clear()
  }
}

function onFocusIn() {
  if (!ownsRemoteKeyboard()) return
  keyboardFocused.value = true
  syncLocalClipboard().catch(error => console.error('VNC clipboard sync error:', error))
  if (keyboardCaptureId || !currentSessionId.value) return
  const captureID = crypto.randomUUID()
  const sessionID = currentSessionId.value
  keyboardCaptureId = captureID
  queueRemoteKeyboardChange(() => SetVNCKeyboardCapture(sessionID, captureID, true, isFullscreen.value))
    .then(enabled => {
      if (!enabled || keyboardCaptureId !== captureID) return
      let pending = false
      keyboardHeartbeat = setInterval(() => {
        if (!ownsRemoteKeyboard()) { onFocusOut(); return }
        if (pending) return
        pending = true
        queueRemoteKeyboardChange(() => SetVNCKeyboardCapture(sessionID, captureID, true, isFullscreen.value))
          .catch(error => {
            console.error('VNC keyboard capture error:', error)
            if (keyboardCaptureId === captureID) onFocusOut()
          })
          .finally(() => { pending = false })
      }, 1000)
    })
    .catch(error => console.error('VNC keyboard capture error:', error))
}

function onFocusOut() {
  if (keyboardHeartbeat !== null) clearInterval(keyboardHeartbeat)
  keyboardHeartbeat = null
  keyboardFocused.value = false
  const captureID = keyboardCaptureId
  keyboardCaptureId = ''
  if (captureID) {
    const sessionID = currentSessionId.value || ''
    queueRemoteKeyboardChange(() => SetVNCKeyboardCapture(sessionID, captureID, false))
      .catch(error => console.error('VNC keyboard release error:', error))
  }
  inputQueue.clear()
  nativeKeyboard?.release()
  for (const [code, keysym] of Array.from(sentKeys).reverse()) rfb?.sendKey(keysym, code, false)
  sentKeys.clear()
  inputKeys.clear()
}

function onNativeKey(data: NativeRdpKey) {
  if (data.captureID !== keyboardCaptureId || !ownsRemoteKeyboard()) return
  if (data.reset) { onFocusOut(); onFocusIn(); return }
  nativeKeyboard?.key(data)
}

function onVisibilityChange() {
  if (document.hidden) onFocusOut()
  else onFocusIn()
}

function createRFB(RFB: any, proxyAddr: string, password: string) {
  if (!vncContainer.value || vncContainer.value.childElementCount > 0) {
    isIniting = false
    return
  }

  try {
    rfb = new RFB(vncContainer.value, proxyAddr, {
      credentials: { password: password || '' },
      shared: props.config?.vncShared ?? true,
      repeaterID: props.config?.vncRepeaterID || '',
    })
  } catch (e: any) {
    console.error('Failed to create RFB instance:', e)
    lastError.value = e?.message || String(e)
    status.value = 'error'
    isIniting = false
    return
  }

  // Force RFB 3.3 negotiation. RealVNC Server advertises a private
  // "RFB 005.000" banner which noVNC maps to 3.8; in 3.8 the server then
  // offers a security-type list ([6,130,2,...]) and noVNC picks RA2ne (6)
  // ahead of VNC Auth (2), failing with a 1006 close. Cap the max version to
  // 3.3 so noVNC replies "RFB 003.003" and the server falls back to legacy
  // single-security-type negotiation, sending VNC Auth (2) directly.
  rfb._rfbMaxVersion = 3.3

  applyRFBOptions()

  bindRFBHandlers(rfb)

  isIniting = false
}

// Named RFB event handlers so they can be re-bound: the cache-restore path
// hands this component the previous instance's rfb, whose listeners still
// point at that dead instance's refs. The bound pairs are recorded on the rfb
// itself (`__unitermHandlers`) so the next binder can drop them by reference
// — handlers never stack and the restored tab reacts to
// connect/securityfailure/credentials/disconnect again (security.md #14).
function onRfbConnect() {
  // issue #95: only flip to 'connected' on the noVNC connect event.
  // Previously status='connected' was set as soon as the local proxy was
  // up, well before the RFB handshake completed, producing a black
  // screen with no error on security failure or wrong password.
  lastError.value = ''
  status.value = 'connected'
}

function onRfbSecurityFailure(e: any) {
  const reason = e.detail?.reason ?? e.detail?.status
  lastError.value = t('vnc.errorSecurity', { reason: String(reason ?? '') })
  status.value = 'error'
}

function onRfbCredentialsRequired(e: any) {
  const types = (e.detail?.types || []).join(', ')
  lastError.value = t('vnc.errorCredentials', { types })
  status.value = 'error'
}

function onRfbDisconnect(e: any) {
  if (!e.detail.clean) {
    if (!lastError.value) lastError.value = t('vnc.errorClosed')
    status.value = 'error'
  } else {
    status.value = 'disconnected'
  }
}

function bindRFBHandlers(target: any) {
  const stale: Array<[string, EventListener]> = target.__unitermHandlers ?? []
  for (const [type, fn] of stale) target.removeEventListener(type, fn)
  const pairs: Array<[string, EventListener]> = [
    ['connect', onRfbConnect as EventListener],
    ['securityfailure', onRfbSecurityFailure as EventListener],
    ['credentialsrequired', onRfbCredentialsRequired as EventListener],
    ['disconnect', onRfbDisconnect as EventListener],
  ]
  for (const [type, fn] of pairs) target.addEventListener(type, fn)
  target.__unitermHandlers = pairs
}

function onPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text')
  if (!text || !rfb || !ownsRemoteKeyboard()) return
  e.preventDefault()
  const target = rfb
  inputQueue.enqueue(() => syncLocalClipboard(text))
  inputQueue.enqueue(() => {
    target.sendKey(0xffe3, 'ControlLeft', true)
    target.sendKey(0x76, 'KeyV', true)
    target.sendKey(0x76, 'KeyV', false)
    target.sendKey(0xffe3, 'ControlLeft', false)
  })
}

function onReconnectEvent(e: Event) {
  const panelId = (e as CustomEvent)?.detail?.panelId
  if (panelId && panelId === props.panelId) reconnect()
}

function focusDesktop() {
  if (status.value === 'connected') rfb?.focus({ preventScroll: true })
}

function blurDesktop() {
  onFocusOut()
  rfb?.blur()
}

function onToolbarError(error: unknown) {
  console.error('VNC minimize error:', error)
}

// The frameless WebView2 window runs in composition mode: the OS cursor is
// only updated when the web content's cursor *changes* (CursorChanged →
// Wails SetCursor). Entering fullscreen rewrites the window style, which
// drops the active cursor; VNC servers only send cursor shapes when the
// remote shape changes, so unlike RDP nothing re-fires it afterwards and the
// cursor stays invisible until this replay nudges WebView2 to re-emit it.
function replayCanvasCursor() {
  const canvas = vncContainer.value?.querySelector('canvas')
  if (!canvas) return
  const cursor = canvas.style.cursor
  if (!cursor) return
  canvas.style.cursor = 'default'
  requestAnimationFrame(() => { canvas.style.cursor = cursor })
}

function onFullscreenChange() {
  isFullscreen.value = document.fullscreenElement === container.value
  if (keyboardCaptureId && currentSessionId.value) {
    // Apply the new fullscreen flag immediately instead of waiting for the
    // next capture heartbeat, so Escape is forwarded right away.
    const captureID = keyboardCaptureId
    const sessionID = currentSessionId.value
    queueRemoteKeyboardChange(() => SetVNCKeyboardCapture(sessionID, captureID, true, isFullscreen.value))
      .catch(error => console.error('VNC keyboard capture error:', error))
  }
  // Replay once on the next frame and once after the window-style swap has
  // settled; both are idempotent.
  requestAnimationFrame(replayCanvasCursor)
  setTimeout(replayCanvasCursor, 300)
}

async function toggleFullscreen() {
  const el = container.value
  if (!el) return
  try {
    if (document.fullscreenElement === el) await document.exitFullscreen()
    else await el.requestFullscreen()
    focusDesktop()
  } catch (error) {
    console.error('VNC fullscreen error:', error)
  }
}

function onFullscreenEvent(event: Event) {
  if ((event as CustomEvent<{ panelId: string }>).detail?.panelId === props.panelId) void toggleFullscreen()
}

onMounted(() => {
  unsubKeyboard = Events.On('remote-desktop:keyboard', event => onNativeKey(event.data as NativeRdpKey))
  window.addEventListener('blur', onFocusOut)
  window.addEventListener('focus', onFocusIn)
  document.addEventListener('visibilitychange', onVisibilityChange)
  clipboardPoll = setInterval(() => {
    syncLocalClipboard().catch(error => console.error('VNC clipboard sync error:', error))
  }, 1000)
  onPixelRatioChange()
  document.addEventListener('fullscreenchange', onFullscreenChange)
  window.addEventListener('vnc:fullscreen', onFullscreenEvent)
  if (props.sessionId) {
    currentSessionId.value = props.sessionId
  }

  // Register event listeners BEFORE the cache-restore branch: a restored tab
  // must keep the same wiring as a fresh one — session:status (error/
  // disconnect handling), the tab right-click 重连 menu (security.md #14:
  // restore used to return early, so disconnects showed no overlay and
  // Reconnect did nothing).
  //
  // session:status registers BEFORE connect() too — with synchronous VNC
  // connect the event is emitted during CreateSession and would arrive before
  // a later listener.
  unsubStatus = Events.On('session:status', (ev) => { const data: any = ev.data; 
    if (data.id !== currentSessionId.value) return
    switch (data.status) {
      case 'connected':
        if (data.proxyAddr) {
          panelStore.setProxyAddr(props.panelId, data.proxyAddr)
        }
        if (props.config && data.proxyAddr) {
          void resolvePassword().then((password) => {
            savedPassword.value = password
            initRFB(data.proxyAddr, password)
          })
        } else {
          const proxy = panelStore.getProxyAddr(props.panelId)
          if (proxy) {
            initRFB(proxy, savedPassword.value)
          }
        }
        break
      case 'disconnected':
        if (status.value !== 'error') status.value = 'disconnected'
        break
      case 'error':
        if (!lastError.value) lastError.value = t('vnc.errorClosed')
        status.value = 'error'
        break
    }
  })

  // Tab right-click 「重连」(Reconnect) menu → forced reconnect of this panel.
  window.addEventListener('panel:reconnect', onReconnectEvent)

  const cached = panelStore.getVNCCache(props.panelId)
  if (cached && vncContainer.value) {
    const children = Array.from(cached.container.children)
    children.forEach(child => vncContainer.value!.appendChild(child))
    rfb = cached.rfb
    panelStore.removeVNCCache(props.panelId)
    status.value = 'connected'
    applyRFBOptions()
    // Rebind the handlers: the cached rfb still carries the previous
    // component instance's listeners, which write to that dead instance's
    // refs (security.md #14).
    bindRFBHandlers(rfb)
    watchThemeBackground()
    return
  }

  const storedProxy = panelStore.getProxyAddr(props.panelId)
  if (storedProxy && props.config) {
    void resolvePassword().then((password) => {
      savedPassword.value = password
      initRFB(storedProxy, password)
    })
  } else {
    connect()
  }

  watchThemeBackground()
})

onBeforeUnmount(() => {
  onFocusOut()
  restoreDesktopInput?.()
  restoreDesktopInput = null
  unsubKeyboard?.()
  if (clipboardPoll !== null) clearInterval(clipboardPoll)
  clipboardPoll = null
  window.removeEventListener('blur', onFocusOut)
  window.removeEventListener('focus', onFocusIn)
  document.removeEventListener('visibilitychange', onVisibilityChange)
  pixelRatioQuery?.removeEventListener('change', onPixelRatioChange)
  pixelRatioQuery = null
  restorePixelScaling?.()
  restorePixelScaling = null
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  window.removeEventListener('vnc:fullscreen', onFullscreenEvent)
  themeObserver?.disconnect()
  themeObserver = null
  unsubStatus?.()
  window.removeEventListener('panel:reconnect', onReconnectEvent)

  if (rfb && vncContainer.value && vncContainer.value.childElementCount > 0) {
    const container = document.createElement('div')
    container.style.display = 'none'
    const children = Array.from(vncContainer.value.children)
    children.forEach(child => container.appendChild(child))
    document.body.appendChild(container)
    panelStore.setVNCCache(props.panelId, { rfb, container })
  } else if (rfb) {
    rfb.disconnect()
    rfb = null
  }
})

watch(() => props.sessionId, (newId) => {
  if (newId && !currentSessionId.value) {
    currentSessionId.value = newId
  }
})

watch(scaleViewport, (val) => {
  if (rfb) rfb.scaleViewport = val
})
watch(viewOnly, (val) => {
  if (val) onFocusOut()
  if (rfb) rfb.viewOnly = val
  if (!val) onFocusIn()
})
watch(showDotCursor, (val) => {
  if (rfb) rfb.showDotCursor = val
})
watch(status, value => value === 'connected' ? nextTick(onFocusIn) : onFocusOut())
onDeactivated(onFocusOut)
onActivated(onFocusIn)
</script>

<style scoped>
.vnc-tab-content {
  position: relative;
  width: 100%;
  height: 100%;
  background: #000;
}
.vnc-area {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 1.5rem;
  background: #000;
  outline: none;
  overflow: auto;
}
.vnc-area :deep(canvas) {
  display: block;
  flex-shrink: 0;
}
.vnc-area.is-unscaled :deep(canvas) {
  image-rendering: pixelated;
}
.vnc-tab-content.is-fullscreen .vnc-area,
.vnc-tab-content.is-fullscreen .vnc-overlay {
  bottom: 0;
}
.vnc-overlay {
  position: absolute;
  inset: 0;
  bottom: 1.5rem;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.75rem;
  color: var(--text-muted);
  z-index: 10;
}
.vnc-error-text { color: var(--error); }
.vnc-error-detail {
  color: var(--text-muted);
  font-size: 0.75rem;
  max-width: 80%;
  text-align: center;
  word-break: break-word;
}
.vnc-statusbar {
  position: absolute;
  bottom: 0;
  left: 0;
  right: 0;
  height: 1.5rem;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0 0.75rem;
  background: var(--bg-elevated);
  color: var(--text-muted);
  font-size: 0.75rem;
  box-sizing: border-box;
  z-index: 5;
}
.vnc-status-dot {
  width: 0.5rem; height: 0.5rem;
  border-radius: 50%;
  background: var(--success);
  flex-shrink: 0;
}
.vnc-status-sep { color: var(--text-disabled); }
.vnc-status-label { font-size: 0.6875rem; }
.vnc-status-spacer { flex: 1; }
.vnc-fullscreen-btn {
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--text-muted);
  font-size: 0.75rem;
  padding: 1px 0.625rem;
  border-radius: var(--radius-sm);
}
.vnc-fullscreen-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}
</style>
