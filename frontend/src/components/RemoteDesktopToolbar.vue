<template>
  <div
    class="remote-desktop-fullscreen-edge"
    aria-hidden="true"
    @pointerenter="showFullscreenToolbar"
    @pointerleave="scheduleFullscreenToolbarHide"
    @pointerdown.prevent="showFullscreenToolbar"
  />
  <div
    ref="fullscreenToolbar"
    class="remote-desktop-fullscreen-toolbar"
    :class="{ 'is-hidden': !toolbarPinned && !toolbarExpanded }"
    role="toolbar"
    :aria-label="t('remoteDesktop.fullscreenToolbar')"
    @pointerenter="showFullscreenToolbar"
    @pointerleave="scheduleFullscreenToolbarHide"
    @focusin="showFullscreenToolbar"
    @focusout="scheduleFullscreenToolbarHide"
    @contextmenu.prevent
  >
    <button
      class="remote-desktop-toolbar-btn remote-desktop-toolbar-pin"
      :class="{ 'is-active': toolbarPinned }"
      :aria-pressed="toolbarPinned"
      :aria-label="t(toolbarPinned ? 'remoteDesktop.unpinToolbar' : 'remoteDesktop.pinToolbar')"
      :title="t(toolbarPinned ? 'remoteDesktop.unpinToolbar' : 'remoteDesktop.pinToolbar')"
      @click="toggleToolbarPin"
    >
      <Pin v-if="toolbarPinned" :size="16" />
      <PinOff v-else :size="16" />
    </button>
    <span class="remote-desktop-toolbar-host" :title="`${host}:${port}`">
      {{ host }}:{{ port }}
    </span>
    <span class="remote-desktop-toolbar-divider" />
    <template v-if="resolution">
      <span class="remote-desktop-toolbar-resolution" :title="`${t('remoteDesktop.resolution')}: ${resolution}`">
        {{ resolution }}
      </span>
      <span class="remote-desktop-toolbar-divider" />
    </template>
    <label class="remote-desktop-toolbar-scale">
      <span>{{ t('remoteDesktop.scale') }}</span>
      <el-switch
        :model-value="scaleViewport"
        @update:model-value="emit('update:scaleViewport', $event)"
        inline-prompt
        :active-text="t('vnc.scaleOn')"
        :inactive-text="t('vnc.scaleOff')"
        :aria-label="t('remoteDesktop.scale')"
        :disabled="disabled"
        style="--el-switch-on-color: var(--success); --el-switch-off-color: var(--text-disabled)"
        @change="focusDesktop"
      />
    </label>
    <span class="remote-desktop-toolbar-divider" />
    <button
      class="remote-desktop-toolbar-btn remote-desktop-toolbar-minimize"
      :aria-label="t('window.minimize')"
      :title="t('window.minimize')"
      @click="minimizeWindow"
    >
      <Minus :size="16" />
    </button>
    <button
      class="remote-desktop-toolbar-btn remote-desktop-toolbar-restore"
      :aria-label="t('remoteDesktop.restore')"
      :title="t('remoteDesktop.restore')"
      @click="emit('restore')"
    >
      <Copy :size="15" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import { Pin, PinOff, Minus, Copy } from '@lucide/vue'
import { Window } from '@wailsio/runtime'
import { useI18n } from '../i18n'

const { t } = useI18n()
defineProps<{
  host: string
  port: number
  resolution?: string
  scaleViewport: boolean
  disabled: boolean
}>()
const emit = defineEmits<{
  'update:scaleViewport': [value: boolean]
  restore: []
  focusDesktop: []
  minimize: []
  error: [error: unknown]
}>()

const fullscreenToolbar = ref<HTMLDivElement | null>(null)
const toolbarPinned = ref(true)
const toolbarExpanded = ref(true)
let toolbarHideTimer: ReturnType<typeof setTimeout> | null = null

function clearToolbarHideTimer() {
  if (toolbarHideTimer !== null) clearTimeout(toolbarHideTimer)
  toolbarHideTimer = null
}

function showFullscreenToolbar() {
  clearToolbarHideTimer()
  toolbarExpanded.value = true
}

function scheduleFullscreenToolbarHide() {
  clearToolbarHideTimer()
  if (toolbarPinned.value) return
  toolbarHideTimer = setTimeout(() => {
    toolbarHideTimer = null
    if (!fullscreenToolbar.value?.matches(':hover, :focus-within')) toolbarExpanded.value = false
  }, 600)
}

function focusDesktop() {
  emit('focusDesktop')
}

function toggleToolbarPin() {
  toolbarPinned.value = !toolbarPinned.value
  showFullscreenToolbar()
  focusDesktop()
  scheduleFullscreenToolbarHide()
}

async function minimizeWindow() {
  emit('minimize')
  try {
    await Window.Minimise()
  } catch (e) {
    emit('error', e)
    focusDesktop()
  }
}

onBeforeUnmount(clearToolbarHideTimer)
</script>

<style scoped>
.remote-desktop-fullscreen-edge {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 4px;
  z-index: 20;
}
.remote-desktop-fullscreen-toolbar {
  position: absolute;
  top: 0;
  left: 50%;
  z-index: 21;
  display: flex;
  align-items: center;
  gap: 0.375rem;
  width: min(36rem, 100%);
  min-width: 0;
  padding: 0.25rem 0.5rem;
  box-sizing: border-box;
  border: 1px solid var(--border-default);
  border-top: 0;
  border-radius: 0 0 0.625rem 0.625rem;
  background: var(--bg-elevated);
  color: var(--text-primary);
  box-shadow: 0 0.25rem 1rem #0006;
  transform: translateX(-50%);
  transition: transform 160ms ease, visibility 160ms;
  --wails-draggable: no-drag;
}
.remote-desktop-fullscreen-toolbar.is-hidden {
  transform: translate(-50%, -100%);
  visibility: hidden;
  pointer-events: none;
}
.remote-desktop-toolbar-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 1.875rem;
  height: 1.875rem;
  padding: 0;
  border: 0;
  border-radius: 0.25rem;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
}
.remote-desktop-toolbar-btn:hover,
.remote-desktop-toolbar-btn:focus-visible {
  background: var(--bg-hover);
  color: var(--text-primary);
}
.remote-desktop-toolbar-pin.is-active {
  color: var(--accent);
  background: var(--bg-hover);
}
.remote-desktop-toolbar-host {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  text-align: center;
  font-size: 0.8125rem;
}
.remote-desktop-toolbar-divider {
  flex: none;
  width: 1px;
  height: 1rem;
  background: var(--border-default);
}
.remote-desktop-toolbar-resolution {
  flex: none;
  white-space: nowrap;
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  color: var(--text-muted);
}
.remote-desktop-toolbar-scale {
  display: flex;
  align-items: center;
  flex: none;
  gap: 0.5rem;
  white-space: nowrap;
  font-size: 0.75rem;
}
@media (prefers-reduced-motion: reduce) {
  .remote-desktop-fullscreen-toolbar { transition: none; }
}
</style>
